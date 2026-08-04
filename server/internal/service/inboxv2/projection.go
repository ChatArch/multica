package inboxv2

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ReadEnvVar gates whether the legacy endpoints serve from the v2 tables.
//
// Separate from the dual-write switch on purpose: writing has to be on and
// verified for a while before reading can be, and if reading turns out wrong it
// must be revocable without also stopping the writes that a later backfill
// would otherwise have to repair.
const ReadEnvVar = "MULTICA_INBOX_V2_READ"

// ReadEnabled reports whether the legacy endpoints should project from v2.
func ReadEnabled() bool {
	v := os.Getenv(ReadEnvVar)
	return v == "true" || v == "1"
}

// LegacyProjection serves the old inbox_item-shaped API out of the v2 tables.
//
// Old clients — including App Store builds that will exist for months — keep
// their exact contract: one row per group (which is what they fold to anyway,
// so their fold becomes an identity operation), the same field names, the same
// id round-trip. §8 in full.
type LegacyProjection struct {
	q *db.Queries
}

func NewLegacyProjection(q *db.Queries) *LegacyProjection {
	return &LegacyProjection{q: q}
}

// archivedPageLimit matches the cap the legacy archived query already applied.
// The archive grows without end and v1 ships no pagination, so the response has
// to be bounded somewhere; newest-first means truncation drops the oldest rows
// and can never hide the row a deduplicated client would have rendered.
const archivedPageLimit = 200

