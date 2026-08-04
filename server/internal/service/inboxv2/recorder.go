package inboxv2

import (
	"context"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
)

// DualWriteEnvVar gates the v2 write. Default off: with the switch closed the
// recorder is a no-op and inbox_item remains the only table any delivery
// touches, which is what makes deploying this change reversible without a
// migration.
const DualWriteEnvVar = "MULTICA_INBOX_V2_DUAL_WRITE"

// DualWriteEnabled reads the switch. Read per call rather than cached at
// construction so the switch can be flipped by restarting a single pod during
// rollout without a code change.
func DualWriteEnabled() bool {
	v := os.Getenv(DualWriteEnvVar)
	return v == "true" || v == "1"
}

// Delivery is what a producer knows about one notification, in the shape the v2
// tables need. Producers already hold every field; the legacy write just threw
// most of it away into a JSON blob.
type Delivery struct {
	Type EventType
	// Identity feeds the delivery key. See identityFor for what each type
	// requires.
	Identity IdentityInput

	// SourceKind/SourceID identify the group. Leaving SourceID zero with
	// SourceKind "issue" is a programming error, caught before any write.
	SourceKind SourceKind
	SourceID   string

	TargetKind TargetKind
	TargetID   string

	ActorType string
	ActorID   string

	// Payload is the render snapshot. Optional; defaults to {}.
	Payload []byte
}

// Recorder performs the v2 half of a dual write.
//
// The write is transactional, not best-effort: RecordInTx joins the caller's
// transaction so the legacy inbox_item row and the v2 group/event commit or
// roll back together. An earlier draft of this type swallowed v2 failures and
// left the backfill to repair them, which is the shape v2.1 §9 rules out — it
// produces windows where the two tables disagree, and "the backfill will fix
// it" is not a property anyone can verify at the time of the write.
type Recorder struct {
	store   *Store
	enabled func() bool
}

func NewRecorder(store *Store) *Recorder {
	return &Recorder{store: store, enabled: DualWriteEnabled}
}

// NewRecorderWithSwitch is for tests, which need to drive the switch directly
// rather than through the process environment.
func NewRecorderWithSwitch(store *Store, enabled func() bool) *Recorder {
	return &Recorder{store: store, enabled: enabled}
}

// Enabled reports whether this recorder would write.
func (r *Recorder) Enabled() bool {
	return r != nil && r.store != nil && r.enabled != nil && r.enabled()
}

// RecordInTx writes the v2 group/event inside the caller's transaction.
//
// It returns an error, and the caller MUST roll back on one. That is the whole
// contract: a producer that writes inbox_item and then ignores a v2 failure has
// silently created the divergence the dual write exists to prevent.
//
// With the switch off it returns nil without touching the transaction, so a
// producer's code path is identical either way and the switch alone decides
// whether v2 is written.
func (r *Recorder) RecordInTx(ctx context.Context, tx pgx.Tx, workspaceID, recipientID string, d Delivery, now time.Time) error {
	if !r.Enabled() {
		return nil
	}
	return r.record(ctx, tx, workspaceID, recipientID, d, now)
}

func (r *Recorder) record(ctx context.Context, tx pgx.Tx, workspaceID, recipientID string, d Delivery, now time.Time) error {
	key, err := DeliveryKey(workspaceID, recipientID, d.Type, d.Identity)
	if err != nil {
		return err
	}

	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return err
	}
	rcptID, err := util.ParseUUID(recipientID)
	if err != nil {
		return err
	}

	sourceKind := d.SourceKind
	if sourceKind == "" {
		sourceKind = SourceStandalone
	}
	sourceID, err := resolveSourceID(sourceKind, d.SourceID, key)
	if err != nil {
		return err
	}

	var targetID pgtype.UUID
	if d.TargetID != "" {
		targetID, err = util.ParseUUID(d.TargetID)
		if err != nil {
			return err
		}
	}

	var actorID pgtype.UUID
	if d.ActorID != "" {
		// An unparseable actor is not worth losing the event over: the actor is
		// decoration on the row, not part of its identity.
		if parsed, perr := util.ParseUUID(d.ActorID); perr == nil {
			actorID = parsed
		}
	}

	_, err = r.store.AppendInTx(ctx, tx, AppendParams{
		WorkspaceID: wsID,
		RecipientID: rcptID,
		SourceKind:  sourceKind,
		SourceID:    sourceID,
		Type:        d.Type,
		ActorType:   d.ActorType,
		ActorID:     actorID,
		TargetKind:  d.TargetKind,
		TargetID:    targetID,
		Payload:     d.Payload,
		DeliveryKey: key,
		Now:         now,
	})
	return err
}

// resolveSourceID turns a producer's source into the group's source_id.
//
// Standalone notifications have no durable parent, so their id is derived from
// the delivery key: a retry recomputes the same key, lands on the same id, and
// therefore reuses the group instead of opening a second one.
func resolveSourceID(kind SourceKind, raw, deliveryKey string) (pgtype.UUID, error) {
	if kind == SourceStandalone && raw == "" {
		derived := StandaloneSourceID(deliveryKey)
		return pgtype.UUID{Bytes: derived, Valid: true}, nil
	}
	parsed, err := util.ParseUUID(raw)
	if err != nil {
		return pgtype.UUID{}, err
	}
	return parsed, nil
}

// SourceForIssue is the common case: everything about one issue folds into one
// group for one person.
func SourceForIssue(issueID string) (SourceKind, string) {
	if issueID == "" {
		return SourceStandalone, ""
	}
	return SourceIssue, issueID
}

// UUIDString is a small helper so producers holding a pgtype.UUID do not each
// re-implement the empty check.
func UUIDString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return uuid.UUID(u.Bytes).String()
}
