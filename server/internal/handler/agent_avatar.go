package handler

import (
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
)

// newAgentAvatar resolves the avatar to persist for a newly created agent.
//
// An explicit value is validated through acceptAvatarURL — a create path is
// still a way to publish a storage object, so it carries the same authorization
// boundary as an update. ok=false means the error response is already written
// and the caller must abort.
//
// An omitted, empty, or whitespace-only value stores NULL rather than a
// generated placeholder: an agent without an uploaded avatar renders the icon
// of the runtime it is bound to, and that has to stay correct when the agent is
// later rebound to another runtime. Persisting a snapshot of the runtime — or a
// random glyph, as the former emoji default did — would freeze the wrong
// identity into the row.
func (h *Handler) newAgentAvatar(w http.ResponseWriter, r *http.Request, avatarURL *string) (pgtype.Text, bool) {
	if avatarURL == nil || strings.TrimSpace(*avatarURL) == "" {
		return pgtype.Text{}, true
	}
	accepted, ok := h.acceptAvatarURL(w, r, *avatarURL, "")
	if !ok {
		return pgtype.Text{}, false
	}
	return pgtype.Text{String: accepted, Valid: true}, true
}
