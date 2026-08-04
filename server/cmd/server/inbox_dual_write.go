package main

import (
	"time"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/service/inboxv2"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The delivery descriptors below are the producer half of the type contract in
// inboxv2.EventTypes. Each one records what this producer actually knows, so
// the identity that feeds delivery_key is derived once here rather than being
// re-guessed at each of the sixteen call sites.

func issueDelivery(t inboxv2.EventType, issueID string, e events.Event) inboxv2.Delivery {
	return inboxv2.Delivery{
		Type:       t,
		SourceKind: inboxv2.SourceIssue,
		SourceID:   issueID,
		ActorType:  e.ActorType,
		ActorID:    e.ActorID,
	}
}

// fieldChangeDelivery covers status/priority/date/assignee edits. ChangeID is
// the issue's updated_at: the producers hold it, a retry recomputes it, and two
// distinct edits differ — which (issue, field, from, to) would not, so a status
// flipped away and back would collide with the original edit.
func fieldChangeDelivery(t inboxv2.EventType, issue handler.IssueResponse, field string, e events.Event) inboxv2.Delivery {
	d := issueDelivery(t, issue.ID, e)
	d.Identity = inboxv2.IdentityInput{IssueID: issue.ID, Field: field, ChangeID: issue.UpdatedAt}
	return d
}

// assignmentDelivery is a field change on the assignee, addressed to the person
// gaining or losing the issue rather than to its subscribers.
func assignmentDelivery(t inboxv2.EventType, issue handler.IssueResponse, e events.Event) inboxv2.Delivery {
	return fieldChangeDelivery(t, issue, "assignee", e)
}

// commentDelivery is a notification anchored on a specific comment.
func commentDelivery(t inboxv2.EventType, issueID, commentID string, e events.Event) inboxv2.Delivery {
	d := issueDelivery(t, issueID, e)
	d.Identity = inboxv2.IdentityInput{CommentID: commentID}
	if commentID != "" {
		d.TargetKind = inboxv2.TargetComment
		d.TargetID = commentID
	}
	return d
}

// descriptionMentionDelivery is a mention that came from the issue description
// rather than a comment, so it has no comment to point at. The edit identifies
// it, the same way a field change is identified.
func descriptionMentionDelivery(issue handler.IssueResponse, e events.Event) inboxv2.Delivery {
	d := issueDelivery(inboxv2.TypeMentioned, issue.ID, e)
	d.Identity = inboxv2.IdentityInput{IssueID: issue.ID, ChangeID: issue.UpdatedAt}
	return d
}

// reactionDelivery covers both anchors: a reaction on a comment carries it, a
// reaction on the issue itself does not. Emoji and actor are part of the
// identity because one comment collects many reactions and each is its own
// notification.
func reactionDelivery(issueID, commentID, emoji string, e events.Event) inboxv2.Delivery {
	d := issueDelivery(inboxv2.TypeReactionAdded, issueID, e)
	d.Identity = inboxv2.IdentityInput{
		CommentID: commentID,
		IssueID:   issueID,
		Emoji:     emoji,
		ActorID:   e.ActorID,
	}
	if commentID != "" {
		d.TargetKind = inboxv2.TargetComment
		d.TargetID = commentID
	}
	return d
}

// autopilotPausedDelivery has no issue behind it, so it opens a standalone
// group whose id is derived from the delivery key.
//
// The occurrence is the pause itself, identified by the autopilot's updated_at
// as returned by the pause query. A later sweep cannot re-pause an already
// paused autopilot, so a retry of this tick recomputes the same key while a
// genuine second pause — after someone re-enables it and the failures return —
// gets its own.
func autopilotPausedDelivery(a db.Autopilot) inboxv2.Delivery {
	id := util.UUIDToString(a.ID)
	return inboxv2.Delivery{
		Type: inboxv2.TypeAutopilotPaused,
		Identity: inboxv2.IdentityInput{
			AutopilotID:  id,
			OccurrenceID: a.UpdatedAt.Time.UTC().Format(time.RFC3339Nano),
		},
		SourceKind: inboxv2.SourceStandalone,
		TargetKind: inboxv2.TargetAutopilot,
		TargetID:   id,
		ActorType:  "system",
	}
}

// taskFailedDelivery points at the run that failed. The target is required by
// the frozen contract, and both task:failed publishers carry task_id.
func taskFailedDelivery(issueID, taskID, agentID string) inboxv2.Delivery {
	return inboxv2.Delivery{
		Type:       inboxv2.TypeTaskFailed,
		Identity:   inboxv2.IdentityInput{RunID: taskID},
		SourceKind: inboxv2.SourceIssue,
		SourceID:   issueID,
		TargetKind: inboxv2.TargetRun,
		TargetID:   taskID,
		ActorType:  "agent",
		ActorID:    agentID,
	}
}
