package inboxv2

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
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
// Every method is best-effort by design: the legacy inbox_item write is still
// the source of truth while the switch is being rolled out, so a v2 failure
// must never turn a delivered notification into a dropped one. Failures are
// logged with enough context to be found and replayed later by the backfill,
// which is idempotent on delivery_key.
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

// Record writes one event + group upsert. It returns nothing: no caller may
// branch on the outcome, because doing so would let the v2 path change legacy
// behaviour while the switch is supposed to be invisible.
func (r *Recorder) Record(ctx context.Context, workspaceID, recipientID string, d Delivery, now time.Time) {
	if !r.Enabled() {
		return
	}
	if err := r.record(ctx, workspaceID, recipientID, d, now); err != nil {
		// Warn, not Error: the user still got their notification through the
		// legacy row. This is rollout telemetry, not an incident.
		slog.Warn("inbox v2 dual write failed",
			"type", string(d.Type),
			"workspace_id", workspaceID,
			"recipient_id", recipientID,
			"error", err)
	}
}

func (r *Recorder) record(ctx context.Context, workspaceID, recipientID string, d Delivery, now time.Time) error {
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

	_, err = r.store.Append(ctx, AppendParams{
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
