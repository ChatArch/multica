// Package inboxv2 is the storage-side domain model for the Inbox refactor:
// an immutable event log (inbox_event) plus per-recipient state (inbox_group).
//
// Nothing in the delivery path calls this package yet. It lands ahead of the
// dual-write switch so the ordering, idempotency and read-cursor contracts can
// be exercised against a real database before any producer depends on them.
package inboxv2

import "fmt"

// EventType is a notification kind. The set is closed: the database carries the
// same list in the inbox_event_type_known CHECK, and a contract test walks this
// registry against a live database so the two cannot drift.
type EventType string

const (
	TypeIssueAssigned   EventType = "issue_assigned"
	TypeUnassigned      EventType = "unassigned"
	TypeAssigneeChanged EventType = "assignee_changed"
	TypeStatusChanged   EventType = "status_changed"
	TypePriorityChanged EventType = "priority_changed"
	TypeStartDateChange EventType = "start_date_changed"
	TypeDueDateChanged  EventType = "due_date_changed"
	TypeIssueSubscribed EventType = "issue_subscribed"

	TypeNewComment    EventType = "new_comment"
	TypeMentioned     EventType = "mentioned"
	TypeReactionAdded EventType = "reaction_added"

	TypeTaskCompleted  EventType = "task_completed"
	TypeTaskFailed     EventType = "task_failed"
	TypeAgentBlocked   EventType = "agent_blocked"
	TypeAgentCompleted EventType = "agent_completed"

	TypeAutopilotPaused EventType = "autopilot_paused"

	TypeQuickCreateDone        EventType = "quick_create_done"
	TypeQuickCreateFailed      EventType = "quick_create_failed"
	TypeQuickCreateUnconfirmed EventType = "quick_create_unconfirmed"
)

// TargetKind is the entity a click on this event should open.
type TargetKind string

const (
	TargetComment   TargetKind = "comment"
	TargetRun       TargetKind = "run"
	TargetAutopilot TargetKind = "autopilot"
)

// TargetRule says what a type is allowed to carry as a jump target.
//
// The old schema had no rule at all: the target lived in a free-form `details`
// map, so when the newest event had none the clients reached back into an older
// event and borrowed one. That is why a status-change row could open a comment
// from three days earlier. Making the rule explicit — and enforcing the two
// extremes in the database — removes the borrow rather than discouraging it.
type TargetRule int

const (
	// TargetForbidden: the notification is about the source itself, so a click
	// opens the source overview and scrolls to nothing.
	TargetForbidden TargetRule = iota
	// TargetRequired: the notification exists to point at one specific entity.
	TargetRequired
	// TargetOptional: the producer may or may not have an entity to point at.
	// Reserved for the agent/task types, whose per-type mapping is still an
	// open solution-design item (the "type -> legacy details keys" table). They
	// are deliberately left permissive here rather than guessed at.
	TargetOptional
)

// EventTypeSpec is the per-type contract.
type EventTypeSpec struct {
	// Rule constrains whether a target may/must be present.
	Rule TargetRule
	// Kind is the only target kind this type may use. Zero value means the
	// type carries no target (Rule == TargetForbidden).
	Kind TargetKind
}

// EventTypes is the registry. Keep it in sync with the CHECK constraints in
// migration 253; TestEventTypeRegistryMatchesDatabase enforces that.
var EventTypes = map[EventType]EventTypeSpec{
	// Field and status changes: about the issue, never about a sub-entity.
	TypeIssueAssigned:   {Rule: TargetForbidden},
	TypeUnassigned:      {Rule: TargetForbidden},
	TypeAssigneeChanged: {Rule: TargetForbidden},
	TypeStatusChanged:   {Rule: TargetForbidden},
	TypePriorityChanged: {Rule: TargetForbidden},
	TypeStartDateChange: {Rule: TargetForbidden},
	TypeDueDateChanged:  {Rule: TargetForbidden},
	TypeIssueSubscribed: {Rule: TargetForbidden},

	// Comment-bearing: the entire point is "open this comment".
	TypeNewComment:    {Rule: TargetRequired, Kind: TargetComment},
	TypeMentioned:     {Rule: TargetRequired, Kind: TargetComment},
	TypeReactionAdded: {Rule: TargetRequired, Kind: TargetComment},

	// Agent/task outcomes. Pending the solution-design mapping table.
	TypeTaskCompleted:          {Rule: TargetOptional, Kind: TargetRun},
	TypeTaskFailed:             {Rule: TargetOptional, Kind: TargetRun},
	TypeAgentBlocked:           {Rule: TargetOptional, Kind: TargetRun},
	TypeAgentCompleted:         {Rule: TargetOptional, Kind: TargetRun},
	TypeQuickCreateDone:        {Rule: TargetOptional, Kind: TargetRun},
	TypeQuickCreateFailed:      {Rule: TargetOptional, Kind: TargetRun},
	TypeQuickCreateUnconfirmed: {Rule: TargetOptional, Kind: TargetRun},

	TypeAutopilotPaused: {Rule: TargetOptional, Kind: TargetAutopilot},
}

// ValidateTarget checks a (type, target) pair against the registry.
//
// The database enforces the same rule for the required and forbidden cases.
// This exists so a producer fails with a usable message at the call site
// instead of surfacing a constraint violation from three layers down, and so
// the optional cases — which the database cannot express per type — still have
// one place that decides.
func ValidateTarget(t EventType, kind TargetKind, hasID bool) error {
	spec, ok := EventTypes[t]
	if !ok {
		return fmt.Errorf("inboxv2: unknown event type %q", t)
	}
	hasKind := kind != ""
	if hasKind != hasID {
		return fmt.Errorf("inboxv2: %s: target kind and id must be set together", t)
	}
	switch spec.Rule {
	case TargetForbidden:
		if hasKind {
			return fmt.Errorf("inboxv2: %s must not carry a target, got %q", t, kind)
		}
	case TargetRequired:
		if !hasKind {
			return fmt.Errorf("inboxv2: %s requires a %s target", t, spec.Kind)
		}
	}
	if hasKind && kind != spec.Kind {
		return fmt.Errorf("inboxv2: %s may only target %q, got %q", t, spec.Kind, kind)
	}
	return nil
}

// SourceKind identifies what a group is about.
type SourceKind string

const (
	// SourceIssue groups every notification about one issue into one row.
	SourceIssue SourceKind = "issue"
	// SourceStandalone is for notifications with no durable parent entity
	// (an autopilot pausing, a quick-create failing before an issue exists).
	SourceStandalone SourceKind = "standalone"
)

// KnownSourceKinds mirrors the source_kind CHECK in migration 253.
var KnownSourceKinds = []SourceKind{SourceIssue, SourceStandalone}
