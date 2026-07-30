package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TimelineEntry represents a single entry in the issue timeline, which can be
// either an activity log record or a comment.
type TimelineEntry struct {
	Type string `json:"type"` // "activity" or "comment"
	ID   string `json:"id"`

	ActorType string `json:"actor_type"`
	ActorID   string `json:"actor_id"`
	CreatedAt string `json:"created_at"`

	// Activity-only fields
	Action  *string         `json:"action,omitempty"`
	Details json.RawMessage `json:"details,omitempty"`

	// Comment-only fields
	Content     *string `json:"content,omitempty"`
	ParentID    *string `json:"parent_id,omitempty"`
	UpdatedAt   *string `json:"updated_at,omitempty"`
	CommentType *string `json:"comment_type,omitempty"`
	// Set only on comments produced by a quick action run. Unforgeable: there
	// is no request field for it on the generic comment endpoint.
	QuickActionID  *string              `json:"quick_action_id,omitempty"`
	Reactions      []ReactionResponse   `json:"reactions,omitempty"`
	Attachments    []AttachmentResponse `json:"attachments,omitempty"`
	ResolvedAt     *string              `json:"resolved_at,omitempty"`
	ResolvedByType *string              `json:"resolved_by_type,omitempty"`
	ResolvedByID   *string              `json:"resolved_by_id,omitempty"`
	SourceTaskID   *string              `json:"source_task_id,omitempty"`
}

// timelineHardCap bounds the per-issue timeline payload. Sized as a defensive
// safety net, not a UX page window: see commentHardCap in comment.go for the
// data-shape rationale (#1929).
const timelineHardCap = 2000

// timelineProbeLimit reads one row past the cap so "we hit the cap" can be
// distinguished from "the issue happens to have exactly timelineHardCap rows".
// Without the probe row an issue sitting exactly on the boundary would be
// reported as truncated and would drag the other list's window down with it.
const timelineProbeLimit = timelineHardCap + 1

// Truncation is signalled with response headers rather than body fields because
// the unpaginated response is a bare JSON array (TimelineEntriesSchema =
// z.array(TimelineEntrySchema) in packages/core/api/schemas.ts) with nowhere to
// put a flag. Headers are additive: existing clients keep validating, and a
// future "load earlier" affordance has the floor it needs to resume from.
//
// Exported so the CORS layer can reference the same identifiers
// (corsExposedHeaders in server/cmd/server/router.go). A custom response header
// is invisible to browser JS unless it is explicitly exposed, so a rename here
// that did not reach the CORS list would silently switch the signal back off.
const (
	HeaderTimelineTruncated  = "X-Timeline-Truncated"
	HeaderTimelineWindowFrom = "X-Timeline-Window-From"
)

// timelineKey is the (created_at, id) tuple the timeline is ordered by. It
// mirrors the keyset tuple in ListCommentsForIssue / ListActivitiesForIssue so a
// window floor computed here means exactly what it means in SQL. Comparisons use
// the real timestamp and the raw UUID bytes (Postgres orders uuid bytewise)
// rather than the formatted strings on TimelineEntry, which are second-precision
// RFC3339 and would collapse sub-second ordering.
type timelineKey struct {
	At time.Time
	ID pgtype.UUID
}

// before reports whether k sorts strictly before other in ascending order.
func (k timelineKey) before(other timelineKey) bool {
	if !k.At.Equal(other.At) {
		return k.At.Before(other.At)
	}
	return bytes.Compare(k.ID.Bytes[:], other.ID.Bytes[:]) < 0
}

func commentKey(c db.Comment) timelineKey {
	return timelineKey{At: c.CreatedAt.Time, ID: c.ID}
}

func activityKey(a db.ActivityLog) timelineKey {
	return timelineKey{At: a.CreatedAt.Time, ID: a.ID}
}

// takeNewest trims a timelineProbeLimit read down to timelineHardCap and returns
// the window floor when the probe row proved older rows exist. rows must be
// ascending; the floor is the oldest row actually kept.
func takeNewest[T any](rows []T, key func(T) timelineKey) ([]T, *timelineKey) {
	if len(rows) <= timelineHardCap {
		return rows, nil
	}
	kept := rows[len(rows)-timelineHardCap:]
	floor := key(kept[0])
	return kept, &floor
}

// clampFrom drops ascending rows older than floor.
func clampFrom[T any](rows []T, floor timelineKey, key func(T) timelineKey) []T {
	for i, row := range rows {
		if !key(row).before(floor) {
			return rows[i:]
		}
	}
	return nil
}

// newerFloor returns whichever floor is later, treating nil as "no floor".
func newerFloor(a, b *timelineKey) *timelineKey {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case a.before(*b):
		return b
	default:
		return a
	}
}

