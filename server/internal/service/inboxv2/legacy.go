package inboxv2

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// LegacyPayload is the render snapshot an event carries.
//
// The v2 tables deliberately do not store title/body/details as columns —
// wording should be changeable and translatable after the fact. But the legacy
// REST and WS shapes need those strings, and §8 requires the projection to
// render with zero database queries so the list path cannot become N+1. The
// resolution is this: capture what rendering needs at delivery time, inside the
// event's payload, and make the renderer a pure function of it.
//
// Details is map[string]string, not map[string]any. Every client parses the
// legacy `details` as a string->string map, and because the inbox endpoint
// returns an ARRAY, a single non-string value fails the whole parse and blanks
// the entire list rather than one row (apps/mobile/data/schemas.ts InboxItemSchema,
// consumed through parseWithFallback in apps/mobile/data/api.ts). The concrete
// type turns that from a convention a reviewer has to notice into a compile
// error.
type LegacyPayload struct {
	Title       string            `json:"title"`
	Body        string            `json:"body,omitempty"`
	Severity    string            `json:"severity"`
	IssueID     string            `json:"issue_id,omitempty"`
	IssueStatus string            `json:"issue_status,omitempty"`
	Details     map[string]string `json:"details,omitempty"`
}

// Encode serialises the snapshot for storage in inbox_event.payload.
func (p LegacyPayload) Encode() []byte {
	b, err := json.Marshal(p)
	if err != nil {
		// The struct contains only strings and a string map; Marshal cannot
		// fail. Falling back to an empty object keeps a delivery from dying on
		// an impossible error.
		return []byte("{}")
	}
	return b
}

// StringDetails converts a producer's detail map into the legacy contract.
//
// Producers historically built map[string]any and let numbers through. This is
// the one place that flattens them, so a new producer cannot reintroduce the
// blank-list bug by writing an int.
func StringDetails(in map[string]any) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		switch t := v.(type) {
		case string:
			out[k] = t
		case bool:
			out[k] = strconv.FormatBool(t)
		case int:
			out[k] = strconv.Itoa(t)
		case int32:
			out[k] = strconv.FormatInt(int64(t), 10)
		case int64:
			out[k] = strconv.FormatInt(t, 10)
		case float32:
			out[k] = strconv.FormatFloat(float64(t), 'f', -1, 32)
		case float64:
			out[k] = strconv.FormatFloat(t, 'f', -1, 64)
		case nil:
			// Drop rather than emit "null": the legacy contract has no null
			// values, and every consumer treats a missing key as absent.
			continue
		default:
			out[k] = fmt.Sprint(t)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// LegacyRow is the legacy inbox_item JSON shape, produced from v2 rows.
//
// It carries the same json tags as handler.InboxItemResponse so the existing
// endpoints can return it unchanged. Keeping the struct here rather than in the
// handler package keeps the projection testable without a handler.
type LegacyRow struct {
	ID            string            `json:"id"`
	WorkspaceID   string            `json:"workspace_id"`
	RecipientType string            `json:"recipient_type"`
	RecipientID   string            `json:"recipient_id"`
	Type          string            `json:"type"`
	Severity      string            `json:"severity"`
	IssueID       *string           `json:"issue_id"`
	Title         string            `json:"title"`
	Body          *string           `json:"body"`
	Read          bool              `json:"read"`
	Archived      bool              `json:"archived"`
	CreatedAt     string            `json:"created_at"`
	IssueStatus   *string           `json:"issue_status"`
	ActorType     *string           `json:"actor_type"`
	ActorID       *string           `json:"actor_id"`
	Details       map[string]string `json:"details"`
}

// RenderLegacy projects one group plus its latest event into the legacy shape.
//
// Field mapping, per §8:
//
//   - id is the EVENT id, not the group id. Old clients round-trip this id back
//     into the single-item write endpoints, which resolve event -> group.
//   - read = the event is covered by the cursor AND the user has not explicitly
//     marked the group unread. Both halves matter: the cursor alone would show
//     a manually-unread group as read.
//   - archived = archived_at is set.
//   - created_at is the event's, so ordering matches what the old table did.
//
// The function performs no database access: everything it needs is in the two
// rows and the payload snapshot.
func RenderLegacy(e db.InboxEvent, g db.InboxGroup, recipientType string) LegacyRow {
	var p LegacyPayload
	if len(e.Payload) > 0 {
		_ = json.Unmarshal(e.Payload, &p)
	}

	row := LegacyRow{
		ID:            util.UUIDToString(e.ID),
		WorkspaceID:   util.UUIDToString(e.WorkspaceID),
		RecipientType: recipientType,
		RecipientID:   util.UUIDToString(g.RecipientID),
		Type:          e.Type,
		Severity:      p.Severity,
		Title:         p.Title,
		Read:          e.EventSeq <= g.ReadThroughSeq && !g.ManualUnread,
		Archived:      g.ArchivedAt.Valid,
		CreatedAt:     e.CreatedAt.Time.UTC().Format("2006-01-02T15:04:05.999999Z07:00"),
		Details:       p.Details,
	}
	if row.RecipientType == "" {
		row.RecipientType = "member"
	}
	if row.Severity == "" {
		// The old column defaulted to 'info' and every client's enum falls back
		// there; an empty string would not.
		row.Severity = "info"
	}
	if row.Details == nil {
		// The legacy column is a JSON object, never absent. Clients that index
		// into it would otherwise have to nil-check.
		row.Details = map[string]string{}
	}
	if p.Body != "" {
		body := p.Body
		row.Body = &body
	}
	if p.IssueStatus != "" {
		status := p.IssueStatus
		row.IssueStatus = &status
	}
	// issue_id comes from the group when the source is an issue, and from the
	// snapshot otherwise — a standalone group has no issue in source_id.
	switch {
	case g.SourceKind == string(SourceIssue):
		id := util.UUIDToString(g.SourceID)
		row.IssueID = &id
	case p.IssueID != "":
		id := p.IssueID
		row.IssueID = &id
	}
	if e.ActorType.Valid && e.ActorType.String != "" {
		actorType := e.ActorType.String
		row.ActorType = &actorType
	}
	if e.ActorID.Valid {
		actorID := util.UUIDToString(e.ActorID)
		row.ActorID = &actorID
	}
	return row
}
