package inboxv2

import (
	"strings"
	"testing"
)

const (
	wsA   = "11111111-1111-1111-1111-111111111111"
	userA = "22222222-2222-2222-2222-222222222222"
	userB = "33333333-3333-3333-3333-333333333333"
	cmtA  = "44444444-4444-4444-4444-444444444444"
)

func TestDeliveryKeyIsStableAcrossRetries(t *testing.T) {
	in := IdentityInput{CommentID: cmtA}
	first, err := DeliveryKey(wsA, userA, TypeNewComment, in)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := DeliveryKey(wsA, userA, TypeNewComment, in)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first != second {
		t.Fatalf("retry produced a different key:\n  %s\n  %s", first, second)
	}
}

func TestDeliveryKeyCarriesAlgorithmVersion(t *testing.T) {
	key, err := DeliveryKey(wsA, userA, TypeNewComment, IdentityInput{CommentID: cmtA})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "v1:") {
		t.Fatalf("key must carry an algorithm version so a future derivation change is visible, got %q", key)
	}
}

func TestDeliveryKeySeparatesRecipients(t *testing.T) {
	in := IdentityInput{CommentID: cmtA}
	a, _ := DeliveryKey(wsA, userA, TypeNewComment, in)
	b, _ := DeliveryKey(wsA, userB, TypeNewComment, in)
	if a == b {
		t.Fatal("the same comment notifying two people must produce two keys")
	}
}

func TestDeliveryKeySeparatesTypes(t *testing.T) {
	in := IdentityInput{CommentID: cmtA}
	a, _ := DeliveryKey(wsA, userA, TypeNewComment, in)
	b, _ := DeliveryKey(wsA, userA, TypeMentioned, in)
	if a == b {
		t.Fatal("a comment that both mentions and notifies must produce two keys")
	}
}

// The separator matters: without it ("ab","c") and ("a","bc") hash alike, and
// two unrelated deliveries would silently deduplicate into one.
func TestDeliveryKeyIsNotAmbiguousAcrossPartBoundaries(t *testing.T) {
	a, err := DeliveryKey(wsA, userA, TypeReactionAdded, IdentityInput{
		CommentID: "ab", Emoji: "c", ActorID: userB,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := DeliveryKey(wsA, userA, TypeReactionAdded, IdentityInput{
		CommentID: "a", Emoji: "bc", ActorID: userB,
	})
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("part boundaries must be unambiguous")
	}
}

func TestDeliveryKeyRejectsMissingIdentity(t *testing.T) {
	if _, err := DeliveryKey(wsA, userA, TypeNewComment, IdentityInput{}); err == nil {
		t.Fatal("new_comment without a comment id must not produce a key")
	}
	if _, err := DeliveryKey("", userA, TypeNewComment, IdentityInput{CommentID: cmtA}); err == nil {
		t.Fatal("a key without a workspace must be rejected")
	}
}

// Every registered type needs a delivery identity, otherwise a producer for it
// has no way to be idempotent and retries would duplicate.
func TestEveryEventTypeHasADeliveryIdentity(t *testing.T) {
	for typ := range EventTypes {
		if _, ok := identityFor[typ]; !ok {
			t.Errorf("event type %q has no delivery identity registered", typ)
		}
	}
	for typ := range identityFor {
		if _, ok := EventTypes[typ]; !ok {
			t.Errorf("delivery identity registered for unknown event type %q", typ)
		}
	}
}

func TestStandaloneSourceIDIsDerivedFromDeliveryKey(t *testing.T) {
	key, err := DeliveryKey(wsA, userA, TypeAutopilotPaused, IdentityInput{
		AutopilotID: cmtA, OccurrenceID: "run-7",
	})
	if err != nil {
		t.Fatal(err)
	}
	// A retry recomputes the same key, so it must land on the same group
	// instead of opening a second one.
	if StandaloneSourceID(key) != StandaloneSourceID(key) {
		t.Fatal("standalone source id must be deterministic")
	}
	other, _ := DeliveryKey(wsA, userA, TypeAutopilotPaused, IdentityInput{
		AutopilotID: cmtA, OccurrenceID: "run-8",
	})
	if StandaloneSourceID(key) == StandaloneSourceID(other) {
		t.Fatal("different deliveries must not share a standalone group")
	}
}

func TestValidateTarget(t *testing.T) {
	tests := []struct {
		name    string
		typ     EventType
		kind    TargetKind
		hasID   bool
		wantErr bool
	}{
		{"comment type with comment target", TypeNewComment, TargetComment, true, false},
		{"comment type without target", TypeNewComment, "", false, true},
		{"comment type with wrong kind", TypeNewComment, TargetRun, true, true},
		{"status change without target", TypeStatusChanged, "", false, false},
		{"status change with target", TypeStatusChanged, TargetComment, true, true},
		{"optional type with target", TypeTaskFailed, TargetRun, true, false},
		{"optional type without target", TypeTaskFailed, "", false, false},
		{"kind without id", TypeTaskFailed, TargetRun, false, true},
		{"id without kind", TypeTaskFailed, "", true, true},
		{"unknown type", EventType("nope"), "", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTarget(tc.typ, tc.kind, tc.hasID)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateTarget(%q,%q,%v) error = %v, wantErr %v",
					tc.typ, tc.kind, tc.hasID, err, tc.wantErr)
			}
		})
	}
}