// timelinePaginatedResponse mirrors the wrapper shape produced by the prior
// cursor-paginated ListTimeline (#2128). It is preserved as a backward-compat
// surface for installed Desktop builds and stale Web bundles between #2128 and
// #1929 that send `?limit=`/`?before=`/`?after=`/`?around=` and parse the
// response with the old TimelinePageSchema (entries + cursors). Cursors are
// always nil and `has_more_after` is always false: the new server returns the
// whole timeline in one shot. `has_more_before` is now truthful — it reports the
// hard-cap clamp — instead of being hardcoded false.
type timelinePaginatedResponse struct {
	Entries       []TimelineEntry `json:"entries"`
	NextCursor    *string         `json:"next_cursor"`
	PrevCursor    *string         `json:"prev_cursor"`
	HasMoreBefore bool            `json:"has_more_before"`
	HasMoreAfter  bool            `json:"has_more_after"`
	TargetIndex   *int            `json:"target_index,omitempty"`
}

// ListTimeline returns the full issue timeline (comments + activities merged).
// Two response shapes coexist for boundary compatibility (#1929):
//
//   - No pagination params → flat ASC `TimelineEntry[]`. Matches the legacy
//     desktop contract (Multica.app ≤ v0.2.25) and the new client.
//   - Any of `limit` / `before` / `after` / `around` present → wrapped object
//     with DESC entries + null cursors + has_more_after=false. Matches what a
//     stale v0.2.26+ build expects when it parses the response with
//     TimelinePageSchema; cursor-walking is now a no-op so the client just
//     sees a single full page.
//
// Both shapes carry the same set of entries — paging and ordering differ.
// Time-based pagination was removed because it split reply threads at page
// boundaries, and at observed data sizes (p99 ~30 comments per issue) the
// cursor machinery was pure overhead.
//
// When the hard cap fires the timeline is a window of the NEWEST entries, and
// the response says so via the X-Timeline-* headers (MUL-5492).
func (h *Handler) ListTimeline(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, id)
	if !ok {
		return
	}
	ctx := r.Context()

	comments, err := h.Queries.ListCommentsForIssue(ctx, db.ListCommentsForIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Limit:       timelineProbeLimit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list comments")
		return
	}
	activities, err := h.Queries.ListActivitiesForIssue(ctx, db.ListActivitiesForIssueParams{
		IssueID: issue.ID,
		Limit:   timelineProbeLimit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list activities")
		return
	}

	q := r.URL.Query()
	wantWrapped := q.Get("limit") != "" || q.Get("before") != "" ||
		q.Get("after") != "" || q.Get("around") != ""

	// Each query independently returned the newest timelineHardCap rows of its
	// own table, so each has its own window floor. Merging two windows with
	// different floors yields a timeline that looks continuous but, below the
	// higher floor, contains only one of the two kinds — e.g. comments with no
	// interleaved activity at all. That is strictly worse than a timeline that
	// visibly stops, because nothing about it looks wrong.
	//
	// Activity is machine-paced and comments are human-paced, so in practice
	// activity hits its cap first and it is the activity floor that wins. Clamp
	// both lists to the newest floor so the window returned is a contiguous,
	// correctly interleaved slice of the timeline with no partial region.
	comments, commentFloor := takeNewest(comments, commentKey)
	activities, activityFloor := takeNewest(activities, activityKey)
	floor := newerFloor(commentFloor, activityFloor)
	if floor != nil {
		comments = clampFrom(comments, *floor, commentKey)
		activities = clampFrom(activities, *floor, activityKey)

		w.Header().Set(HeaderTimelineTruncated, "true")
		// Formatted with the same helper as TimelineEntry.CreatedAt so a client
		// can compare the floor against entry timestamps directly, without
		// having to normalize two different offset representations.
		w.Header().Set(HeaderTimelineWindowFrom, timestampToString(pgtype.Timestamptz{Time: floor.At, Valid: true}))
	}

	if wantWrapped {
		entries := h.mergeTimeline(r, comments, activities, false)
		if entries == nil {
			entries = []TimelineEntry{}
		}
		resp := timelinePaginatedResponse{
			Entries:       entries,
			HasMoreBefore: floor != nil,
		}
		// `around=<id>`: locate the anchor in the DESC slice so the legacy
		// client can scroll-to-highlight without a follow-up request.
		if anchor := q.Get("around"); anchor != "" {
			for i, e := range entries {
				if e.ID == anchor {
					idx := i
					resp.TargetIndex = &idx
					break
				}
			}
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	entries := h.mergeTimeline(r, comments, activities, true)
	if entries == nil {
		entries = []TimelineEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// mergeTimeline merges comments and activities and returns them sorted by
// (created_at, id). When ascending=true, oldest first (the new flat-array
// contract); otherwise newest first (the wrapped legacy contract).
func (h *Handler) mergeTimeline(r *http.Request, comments []db.Comment, activities []db.ActivityLog, ascending bool) []TimelineEntry {
	out := make([]TimelineEntry, 0, len(comments)+len(activities))
	out = append(out, h.commentsToEntries(r, comments)...)
	for _, a := range activities {
		out = append(out, activityToEntry(a))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			if ascending {
				return out[i].CreatedAt < out[j].CreatedAt
			}
			return out[i].CreatedAt > out[j].CreatedAt
		}
		if ascending {
			return out[i].ID < out[j].ID
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// commentsToEntries fetches reactions + attachments for the given comments in
// one batch each and returns enriched TimelineEntry slices preserving order.
func (h *Handler) commentsToEntries(r *http.Request, comments []db.Comment) []TimelineEntry {
	if len(comments) == 0 {
		return nil
	}
	ids := make([]pgtype.UUID, len(comments))
	for i, c := range comments {
		ids[i] = c.ID
	}
	reactions := h.groupReactions(r, ids)
	attachments := h.groupAttachments(r, ids)

	out := make([]TimelineEntry, len(comments))
	for i, c := range comments {
		content := c.Content
		commentType := c.Type
		updatedAt := timestampToString(c.UpdatedAt)
		cid := uuidToString(c.ID)
		out[i] = TimelineEntry{
			Type:           "comment",
			ID:             cid,
			ActorType:      c.AuthorType,
			ActorID:        uuidToString(c.AuthorID),
			Content:        &content,
			CommentType:    &commentType,
			QuickActionID:  uuidToPtr(c.QuickActionID),
			ParentID:       uuidToPtr(c.ParentID),
			CreatedAt:      timestampToString(c.CreatedAt),
			UpdatedAt:      &updatedAt,
			Reactions:      reactions[cid],
			Attachments:    attachments[cid],
			ResolvedAt:     timestampToPtr(c.ResolvedAt),
			ResolvedByType: textToPtr(c.ResolvedByType),
			ResolvedByID:   uuidToPtr(c.ResolvedByID),
			SourceTaskID:   uuidToPtr(c.SourceTaskID),
		}
	}
	return out
}

func activityToEntry(a db.ActivityLog) TimelineEntry {
	action := a.Action
	actorType := ""
	if a.ActorType.Valid {
		actorType = a.ActorType.String
	}
	return TimelineEntry{
		Type:      "activity",
		ID:        uuidToString(a.ID),
		ActorType: actorType,
		ActorID:   uuidToString(a.ActorID),
		Action:    &action,
		Details:   a.Details,
		CreatedAt: timestampToString(a.CreatedAt),
	}
}

// AssigneeFrequencyEntry represents how often a user assigns to a specific target.
type AssigneeFrequencyEntry struct {
	AssigneeType string `json:"assignee_type"`
	AssigneeID   string `json:"assignee_id"`
	Frequency    int64  `json:"frequency"`
}

// GetAssigneeFrequency returns assignee usage frequency for the current user,
// combining data from assignee change activities and initial issue assignments.
func (h *Handler) GetAssigneeFrequency(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)

	// Aggregate frequency from both data sources.
	freq := map[string]int64{} // key: "type:id"

	// Source 1: assignee_changed activities by this user.
	activityCounts, err := h.Queries.CountAssigneeChangesByActor(r.Context(), db.CountAssigneeChangesByActorParams{
		WorkspaceID: parseUUID(workspaceID),
		ActorID:     parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get assignee frequency")
		return
	}
	for _, row := range activityCounts {
		aType, _ := row.AssigneeType.(string)
		aID, _ := row.AssigneeID.(string)
		if aType != "" && aID != "" {
			freq[aType+":"+aID] += row.Frequency
		}
	}

	// Source 2: issues created by this user with an assignee.
	issueCounts, err := h.Queries.CountCreatedIssueAssignees(r.Context(), db.CountCreatedIssueAssigneesParams{
		WorkspaceID: parseUUID(workspaceID),
		CreatorID:   parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get assignee frequency")
		return
	}
	for _, row := range issueCounts {
		if !row.AssigneeType.Valid || !row.AssigneeID.Valid {
			continue
		}
		key := row.AssigneeType.String + ":" + uuidToString(row.AssigneeID)
		freq[key] += row.Frequency
	}

	// Build sorted response.
	result := make([]AssigneeFrequencyEntry, 0, len(freq))
	for key, count := range freq {
		// Split "type:id" — type is always "member" or "agent" (no colons).
		var aType, aID string
		for i := 0; i < len(key); i++ {
			if key[i] == ':' {
				aType = key[:i]
				aID = key[i+1:]
				break
			}
		}
		result = append(result, AssigneeFrequencyEntry{
			AssigneeType: aType,
			AssigneeID:   aID,
			Frequency:    count,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Frequency > result[j].Frequency
	})

	writeJSON(w, http.StatusOK, result)
}
