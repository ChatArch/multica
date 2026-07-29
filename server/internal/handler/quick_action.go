package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Issue Quick Actions (MUL-5465): workspace-level presets for "who to call and
// what to say" on an existing issue.
//
// Contract highlights:
//   - Running one is NOT a new dispatch path. It renders the prompt, posts a
//     `quick_action` comment carrying the target's mention markup, and hands
//     off to triggerTasksForComment. Permission, attribution, squad routing,
//     the execution log, and pending-task coalescing are inherited from the
//     comment path rather than reimplemented — the MUL-3375 lesson about four
//     drifting copies of one trigger decision.
//   - Visibility is DERIVED from the bound agent's permission_mode, never
//     stored. A workspace action bound to a private agent is usable only by
//     that agent's owner; the settings UI says so at bind time so the author
//     learns it immediately instead of from a confused teammate months later.
//   - Prompt templating is flat substitution over a fixed whitelist. No
//     conditionals, loops, or filters — the agent already reads the whole
//     issue, so natural language is the control flow.
//   - Definitions are managed by human owner/admin members, mirroring
//     property/label definitions. Agents can propose in comments; humans
//     confirm. Otherwise an admin's agent could mass-produce them.
const (
	maxActiveQuickActionsPerWorkspace = 30
	maxQuickActionNameLen             = 32
	maxQuickActionDescriptionLen      = 200
	maxQuickActionPromptLen           = 4000
	maxQuickActionInputLabelLen       = 60
	maxQuickActionInputValueLen       = 2000
	// quickActionSidebarLimit is how many actions the issue sidebar shows
	// before the rest collapse behind "More". Scarcity here is structural: a
	// list that renders all 30 stops being a shortlist and becomes a menu
	// nobody reads.
	quickActionSidebarLimit = 5
)

