package inboxv2

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// deliveryKeyAlgorithm is the version tag every key carries.
//
// The key is a hash, so its inputs are unrecoverable once stored. Without a
// version tag, changing the derivation later would silently stop retries from
// matching the keys already in the table: the same delivery would hash
// differently, miss the unique constraint, and produce a duplicate
// notification. The tag makes that change a visible, migratable event instead.
const deliveryKeyAlgorithm = "v1"

// EntityIdentity is the stable identity of whatever produced a notification.
// It is deliberately not the event id: a retried delivery generates a fresh
// event id, so keying on it would make every retry look new.
type EntityIdentity struct {
	// Parts are the identity components, in a fixed order per type.
	Parts []string
}

// identityFor is the table-driven mapping from an event type to the producer
// entity that identifies it. Keeping it in one table rather than letting each
// producer roll its own is the point: seven call sites inventing their own
// idempotency string is how the same notification ends up delivered twice.
//
// Each function receives the type-specific identity inputs already resolved by
// the caller and returns the ordered parts. A type absent from this map has no
// producer-stable identity and must supply one explicitly.
var identityFor = map[EventType]func(in IdentityInput) (EntityIdentity, error){
	// Comment-bearing types are identified by the comment they point at.
	TypeNewComment: byComment,
	TypeMentioned:  byComment,
	// A reaction is not identified by its comment alone: the same comment can
	// collect many reactions, and each is a separate notification. Reactions on
	// the issue itself have no comment, so the issue stands in for it — see the
	// reaction_added note in EventTypes.
	TypeReactionAdded: func(in IdentityInput) (EntityIdentity, error) {
		anchor := in.CommentID
		if anchor == "" {
			anchor = in.IssueID
		}
		if anchor == "" || in.Emoji == "" || in.ActorID == "" {
			return EntityIdentity{}, fmt.Errorf("inboxv2: reaction_added needs a comment or issue, plus emoji and actor")
		}
		return EntityIdentity{Parts: []string{anchor, in.Emoji, in.ActorID}}, nil
	},

	// Agent/task outcomes are identified by the run that produced them.
	TypeTaskCompleted:  byRun,
	TypeTaskFailed:     byRun,
	TypeAgentBlocked:   byRun,
	TypeAgentCompleted: byRun,

	// Quick-create outcomes are identified by the originating task, which
	// survives a retry; the issue may not exist yet.
	TypeQuickCreateDone:        byOriginTask,
	TypeQuickCreateFailed:      byOriginTask,
	TypeQuickCreateUnconfirmed: byOriginTask,

	TypeAutopilotPaused: func(in IdentityInput) (EntityIdentity, error) {
		if in.AutopilotID == "" || in.OccurrenceID == "" {
			return EntityIdentity{}, fmt.Errorf("inboxv2: autopilot_paused needs autopilot and occurrence")
		}
		return EntityIdentity{Parts: []string{in.AutopilotID, in.OccurrenceID}}, nil
	},

	// Field and status changes are identified by the specific change event, so
	// two edits of the same field stay two notifications while a retry of one
	// edit stays one.
	TypeIssueAssigned:   byFieldChange,
	TypeUnassigned:      byFieldChange,
	TypeAssigneeChanged: byFieldChange,
	TypeStatusChanged:   byFieldChange,
	TypePriorityChanged: byFieldChange,
	TypeStartDateChange: byFieldChange,
	TypeDueDateChanged:  byFieldChange,
	TypeIssueSubscribed: byFieldChange,
}

// IdentityInput carries every component any type might need. Producers fill in
// only the fields their type uses; the mapping above validates the rest.
type IdentityInput struct {
	CommentID    string
	Emoji        string
	ActorID      string
	RunID        string
	OriginTaskID string
	AutopilotID  string
	OccurrenceID string
	IssueID      string
	Field        string
	ChangeID     string
}

func byComment(in IdentityInput) (EntityIdentity, error) {
	if in.CommentID == "" {
		return EntityIdentity{}, fmt.Errorf("inboxv2: comment id required")
	}
	return EntityIdentity{Parts: []string{in.CommentID}}, nil
}

func byRun(in IdentityInput) (EntityIdentity, error) {
	if in.RunID == "" {
		return EntityIdentity{}, fmt.Errorf("inboxv2: run id required")
	}
	return EntityIdentity{Parts: []string{in.RunID}}, nil
}

func byOriginTask(in IdentityInput) (EntityIdentity, error) {
	if in.OriginTaskID == "" {
		return EntityIdentity{}, fmt.Errorf("inboxv2: origin task id required")
	}
	return EntityIdentity{Parts: []string{in.OriginTaskID}}, nil
}

// byFieldChange identifies one edit of one field on one issue.
//
// ChangeID is the issue's updated_at at the moment of the edit, which is what
// the producers actually have: issue:updated carries the updated IssueResponse
// and no separate change-event id exists on the bus. It has the property the
// key needs — a retry of the same edit recomputes the same value, while two
// distinct edits differ — which (issue, field, from, to) would not: flipping a
// status to in_progress and back to todo would otherwise collide with the
// original edit and be silently deduplicated away.
func byFieldChange(in IdentityInput) (EntityIdentity, error) {
	if in.IssueID == "" || in.Field == "" || in.ChangeID == "" {
		return EntityIdentity{}, fmt.Errorf("inboxv2: field change needs issue, field and change id (issue updated_at)")
	}
	return EntityIdentity{Parts: []string{in.IssueID, in.Field, in.ChangeID}}, nil
}

// DeliveryKey derives the idempotency key for one delivery.
//
// Recipient is part of the key because the same comment notifies several
// people and each of them gets their own event. Workspace is included so a key
// can never collide across tenants even if an entity id were reused.
//
// The result is unique globally, not per group, which is what lets a retry
// still collide after the grouping rule itself has changed.
func DeliveryKey(workspaceID, recipientID string, t EventType, in IdentityInput) (string, error) {
	if workspaceID == "" || recipientID == "" {
		return "", fmt.Errorf("inboxv2: workspace and recipient required")
	}
	fn, ok := identityFor[t]
	if !ok {
		return "", fmt.Errorf("inboxv2: no delivery identity registered for %q", t)
	}
	ident, err := fn(in)
	if err != nil {
		return "", err
	}
	// "\x1f" (unit separator) cannot appear in a UUID or an emoji shortcode, so
	// joining on it keeps ("a","bc") and ("ab","c") from hashing alike.
	parts := append([]string{workspaceID, recipientID, string(t)}, ident.Parts...)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return deliveryKeyAlgorithm + ":" + hex.EncodeToString(sum[:]), nil
}

// standaloneNamespace namespaces the UUIDv5 used for standalone group ids. A
// fixed random UUID, not a well-known one, so these ids cannot collide with
// v5 ids some other subsystem derives from the same name.
var standaloneNamespace = uuid.MustParse("6f1d5b9e-3c2a-4f77-9c1e-8a4b0d6e2f31")

// StandaloneSourceID derives a deterministic group id for a notification with
// no durable parent entity.
//
// source_id is NOT NULL for every kind, so standalone rows need *some* id, and
// deriving it from the delivery key is what makes a retry land on the group it
// already created. Using a fresh UUID here would open a second group per retry
// and put the same notification in the inbox twice.
func StandaloneSourceID(deliveryKey string) uuid.UUID {
	return uuid.NewSHA1(standaloneNamespace, []byte(deliveryKey))
}