// List projects the active inbox.
func (p *LegacyProjection) List(ctx context.Context, workspaceID, recipientID pgtype.UUID, now time.Time) ([]LegacyRow, error) {
	rows, err := p.q.ListInboxGroupsWithLatestEvent(ctx, db.ListInboxGroupsWithLatestEventParams{
		WorkspaceID: workspaceID,
		RecipientID: recipientID,
		Now:         pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		return nil, err
	}
	out := make([]LegacyRow, len(rows))
	for i, r := range rows {
		out[i] = RenderLegacy(r.InboxEvent, r.InboxGroup, "member")
	}
	return out, nil
}

// ListArchived projects the archived view.
func (p *LegacyProjection) ListArchived(ctx context.Context, workspaceID, recipientID pgtype.UUID) ([]LegacyRow, error) {
	rows, err := p.q.ListArchivedInboxGroupsWithLatestEvent(ctx, db.ListArchivedInboxGroupsWithLatestEventParams{
		WorkspaceID: workspaceID,
		RecipientID: recipientID,
		PageSize:    archivedPageLimit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]LegacyRow, len(rows))
	for i, r := range rows {
		out[i] = RenderLegacy(r.InboxEvent, r.InboxGroup, "member")
	}
	return out, nil
}

// ErrNotFound is returned when an event id does not resolve to a group this
// recipient owns. Callers map it to 404 — the same answer for "does not exist"
// and "is not yours", so an id cannot be probed for existence.
var ErrNotFound = errors.New("inboxv2: inbox item not found")

// resolve turns the event id an old client holds into the group a write applies
// to. This is the id translation §8 calls for: legacy rows are addressed by
// event id, but every piece of state lives on the group.
func (p *LegacyProjection) resolve(ctx context.Context, eventID, workspaceID, recipientID pgtype.UUID) (db.FindInboxGroupByEventIDRow, error) {
	row, err := p.q.FindInboxGroupByEventID(ctx, db.FindInboxGroupByEventIDParams{
		EventID:     eventID,
		WorkspaceID: workspaceID,
		RecipientID: recipientID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.FindInboxGroupByEventIDRow{}, ErrNotFound
	}
	return row, err
}

// MarkRead projects POST /api/inbox/{id}/read.
//
// The old endpoint marked one row; the new one advances the group's cursor to
// the event that row represents. Because old clients always hold the newest
// event of a group, that is the same user-visible outcome — but it now also
// covers the older events the old schema left as ghost-unread rows.
//
// observed_state_version comes from the group as it is right now rather than
// from the client, because the legacy request carries no version. That makes
// this an unconditional read, which matches what the old endpoint did.
func (p *LegacyProjection) MarkRead(ctx context.Context, eventID, workspaceID, recipientID pgtype.UUID, now time.Time) (LegacyRow, error) {
	row, err := p.resolve(ctx, eventID, workspaceID, recipientID)
	if err != nil {
		return LegacyRow{}, err
	}
	group, err := p.q.MarkInboxGroupRead(ctx, db.MarkInboxGroupReadParams{
		ID:                   row.InboxGroup.ID,
		WorkspaceID:          workspaceID,
		RecipientID:          recipientID,
		ObservedSeq:          row.InboxEvent.EventSeq,
		ObservedStateVersion: row.InboxGroup.StateVersion,
		Now:                  pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		return LegacyRow{}, err
	}
	return RenderLegacy(row.InboxEvent, group, "member"), nil
}

// MarkUnread projects POST /api/inbox/{id}/unread.
func (p *LegacyProjection) MarkUnread(ctx context.Context, eventID, workspaceID, recipientID pgtype.UUID, now time.Time) (LegacyRow, error) {
	row, err := p.resolve(ctx, eventID, workspaceID, recipientID)
	if err != nil {
		return LegacyRow{}, err
	}
	group, err := p.q.MarkInboxGroupUnread(ctx, db.MarkInboxGroupUnreadParams{
		ID:          row.InboxGroup.ID,
		WorkspaceID: workspaceID,
		RecipientID: recipientID,
		Now:         pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		return LegacyRow{}, err
	}
	return RenderLegacy(row.InboxEvent, group, "member"), nil
}

// Archive projects POST /api/inbox/{id}/archive.
//
// The old endpoint archived the row and then swept its issue siblings in a
// second statement. Here archiving is a single group update, because the group
// IS the issue's row — the sweep existed only to simulate that.
func (p *LegacyProjection) Archive(ctx context.Context, eventID, workspaceID, recipientID pgtype.UUID, now time.Time) (LegacyRow, error) {
	row, err := p.resolve(ctx, eventID, workspaceID, recipientID)
	if err != nil {
		return LegacyRow{}, err
	}
	group, err := p.q.ArchiveInboxGroup(ctx, db.ArchiveInboxGroupParams{
		ID:          row.InboxGroup.ID,
		WorkspaceID: workspaceID,
		RecipientID: recipientID,
		Now:         pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		return LegacyRow{}, err
	}
	return RenderLegacy(row.InboxEvent, group, "member"), nil
}

// Unarchive projects POST /api/inbox/{id}/unarchive.
func (p *LegacyProjection) Unarchive(ctx context.Context, eventID, workspaceID, recipientID pgtype.UUID, now time.Time) (LegacyRow, error) {
	row, err := p.resolve(ctx, eventID, workspaceID, recipientID)
	if err != nil {
		return LegacyRow{}, err
	}
	group, err := p.q.UnarchiveInboxGroup(ctx, db.UnarchiveInboxGroupParams{
		ID:          row.InboxGroup.ID,
		WorkspaceID: workspaceID,
		RecipientID: recipientID,
		Now:         pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		return LegacyRow{}, err
	}
	return RenderLegacy(row.InboxEvent, group, "member"), nil
}

// CountUnread projects GET /api/inbox/unread-count.
//
// The old schema had two disagreeing counts — one over raw rows, one folded by
// issue in the client. This is the folded one, which is what every badge
// actually wanted.
func (p *LegacyProjection) CountUnread(ctx context.Context, workspaceID, recipientID pgtype.UUID, now time.Time) (int64, error) {
	return p.q.CountUnreadInboxGroups(ctx, db.CountUnreadInboxGroupsParams{
		WorkspaceID: workspaceID,
		RecipientID: recipientID,
		Now:         pgtype.Timestamptz{Time: now, Valid: true},
	})
}

// WorkspaceUnread is one workspace's count in the cross-workspace summary.
type WorkspaceUnread struct {
	WorkspaceID pgtype.UUID
	Count       int64
}

// UnreadSummary projects GET /api/inbox/unread-summary.
func (p *LegacyProjection) UnreadSummary(ctx context.Context, recipientID pgtype.UUID, now time.Time) ([]WorkspaceUnread, error) {
	rows, err := p.q.CountUnreadInboxGroupsByWorkspace(ctx, db.CountUnreadInboxGroupsByWorkspaceParams{
		RecipientID: recipientID,
		Now:         pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		return nil, err
	}
	out := make([]WorkspaceUnread, len(rows))
	for i, r := range rows {
		out[i] = WorkspaceUnread{WorkspaceID: r.WorkspaceID, Count: r.Count}
	}
	return out, nil
}

// The four batch endpoints. Each returns the number of groups affected, which
// is what the old endpoints returned as {"count": n} and what both clients use
// only to decide whether to invalidate — so the change from "rows" to "groups"
// is invisible to them.

func (p *LegacyProjection) MarkAllRead(ctx context.Context, workspaceID, recipientID pgtype.UUID, now time.Time) (int64, error) {
	return p.q.MarkAllInboxGroupsRead(ctx, db.MarkAllInboxGroupsReadParams{
		WorkspaceID: workspaceID, RecipientID: recipientID,
		Now: pgtype.Timestamptz{Time: now, Valid: true},
	})
}

func (p *LegacyProjection) ArchiveAll(ctx context.Context, workspaceID, recipientID pgtype.UUID, now time.Time) (int64, error) {
	return p.q.ArchiveAllInboxGroups(ctx, db.ArchiveAllInboxGroupsParams{
		WorkspaceID: workspaceID, RecipientID: recipientID,
		Now: pgtype.Timestamptz{Time: now, Valid: true},
	})
}

func (p *LegacyProjection) ArchiveRead(ctx context.Context, workspaceID, recipientID pgtype.UUID, now time.Time) (int64, error) {
	return p.q.ArchiveReadInboxGroups(ctx, db.ArchiveReadInboxGroupsParams{
		WorkspaceID: workspaceID, RecipientID: recipientID,
		Now: pgtype.Timestamptz{Time: now, Valid: true},
	})
}

func (p *LegacyProjection) ArchiveCompleted(ctx context.Context, workspaceID, recipientID pgtype.UUID, now time.Time) (int64, error) {
	return p.q.ArchiveCompletedInboxGroups(ctx, db.ArchiveCompletedInboxGroupsParams{
		WorkspaceID: workspaceID, RecipientID: recipientID,
		Now: pgtype.Timestamptz{Time: now, Valid: true},
	})
}