// quickActionVariableRe finds every {{token}} in a prompt. Whitespace inside
// the braces is tolerated on parse so "{{ input }}" is recognized (and then
// validated / substituted) rather than silently surviving as literal text.
var quickActionVariableRe = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_.]+)\s*\}\}`)

// quickActionVariables is the closed whitelist. Keep it small on purpose: the
// agent can already read the issue, so a variable earns its place only when it
// changes what the agent ATTENDS TO, not what it knows.
var quickActionVariables = map[string]struct{}{
	"issue.title":      {},
	"issue.identifier": {},
	"issue.url":        {},
	"user.name":        {},
	"date":             {},
	"input":            {},
}

const quickActionInputVariable = "input"

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// QuickActionVisibility is the derived answer to "who can actually use this",
// computed per request from the bound target's permission model.
//
// It is presentation-only: the authority is canInvokeAgent, re-checked on every
// run. Clients must `default` any switch on it (server-driven enum).
type QuickActionVisibility string

const (
	// QuickActionVisibilityWorkspace: every workspace member may run it.
	QuickActionVisibilityWorkspace QuickActionVisibility = "workspace"
	// QuickActionVisibilityRestricted: a public_to allow-list of specific
	// members may run it.
	QuickActionVisibilityRestricted QuickActionVisibility = "restricted"
	// QuickActionVisibilityOwnerOnly: the target is private, so only its owner
	// may run it. This is the case the settings UI must call out loudly.
	QuickActionVisibilityOwnerOnly QuickActionVisibility = "owner_only"
	// QuickActionVisibilityUnavailable: the target is archived or gone. The
	// action can never run until it is rebound.
	QuickActionVisibilityUnavailable QuickActionVisibility = "unavailable"
)

type QuickActionResponse struct {
	ID               string  `json:"id"`
	WorkspaceID      string  `json:"workspace_id"`
	Name             string  `json:"name"`
	Description      string  `json:"description"`
	AssigneeType     string  `json:"assignee_type"`
	AssigneeID       string  `json:"assignee_id"`
	Prompt           string  `json:"prompt"`
	InputEnabled     bool    `json:"input_enabled"`
	InputLabel       string  `json:"input_label"`
	InputPlaceholder string  `json:"input_placeholder"`
	InputRequired    bool    `json:"input_required"`
	Position         float64 `json:"position"`
	Status           string  `json:"status"`
	LastUsedAt       *string `json:"last_used_at"`
	UseCount         int64   `json:"use_count"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`

	// Derived per request — never stored. See QuickActionVisibility.
	Visibility QuickActionVisibility `json:"visibility"`
	// CanRun is the caller's own invoke verdict. The sidebar renders only
	// can_run actions; settings renders all of them with the visibility badge
	// so an admin can audit which entries are effectively personal.
	CanRun bool `json:"can_run"`
	// TargetName / TargetAvatarURL identify the bound agent or squad. Omitted
	// when the caller cannot even see the target, so the response never
	// discloses a private agent's existence to a stranger.
	TargetName      string `json:"target_name,omitempty"`
	TargetAvatarURL string `json:"target_avatar_url,omitempty"`
}

type CreateQuickActionRequest struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	AssigneeType     string `json:"assignee_type"`
	AssigneeID       string `json:"assignee_id"`
	Prompt           string `json:"prompt"`
	InputEnabled     bool   `json:"input_enabled"`
	InputLabel       string `json:"input_label"`
	InputPlaceholder string `json:"input_placeholder"`
	InputRequired    bool   `json:"input_required"`
}

type UpdateQuickActionRequest struct {
	Name             *string  `json:"name"`
	Description      *string  `json:"description"`
	AssigneeType     *string  `json:"assignee_type"`
	AssigneeID       *string  `json:"assignee_id"`
	Prompt           *string  `json:"prompt"`
	InputEnabled     *bool    `json:"input_enabled"`
	InputLabel       *string  `json:"input_label"`
	InputPlaceholder *string  `json:"input_placeholder"`
	InputRequired    *bool    `json:"input_required"`
	Status           *string  `json:"status"`
	Position         *float64 `json:"position"`
}

type RunQuickActionRequest struct {
	Input string `json:"input"`
}

type ListQuickActionsResponse struct {
	QuickActions []QuickActionResponse `json:"quick_actions"`
	// SidebarLimit lets the client render the "top N + More" split without
	// hardcoding the number in three codebases.
	SidebarLimit int `json:"sidebar_limit"`
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

func validateQuickActionName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	if utf8.RuneCountInString(name) > maxQuickActionNameLen {
		return "", fmt.Errorf("name must be at most %d characters", maxQuickActionNameLen)
	}
	return name, nil
}

// validateQuickActionPrompt enforces the closed variable whitelist and the
// two-way {{input}} agreement.
//
// Both directions matter. An enabled input whose prompt never references
// {{input}} would silently discard whatever the user typed; a prompt
// referencing {{input}} with the input disabled would render the literal
// braces into the agent's instructions. Rejecting at write time follows the
// autopilot issue-title-template rule: a typo must never land silently.
func validateQuickActionPrompt(raw string, inputEnabled bool) (string, error) {
	prompt := strings.TrimSpace(raw)
	if prompt == "" {
		return "", fmt.Errorf("prompt is required")
	}
	if utf8.RuneCountInString(prompt) > maxQuickActionPromptLen {
		return "", fmt.Errorf("prompt must be at most %d characters", maxQuickActionPromptLen)
	}
	usesInput := false
	for _, m := range quickActionVariableRe.FindAllStringSubmatch(prompt, -1) {
		name := m[1]
		if _, ok := quickActionVariables[name]; !ok {
			return "", fmt.Errorf("unknown variable {{%s}}; supported: %s", name, strings.Join(quickActionVariableNames(), ", "))
		}
		if name == quickActionInputVariable {
			usesInput = true
		}
	}
	if inputEnabled && !usesInput {
		return "", fmt.Errorf("runtime input is enabled but the prompt does not reference {{input}}")
	}
	if !inputEnabled && usesInput {
		return "", fmt.Errorf("the prompt references {{input}} but runtime input is not enabled")
	}
	return prompt, nil
}

func quickActionVariableNames() []string {
	names := make([]string, 0, len(quickActionVariables))
	for n := range quickActionVariables {
		names = append(names, "{{"+n+"}}")
	}
	// Stable order for a deterministic error message.
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if names[j] < names[i] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	return names
}

func validateQuickActionAssignee(assigneeType, assigneeID string) error {
	if assigneeType != "agent" && assigneeType != "squad" {
		return fmt.Errorf("assignee_type must be \"agent\" or \"squad\"")
	}
	if strings.TrimSpace(assigneeID) == "" {
		return fmt.Errorf("assignee_id is required")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Target resolution + derived visibility
// ---------------------------------------------------------------------------

// quickActionTarget is the resolved execution target of an action: the agent
// that will actually run, plus the display identity the client should show.
// For a squad binding, Agent is the squad leader and Name is the SQUAD's name,
// because the squad is what the user bound and what the mention will address.
type quickActionTarget struct {
	Agent     db.Agent
	Name      string
	AvatarURL string
	// MentionType is the mention:// scheme the rendered comment must use so
	// the existing trigger path routes it exactly as a hand-typed mention.
	MentionType string
	// MentionID is the id inside the mention link: the agent id, or the SQUAD
	// id for a squad binding (the trigger path resolves the leader itself).
	MentionID string
	Found     bool
}

// resolveQuickActionTarget loads the execution target. A missing/archived
// agent, or a missing/archived squad, resolves to Found=false — the caller
// renders that as `unavailable` rather than failing the whole list.
func (h *Handler) resolveQuickActionTarget(ctx context.Context, qa db.QuickAction) quickActionTarget {
	switch qa.AssigneeType {
	case "squad":
		squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
			ID:          qa.AssigneeID,
			WorkspaceID: qa.WorkspaceID,
		})
		if err != nil || squad.ArchivedAt.Valid {
			return quickActionTarget{}
		}
		leader, err := h.Queries.GetAgent(ctx, squad.LeaderID)
		if err != nil || leader.ArchivedAt.Valid {
			return quickActionTarget{}
		}
		avatar := ""
		if squad.AvatarUrl.Valid {
			avatar = squad.AvatarUrl.String
		}
		return quickActionTarget{
			Agent:       leader,
			Name:        squad.Name,
			AvatarURL:   avatar,
			MentionType: "squad",
			MentionID:   uuidToString(squad.ID),
			Found:       true,
		}
	default:
		agent, err := h.Queries.GetAgent(ctx, qa.AssigneeID)
		if err != nil || agent.ArchivedAt.Valid || uuidToString(agent.WorkspaceID) != uuidToString(qa.WorkspaceID) {
			return quickActionTarget{}
		}
		avatar := ""
		if agent.AvatarUrl.Valid {
			avatar = agent.AvatarUrl.String
		}
		return quickActionTarget{
			Agent:       agent,
			Name:        agent.Name,
			AvatarURL:   avatar,
			MentionType: "agent",
			MentionID:   uuidToString(agent.ID),
			Found:       true,
		}
	}
}

// deriveQuickActionVisibility answers "who can use this" from the target's
// permission model. This is the whole of decision D6: the field is computed,
// never stored, so it can never drift out of sync with the agent's actual
// permission_mode after someone flips it.
func (h *Handler) deriveQuickActionVisibility(ctx context.Context, target quickActionTarget) QuickActionVisibility {
	if !target.Found {
		return QuickActionVisibilityUnavailable
	}
	if target.Agent.PermissionMode != "public_to" {
		return QuickActionVisibilityOwnerOnly
	}
	targets, err := h.Queries.ListAgentInvocationTargets(ctx, target.Agent.ID)
	if err != nil {
		// Fail closed on a lookup error: claiming "everyone" here would be a
		// visibility promise we cannot back.
		return QuickActionVisibilityOwnerOnly
	}
	for _, t := range targets {
		if t.TargetType == "workspace" {
			return QuickActionVisibilityWorkspace
		}
	}
	return QuickActionVisibilityRestricted
}

func (h *Handler) quickActionToResponse(ctx context.Context, qa db.QuickAction, actorType, actorID, originatorUserID, workspaceID string) QuickActionResponse {
	target := h.resolveQuickActionTarget(ctx, qa)
	visibility := h.deriveQuickActionVisibility(ctx, target)

	canRun := false
	if target.Found {
		canRun = h.canInvokeAgent(ctx, target.Agent, actorType, actorID, originatorUserID, workspaceID)
	}

	resp := QuickActionResponse{
		ID:               uuidToString(qa.ID),
		WorkspaceID:      uuidToString(qa.WorkspaceID),
		Name:             qa.Name,
		Description:      qa.Description,
		AssigneeType:     qa.AssigneeType,
		AssigneeID:       uuidToString(qa.AssigneeID),
		Prompt:           qa.Prompt,
		InputEnabled:     qa.InputEnabled,
		InputLabel:       qa.InputLabel,
		InputPlaceholder: qa.InputPlaceholder,
		InputRequired:    qa.InputRequired,
		Position:         qa.Position,
		Status:           qa.Status,
		LastUsedAt:       timestampToPtr(qa.LastUsedAt),
		UseCount:         qa.UseCount,
		CreatedAt:        timestampToString(qa.CreatedAt),
		UpdatedAt:        timestampToString(qa.UpdatedAt),
		Visibility:       visibility,
		CanRun:           canRun,
	}
	// Only disclose the target's identity to callers who can already see it.
	// A stranger learning "this button points at Lambda" would leak the
	// existence of a private agent the invoke gate is meant to hide.
	if target.Found && h.canAccessPrivateAgent(ctx, target.Agent, actorType, actorID, workspaceID) {
		resp.TargetName = target.Name
		resp.TargetAvatarURL = target.AvatarURL
	}
	return resp
}

// ---------------------------------------------------------------------------
// Prompt rendering
// ---------------------------------------------------------------------------

// renderQuickActionPrompt performs the flat substitution. Rendering lives on
// the server for three reasons: three clients would otherwise each grow their
// own interpolation and drift; a client-assembled body would let anyone submit
// arbitrary text under a trusted action's name, destroying the audit value; and
// only the server can stamp quick_action_id for usage analytics.
func renderQuickActionPrompt(prompt string, issue db.Issue, identifier, userName, publicURL, input string) string {
	values := map[string]string{
		"issue.title":            issue.Title,
		"issue.identifier":       identifier,
		"issue.url":              quickActionIssueURL(publicURL, uuidToString(issue.ID)),
		"user.name":              userName,
		"date":                   time.Now().UTC().Format("2006-01-02"),
		quickActionInputVariable: input,
	}
	return quickActionVariableRe.ReplaceAllStringFunc(prompt, func(match string) string {
		groups := quickActionVariableRe.FindStringSubmatch(match)
		if len(groups) < 2 {
			return match
		}
		if v, ok := values[groups[1]]; ok {
			return v
		}
		// Unreachable for stored prompts (write-time validation rejects
		// unknown variables); leave the token untouched rather than blanking
		// text if an older row ever predates the whitelist.
		return match
	})
}

func quickActionIssueURL(publicURL, issueID string) string {
	if publicURL == "" {
		return ""
	}
	return strings.TrimRight(publicURL, "/") + "/issues/" + issueID
}

// ---------------------------------------------------------------------------
// Permission gate
// ---------------------------------------------------------------------------

// requireQuickActionAdmin gates definition writes to human owner/admin members,
// mirroring requirePropertyAdmin. Agents are rejected before the role check:
// an agent inherits its runtime owner's credentials, so without this an admin's
// agent could mass-produce actions that then appear in everyone's sidebar.
func (h *Handler) requireQuickActionAdmin(w http.ResponseWriter, r *http.Request) (workspaceID, userID string, ok bool) {
	workspaceID = h.resolveWorkspaceID(r)
	userID, ok = requireUserID(w, r)
	if !ok {
		return "", "", false
	}
	if actorType, _ := h.resolveActor(r, userID, workspaceID); actorType == "agent" {
		writeError(w, http.StatusForbidden, "agents cannot manage quick actions")
		return "", "", false
	}
	if _, roleOK := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !roleOK {
		return "", "", false
	}
	return workspaceID, userID, true
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func (h *Handler) ListQuickActions(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	originatorUserID := h.invokeOriginatorFromRequest(r, actorType, actorID)

	includeArchived := r.URL.Query().Get("include_archived") == "true"
	rows, err := h.Queries.ListQuickActions(r.Context(), db.ListQuickActionsParams{
		WorkspaceID:     wsUUID,
		IncludeArchived: includeArchived,
	})
	if err != nil {
		slog.Warn("list quick actions failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to list quick actions")
		return
	}

	// runnable_only is the issue sidebar's mode: it must never render a button
	// the caller cannot actually press. Settings omits it so an admin sees the
	// whole catalog, each row carrying its derived visibility badge.
	runnableOnly := r.URL.Query().Get("runnable_only") == "true"

	out := make([]QuickActionResponse, 0, len(rows))
	for _, qa := range rows {
		resp := h.quickActionToResponse(r.Context(), qa, actorType, actorID, originatorUserID, workspaceID)
		if runnableOnly && !resp.CanRun {
			continue
		}
		out = append(out, resp)
	}
	writeJSON(w, http.StatusOK, ListQuickActionsResponse{QuickActions: out, SidebarLimit: quickActionSidebarLimit})
}

func (h *Handler) CreateQuickAction(w http.ResponseWriter, r *http.Request) {
	workspaceID, userID, ok := h.requireQuickActionAdmin(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	var req CreateQuickActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name, err := validateQuickActionName(req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateQuickActionAssignee(req.AssigneeType, req.AssigneeID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	assigneeUUID, ok := parseUUIDOrBadRequest(w, req.AssigneeID, "assignee_id")
	if !ok {
		return
	}
	prompt, err := validateQuickActionPrompt(req.Prompt, req.InputEnabled)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	description := strings.TrimSpace(req.Description)
	if utf8.RuneCountInString(description) > maxQuickActionDescriptionLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("description must be at most %d characters", maxQuickActionDescriptionLen))
		return
	}
	inputLabel := strings.TrimSpace(req.InputLabel)
	if utf8.RuneCountInString(inputLabel) > maxQuickActionInputLabelLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("input_label must be at most %d characters", maxQuickActionInputLabelLen))
		return
	}
	inputPlaceholder := strings.TrimSpace(req.InputPlaceholder)
	if utf8.RuneCountInString(inputPlaceholder) > maxQuickActionInputLabelLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("input_placeholder must be at most %d characters", maxQuickActionInputLabelLen))
		return
	}

	// Verify the target exists in THIS workspace before storing a dangling
	// binding. A cross-workspace id would otherwise persist and render as
	// permanently `unavailable`.
	if !h.quickActionAssigneeExists(r.Context(), req.AssigneeType, assigneeUUID, wsUUID) {
		writeError(w, http.StatusBadRequest, "assignee not found in this workspace")
		return
	}

	count, err := h.Queries.CountActiveQuickActions(r.Context(), wsUUID)
	if err == nil && count >= maxActiveQuickActionsPerWorkspace {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("a workspace can have at most %d active quick actions; archive one first", maxActiveQuickActionsPerWorkspace))
		return
	}

	qa, err := h.Queries.CreateQuickAction(r.Context(), db.CreateQuickActionParams{
		WorkspaceID:      wsUUID,
		Name:             name,
		Description:      description,
		AssigneeType:     req.AssigneeType,
		AssigneeID:       assigneeUUID,
		Prompt:           prompt,
		InputEnabled:     req.InputEnabled,
		InputLabel:       inputLabel,
		InputPlaceholder: inputPlaceholder,
		InputRequired:    req.InputRequired && req.InputEnabled,
		CreatedByType:    "member",
		CreatedByID:      parseUUID(userID),
	})
	if err != nil {
		slog.Warn("create quick action failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to create quick action")
		return
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	writeJSON(w, http.StatusCreated, h.quickActionToResponse(r.Context(), qa, actorType, actorID, h.invokeOriginatorFromRequest(r, actorType, actorID), workspaceID))
}

func (h *Handler) UpdateQuickAction(w http.ResponseWriter, r *http.Request) {
	workspaceID, userID, ok := h.requireQuickActionAdmin(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	idUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "quick action id")
	if !ok {
		return
	}
	existing, err := h.Queries.GetQuickAction(r.Context(), db.GetQuickActionParams{ID: idUUID, WorkspaceID: wsUUID})
	if err != nil {
		writeError(w, http.StatusNotFound, "quick action not found")
		return
	}

	var req UpdateQuickActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	params := db.UpdateQuickActionParams{ID: idUUID, WorkspaceID: wsUUID}

	if req.Name != nil {
		name, err := validateQuickActionName(*req.Name)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		params.Name = pgtype.Text{String: name, Valid: true}
	}
	if req.Description != nil {
		description := strings.TrimSpace(*req.Description)
		if utf8.RuneCountInString(description) > maxQuickActionDescriptionLen {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("description must be at most %d characters", maxQuickActionDescriptionLen))
			return
		}
		params.Description = pgtype.Text{String: description, Valid: true}
	}

	// assignee_type and assignee_id move together so a type swap can never
	// land with a mismatched id (the same rule autopilot enforces).
	if req.AssigneeType != nil || req.AssigneeID != nil {
		newType := existing.AssigneeType
		if req.AssigneeType != nil {
			newType = *req.AssigneeType
		}
		newID := uuidToString(existing.AssigneeID)
		if req.AssigneeID != nil {
			newID = *req.AssigneeID
		}
		if err := validateQuickActionAssignee(newType, newID); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		newUUID, ok := parseUUIDOrBadRequest(w, newID, "assignee_id")
		if !ok {
			return
		}
		if !h.quickActionAssigneeExists(r.Context(), newType, newUUID, wsUUID) {
			writeError(w, http.StatusBadRequest, "assignee not found in this workspace")
			return
		}
		params.AssigneeType = pgtype.Text{String: newType, Valid: true}
		params.AssigneeID = newUUID
	}

	// The prompt and the input toggle are validated as a pair even when only
	// one of them is being patched — otherwise a lone `input_enabled: true`
	// could land against a prompt with no {{input}}.
	if req.Prompt != nil || req.InputEnabled != nil {
		newPrompt := existing.Prompt
		if req.Prompt != nil {
			newPrompt = *req.Prompt
		}
		newEnabled := existing.InputEnabled
		if req.InputEnabled != nil {
			newEnabled = *req.InputEnabled
		}
		prompt, err := validateQuickActionPrompt(newPrompt, newEnabled)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		params.Prompt = pgtype.Text{String: prompt, Valid: true}
		params.InputEnabled = pgtype.Bool{Bool: newEnabled, Valid: true}
		if !newEnabled {
			// Disabling the input clears its required flag so a later re-enable
			// starts from a clean, non-blocking state.
			params.InputRequired = pgtype.Bool{Bool: false, Valid: true}
		}
	}
	if req.InputLabel != nil {
		label := strings.TrimSpace(*req.InputLabel)
		if utf8.RuneCountInString(label) > maxQuickActionInputLabelLen {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("input_label must be at most %d characters", maxQuickActionInputLabelLen))
			return
		}
		params.InputLabel = pgtype.Text{String: label, Valid: true}
	}
	if req.InputPlaceholder != nil {
		placeholder := strings.TrimSpace(*req.InputPlaceholder)
		if utf8.RuneCountInString(placeholder) > maxQuickActionInputLabelLen {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("input_placeholder must be at most %d characters", maxQuickActionInputLabelLen))
			return
		}
		params.InputPlaceholder = pgtype.Text{String: placeholder, Valid: true}
	}
	if req.InputRequired != nil {
		params.InputRequired = pgtype.Bool{Bool: *req.InputRequired, Valid: true}
	}
	if req.Status != nil {
		if *req.Status != "active" && *req.Status != "archived" {
			writeError(w, http.StatusBadRequest, "status must be \"active\" or \"archived\"")
			return
		}
		if *req.Status == "active" && existing.Status != "active" {
			count, err := h.Queries.CountActiveQuickActions(r.Context(), wsUUID)
			if err == nil && count >= maxActiveQuickActionsPerWorkspace {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("a workspace can have at most %d active quick actions", maxActiveQuickActionsPerWorkspace))
				return
			}
		}
		params.Status = pgtype.Text{String: *req.Status, Valid: true}
	}
	if req.Position != nil {
		params.Position = pgtype.Float8{Float64: *req.Position, Valid: true}
	}

	qa, err := h.Queries.UpdateQuickAction(r.Context(), params)
	if err != nil {
		slog.Warn("update quick action failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to update quick action")
		return
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	writeJSON(w, http.StatusOK, h.quickActionToResponse(r.Context(), qa, actorType, actorID, h.invokeOriginatorFromRequest(r, actorType, actorID), workspaceID))
}

func (h *Handler) DeleteQuickAction(w http.ResponseWriter, r *http.Request) {
	workspaceID, _, ok := h.requireQuickActionAdmin(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	idUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "quick action id")
	if !ok {
		return
	}
	if err := h.Queries.DeleteQuickAction(r.Context(), db.DeleteQuickActionParams{ID: idUUID, WorkspaceID: wsUUID}); err != nil {
		slog.Warn("delete quick action failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to delete quick action")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) quickActionAssigneeExists(ctx context.Context, assigneeType string, id, workspaceID pgtype.UUID) bool {
	if assigneeType == "squad" {
		squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{ID: id, WorkspaceID: workspaceID})
		return err == nil && !squad.ArchivedAt.Valid
	}
	agent, err := h.Queries.GetAgent(ctx, id)
	return err == nil && !agent.ArchivedAt.Valid && uuidToString(agent.WorkspaceID) == uuidToString(workspaceID)
}

// RenderQuickAction returns what the action WOULD post, without posting it.
//
// This backs the composer hand-off (⌥-click and the `/` slash command): the
// user gets the fully rendered text in the comment box to edit before sending.
// Rendering stays on the server even for this read-only path so there is
// exactly one interpolation implementation — a client-side copy would drift
// from the run path and quietly send something different from the preview.
func (h *Handler) RenderQuickAction(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := uuidToString(issue.WorkspaceID)
	idUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "quickActionId"), "quick action id")
	if !ok {
		return
	}
	qa, err := h.Queries.GetQuickAction(r.Context(), db.GetQuickActionParams{ID: idUUID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		writeError(w, http.StatusNotFound, "quick action not found")
		return
	}

	var req RunQuickActionRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	input := strings.TrimSpace(req.Input)
	if !qa.InputEnabled {
		input = ""
	}

	target := h.resolveQuickActionTarget(r.Context(), qa)
	if !target.Found {
		h.writeDispatchBlocked(w, http.StatusConflict, ReasonTargetUnavailable)
		return
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	// Same gate as the run path: a preview that reveals a private agent's name
	// would leak exactly what the invoke gate exists to hide.
	if !h.canInvokeAgent(r.Context(), target.Agent, actorType, actorID, h.invokeOriginatorFromRequest(r, actorType, actorID), workspaceID) {
		h.writeDispatchBlocked(w, http.StatusForbidden, ReasonInvocationNotAllowed)
		return
	}

	identifier := ""
	if ws, err := h.Queries.GetWorkspace(r.Context(), issue.WorkspaceID); err == nil {
		identifier = fmt.Sprintf("%s-%d", ws.IssuePrefix, issue.Number)
	}
	userName := ""
	if u, err := h.Queries.GetUser(r.Context(), parseUUID(userID)); err == nil {
		userName = u.Name
	}

	rendered := renderQuickActionPrompt(qa.Prompt, issue, identifier, userName, h.cfg.PublicURL, input)
	writeJSON(w, http.StatusOK, map[string]string{
		"content": fmt.Sprintf("[@%s](mention://%s/%s)\n\n%s", target.Name, target.MentionType, target.MentionID, rendered),
	})
}

// RunQuickAction renders the action and posts it as a quick_action comment,
// then hands off to the standard comment trigger path.
//
// The response is a CommentResponse carrying trigger_outcomes, identical in
// shape to POST /comments — so the client reuses one result handler and gets
// `coalesced` (merged into the target's pending task), `deferred` (runtime
// offline), and `blocked` for free, instead of a bespoke success/failure
// vocabulary that would drift from the comment path's.
func (h *Handler) RunQuickAction(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := uuidToString(issue.WorkspaceID)
	idUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "quickActionId"), "quick action id")
	if !ok {
		return
	}
	qa, err := h.Queries.GetQuickAction(r.Context(), db.GetQuickActionParams{ID: idUUID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		writeError(w, http.StatusNotFound, "quick action not found")
		return
	}
	if qa.Status != "active" {
		writeError(w, http.StatusBadRequest, "quick action is archived")
		return
	}

	var req RunQuickActionRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	input := sanitizeNullBytes(strings.TrimSpace(req.Input))
	if !qa.InputEnabled {
		// Ignore a stray input on an action that declares none rather than
		// erroring: the rendered prompt has no {{input}} slot to put it in, so
		// accepting it silently would be the real bug.
		input = ""
	}
	if qa.InputEnabled && qa.InputRequired && input == "" {
		writeError(w, http.StatusBadRequest, "this quick action requires an input")
		return
	}
	if utf8.RuneCountInString(input) > maxQuickActionInputValueLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("input must be at most %d characters", maxQuickActionInputValueLen))
		return
	}

	target := h.resolveQuickActionTarget(r.Context(), qa)
	if !target.Found {
		h.writeDispatchBlocked(w, http.StatusConflict, ReasonTargetUnavailable)
		return
	}

	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	originatorUserID := h.invokeOriginatorFromRequest(r, actorType, actorID)
	// Re-gate on every run. The list endpoint already filters, but a stale
	// client, a direct API call, or a permission flip between render and click
	// must all land here — issue visibility never implies the right to trigger
	// someone's private agent.
	if !h.canInvokeAgent(r.Context(), target.Agent, actorType, actorID, originatorUserID, workspaceID) {
		h.writeDispatchBlocked(w, http.StatusForbidden, ReasonInvocationNotAllowed)
		return
	}

	identifier := ""
	if ws, err := h.Queries.GetWorkspace(r.Context(), issue.WorkspaceID); err == nil {
		identifier = fmt.Sprintf("%s-%d", ws.IssuePrefix, issue.Number)
	}
	userName := ""
	if u, err := h.Queries.GetUser(r.Context(), parseUUID(userID)); err == nil {
		userName = u.Name
	}

	rendered := renderQuickActionPrompt(qa.Prompt, issue, identifier, userName, h.cfg.PublicURL, input)
	// The mention line is what actually triggers the run — it is parsed by the
	// same ParseMentions the composer's output goes through, so a quick action
	// is indistinguishable from a hand-typed @mention downstream.
	body := fmt.Sprintf("[@%s](mention://%s/%s)\n\n%s", target.Name, target.MentionType, target.MentionID, rendered)
	body = sanitizeNullBytes(body)

	comment, err := h.Queries.CreateComment(r.Context(), db.CreateCommentParams{
		IssueID:       issue.ID,
		WorkspaceID:   issue.WorkspaceID,
		AuthorType:    actorType,
		AuthorID:      parseUUID(actorID),
		Content:       body,
		Type:          "quick_action",
		QuickActionID: qa.ID,
	})
	if err != nil {
		slog.Warn("quick action comment create failed", append(logger.RequestAttrs(r), "error", err, "quick_action_id", uuidToString(qa.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to run quick action")
		return
	}

	resp := commentToResponse(comment, nil, nil)
	h.publish(protocol.EventCommentCreated, workspaceID, actorType, actorID, map[string]any{
		"comment":             resp,
		"issue_title":         issue.Title,
		"issue_assignee_type": textToPtr(issue.AssigneeType),
		"issue_assignee_id":   uuidToPtr(issue.AssigneeID),
		"issue_status":        issue.Status,
	})

	delegationAuthority := h.autopilotDelegationAuthorityFromRequest(r, issue, actorType, actorID)
	resp.TriggerOutcomes = h.triggerTasksForComment(r.Context(), issue, comment, nil, actorType, actorID, originatorUserID, delegationAuthority, nil)

	// Usage telemetry is best-effort and deliberately outside the run's
	// success path: a failed counter must never cost the user the run.
	if err := h.Queries.TouchQuickActionUsage(r.Context(), db.TouchQuickActionUsageParams{ID: qa.ID, WorkspaceID: issue.WorkspaceID}); err != nil {
		slog.Debug("quick action usage touch failed", "quick_action_id", uuidToString(qa.ID), "error", err)
	}

	slog.Info("quick action run", append(logger.RequestAttrs(r),
		"quick_action_id", uuidToString(qa.ID),
		"issue_id", uuidToString(issue.ID),
		"comment_id", uuidToString(comment.ID),
	)...)
	writeJSON(w, http.StatusCreated, resp)
}
