package service

import (
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service/inboxv2"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// inboxWriter builds the dual writer for a service. The switch inside decides
// whether anything reaches the v2 tables; with it off this is the old
// CreateInboxItem call.
func inboxWriter(tx TxStarter, q *db.Queries) *inboxv2.Writer {
	return inboxv2.NewWriter(tx, q)
}

// withSnapshot attaches the legacy render snapshot, flattening the producer's
// details map to strings. See inboxv2.LegacyPayload for why the flattening is
// not optional.
func withSnapshot(d inboxv2.Delivery, title, body, severity, issueID string, details map[string]any) inboxv2.Delivery {
	d.Payload = inboxv2.LegacyPayload{
		Title:    title,
		Body:     body,
		Severity: severity,
		IssueID:  issueID,
		Details:  inboxv2.StringDetails(details),
	}.Encode()
	return d
}

// quickCreateDelivery describes a quick-create outcome.
//
// The originating task is both the identity and the jump target: it survives a
// retry, and on the failure paths the issue may not exist at all, which is why
// those notifications open a standalone group instead of an issue one.
func quickCreateDelivery(t inboxv2.EventType, taskID pgtype.UUID, issueID string) inboxv2.Delivery {
	task := util.UUIDToString(taskID)
	d := inboxv2.Delivery{
		Type:       t,
		Identity:   inboxv2.IdentityInput{OriginTaskID: task},
		TargetKind: inboxv2.TargetRun,
		TargetID:   task,
		ActorType:  "agent",
	}
	if issueID != "" {
		d.SourceKind, d.SourceID = inboxv2.SourceIssue, issueID
	} else {
		d.SourceKind = inboxv2.SourceStandalone
	}
	return d
}

// issueSubscribedDelivery is the autopilot subscribing someone to a new issue.
// It happens once per (person, issue) at creation time, so the issue's creation
// timestamp identifies the occurrence.
func issueSubscribedDelivery(issue db.Issue, actorID string) inboxv2.Delivery {
	id := util.UUIDToString(issue.ID)
	return inboxv2.Delivery{
		Type: inboxv2.TypeIssueSubscribed,
		Identity: inboxv2.IdentityInput{
			IssueID:  id,
			Field:    "subscribed",
			ChangeID: issue.CreatedAt.Time.UTC().Format(time.RFC3339Nano),
		},
		SourceKind: inboxv2.SourceIssue,
		SourceID:   id,
		ActorType:  "agent",
		ActorID:    actorID,
	}
}
