package inboxv2

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TxStarter matches the interface the other services in this package tree use.
type TxStarter interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Store owns the inbox_event / inbox_group tables.
type Store struct {
	Queries *db.Queries
	Tx      TxStarter
}

func NewStore(q *db.Queries, tx TxStarter) *Store {
	return &Store{Queries: q, Tx: tx}
}

// AppendParams is one delivery to one recipient.
type AppendParams struct {
	WorkspaceID pgtype.UUID
	RecipientID pgtype.UUID
	SourceKind  SourceKind
	SourceID    pgtype.UUID

	Type      EventType
	ActorType string
	ActorID   pgtype.UUID

	TargetKind TargetKind
	TargetID   pgtype.UUID

	Payload        []byte
	PayloadVersion int16

	DeliveryKey string
	Now         time.Time
}

// AppendResult reports what the append did. Deduplicated is true when the
// delivery had already been recorded, in which case nothing advanced and the
// caller must not emit a websocket event — re-emitting would make a retry look
// like a second notification to every connected client.
type AppendResult struct {
	Event        db.InboxEvent
	Group        db.InboxGroup
	Deduplicated bool
}

// uniqueViolation is the SQLSTATE for a unique constraint violation.
const uniqueViolation = "23505"

// Append records one delivery.
//
// The whole thing is a single transaction, and the order inside it is the
// contract:
//
//  1. Probe by delivery key. A hit returns the existing event and the group's
//     current state without advancing anything.
//  2. Acquire (create or lock) the group row.
//  3. Allocate event_seq as latest_seq+1 while holding that lock.
//  4. Insert the event, then advance the group.
//
// Sequence allocation lives inside the lock, so two concurrent deliveries to
// the same group serialise rather than racing for the same number. And because
// the allocation is written in the same transaction as the insert, a failure
// anywhere rolls the number back too: the sequence has no gaps and no phantom
// unread, by transaction atomicity rather than by convention.
//
// Step 1 is only an optimisation. Two transactions can both miss it, so the
// UNIQUE (delivery_key) is the real arbiter: the loser gets a unique violation,
// rolls back whole, and re-reads the winner's rows.
func (s *Store) Append(ctx context.Context, p AppendParams) (AppendResult, error) {
	tx, err := s.Tx.Begin(ctx)
	if err != nil {
		return AppendResult{}, fmt.Errorf("inboxv2: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	res, err := s.AppendInTx(ctx, tx, p)
	if err != nil {
		return AppendResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AppendResult{}, fmt.Errorf("inboxv2: commit: %w", err)
	}
	return res, nil
}

// AppendInTx records one delivery inside a transaction the caller owns.
//
// This is the form the dual write uses: the legacy inbox_item insert and the v2
// event must commit or roll back together, so the v2 half cannot open its own
// transaction. A caller that has already written the legacy row simply passes
// its tx here and commits once.
//
// The append itself is unchanged — probe by delivery key, lock (or create) the
// group, allocate event_seq as latest_seq+1 under that lock, insert, advance —
// but the surrounding transaction is now the caller's, which changes one thing
// materially: a unique violation would abort the WHOLE transaction, taking the
// caller's legacy insert with it. Since a duplicate delivery is expected and
// benign, the insert runs inside a savepoint (pgx implements a nested Begin as
// one). A conflict rolls back to the savepoint, leaves the outer transaction
// healthy, and is reported as a deduplicated result.
//
// Note that the group row lock is now held until the caller commits rather than
// until the append finishes. Groups are per (recipient, source), so two writers
// only contend when the same person gets two notifications about the same issue
// at the same instant.
func (s *Store) AppendInTx(ctx context.Context, tx pgx.Tx, p AppendParams) (AppendResult, error) {
	if err := ValidateTarget(p.Type, p.TargetKind, p.TargetID.Valid); err != nil {
		return AppendResult{}, err
	}
	if p.DeliveryKey == "" {
		return AppendResult{}, errors.New("inboxv2: delivery key required")
	}

	q := s.Queries.WithTx(tx)
	now := pgtype.Timestamptz{Time: p.Now, Valid: true}

	// Fast path: this delivery was already recorded.
	if existing, err := q.FindInboxEventByDeliveryKey(ctx, p.DeliveryKey); err == nil {
		return s.dedupResult(ctx, q, existing, p)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return AppendResult{}, fmt.Errorf("inboxv2: delivery key probe: %w", err)
	}

	sp, err := tx.Begin(ctx)
	if err != nil {
		return AppendResult{}, fmt.Errorf("inboxv2: savepoint: %w", err)
	}
	res, err := s.appendWithin(ctx, sp, p, now)
	if err != nil {
		// Roll back only the v2 attempt. The caller's transaction — and the
		// legacy row it already wrote — survive.
		_ = sp.Rollback(ctx)

		// The probe above can miss when a concurrent transaction commits
		// between it and the insert. The unique constraint is the real
		// arbiter; re-read the winner's rows and report a duplicate.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			existing, ferr := q.FindInboxEventByDeliveryKey(ctx, p.DeliveryKey)
			if ferr != nil {
				return AppendResult{}, fmt.Errorf("inboxv2: reload after conflict: %w", ferr)
			}
			return s.dedupResult(ctx, q, existing, p)
		}
		return AppendResult{}, err
	}
	if err := sp.Commit(ctx); err != nil {
		return AppendResult{}, fmt.Errorf("inboxv2: release savepoint: %w", err)
	}
	return res, nil
}

func (s *Store) appendWithin(ctx context.Context, sp pgx.Tx, p AppendParams, now pgtype.Timestamptz) (AppendResult, error) {
	q := s.Queries.WithTx(sp)

	group, err := q.AcquireInboxGroup(ctx, db.AcquireInboxGroupParams{
		WorkspaceID: p.WorkspaceID,
		RecipientID: p.RecipientID,
		SourceKind:  string(p.SourceKind),
		SourceID:    p.SourceID,
		Now:         now,
	})
	if err != nil {
		return AppendResult{}, fmt.Errorf("inboxv2: acquire group: %w", err)
	}

	seq := group.LatestSeq + 1
	payload := p.Payload
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	version := p.PayloadVersion
	if version == 0 {
		version = 1
	}

	event, err := q.InsertInboxEvent(ctx, db.InsertInboxEventParams{
		GroupID:        group.ID,
		WorkspaceID:    p.WorkspaceID,
		EventSeq:       seq,
		Type:           string(p.Type),
		ActorType:      textOrNull(p.ActorType),
		ActorID:        p.ActorID,
		TargetKind:     textOrNull(string(p.TargetKind)),
		TargetID:       p.TargetID,
		Payload:        payload,
		PayloadVersion: version,
		DeliveryKey:    p.DeliveryKey,
		Now:            now,
	})
	if err != nil {
		return AppendResult{}, err
	}

	group, err = q.AdvanceInboxGroupForEvent(ctx, db.AdvanceInboxGroupForEventParams{
		EventSeq: seq,
		EventID:  event.ID,
		Now:      now,
		ID:       group.ID,
	})
	if err != nil {
		return AppendResult{}, fmt.Errorf("inboxv2: advance group: %w", err)
	}
	return AppendResult{Event: event, Group: group}, nil
}

func (s *Store) dedupResult(ctx context.Context, q *db.Queries, existing db.InboxEvent, p AppendParams) (AppendResult, error) {
	group, err := q.GetInboxGroupForRecipient(ctx, db.GetInboxGroupForRecipientParams{
		ID:          existing.GroupID,
		WorkspaceID: p.WorkspaceID,
		RecipientID: p.RecipientID,
	})
	if err != nil {
		return AppendResult{}, fmt.Errorf("inboxv2: load group for deduplicated event: %w", err)
	}
	return AppendResult{Event: existing, Group: group, Deduplicated: true}, nil
}

func textOrNull(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// IsUnread is the single derived unread rule. Every count, badge and row state
// goes through it so the three clients cannot drift into separate definitions
// the way they did with the old boolean.
func IsUnread(g db.InboxGroup) bool {
	return g.ManualUnread || g.ReadThroughSeq < g.LatestSeq
}

// InInbox reports whether a group belongs in the active list right now.
func InInbox(g db.InboxGroup, now time.Time) bool {
	if g.ArchivedAt.Valid {
		return false
	}
	if g.SnoozedUntil.Valid && !g.SnoozedUntil.Time.Before(now) {
		return false
	}
	return true
}
