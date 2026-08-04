package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/service/inboxv2"
)

// inboxProjection returns the v2-backed implementation of the legacy inbox
// endpoints, or nil when the read switch is off.
//
// nil means "use inbox_item", so the legacy path below each call site stays
// exactly as it is today. Returning nil rather than branching inside every
// handler keeps the switch in one place and keeps the old code readable as the
// thing it still is, rather than as one arm of a permanent if/else.
func (h *Handler) inboxProjection() *inboxv2.LegacyProjection {
	if !inboxv2.ReadEnabled() {
		return nil
	}
	return inboxv2.NewLegacyProjection(h.Queries)
}

// writeProjectedInboxError maps a projection error onto the status codes the
// legacy endpoints already return. A group that is not the caller's is
// indistinguishable from one that does not exist, so an id cannot be probed.
func writeProjectedInboxError(w http.ResponseWriter, err error, message string) {
	if errors.Is(err, inboxv2.ErrNotFound) {
		writeError(w, http.StatusNotFound, "inbox item not found")
		return
	}
	writeError(w, http.StatusInternalServerError, message)
}

// projectedInboxRows adapts the projection output to the legacy response type.
// The two structs carry identical json tags; this exists so the handler package
// keeps owning its own response type rather than leaking the service one into
// the API surface.
func projectedInboxRows(rows []inboxv2.LegacyRow) []InboxItemResponse {
	out := make([]InboxItemResponse, len(rows))
	for i, r := range rows {
		out[i] = projectedInboxRow(r)
	}
	return out
}

func projectedInboxRow(r inboxv2.LegacyRow) InboxItemResponse {
	details, _ := json.Marshal(r.Details)
	return InboxItemResponse{
		ID:            r.ID,
		WorkspaceID:   r.WorkspaceID,
		RecipientType: r.RecipientType,
		RecipientID:   r.RecipientID,
		Type:          r.Type,
		Severity:      r.Severity,
		IssueID:       r.IssueID,
		Title:         r.Title,
		Body:          r.Body,
		Read:          r.Read,
		Archived:      r.Archived,
		CreatedAt:     r.CreatedAt,
		IssueStatus:   r.IssueStatus,
		ActorType:     r.ActorType,
		ActorID:       r.ActorID,
		Details:       details,
	}
}

// inboxNow is the clock the projection reads snooze expiry against. A single
// timestamp per request so a list and its count cannot straddle an expiry.
func inboxNow() time.Time { return time.Now().UTC() }

// projectedInboxWrite runs one of the four single-item write endpoints against
// the projection.
//
// All four differ only in which group mutation they call and which legacy
// websocket event they emit afterwards, so they share this body rather than
// repeating the id parsing, the 404 mapping and the publish four times.
func (h *Handler) projectedInboxWrite(
	w http.ResponseWriter,
	r *http.Request,
	id string,
	mutate func(ctx context.Context, eventID, workspaceID, recipientID pgtype.UUID, now time.Time) (inboxv2.LegacyRow, error),
	event string,
	errMessage string,
) {
	eventID, ok := parseUUIDOrBadRequest(w, id, "inbox item id")
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	userID := requestUserID(r)

	row, err := mutate(r.Context(), eventID, wsUUID, parseUUID(userID), inboxNow())
	if err != nil {
		writeProjectedInboxError(w, err, errMessage)
		return
	}

	// The legacy websocket event keeps its exact shape: old clients key their
	// cache invalidation on item_id, and the projected row's id is the event id
	// they already hold.
	h.publish(event, workspaceID, "member", userID, map[string]any{
		"item_id":      row.ID,
		"recipient_id": row.RecipientID,
	})
	writeJSON(w, http.StatusOK, projectedInboxRow(row))
}

// projectedOrLegacyBatch picks the projection or the legacy statement for the
// four batch endpoints, both of which return "how many were affected".
//
// The unit changes from rows to groups when the projection is on. Both clients
// use the number only to decide whether to invalidate, so the change is
// invisible to them — but it is a real semantic difference and belongs in one
// commented place rather than four.
func (h *Handler) projectedOrLegacyBatch(
	r *http.Request,
	workspaceID, recipientID pgtype.UUID,
	projected func(*inboxv2.LegacyProjection) (int64, error),
	legacy func() (int64, error),
) (int64, error) {
	if p := h.inboxProjection(); p != nil {
		return projected(p)
	}
	return legacy()
}
