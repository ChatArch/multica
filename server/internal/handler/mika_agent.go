package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Mika's product-defined identity. These are server constants on purpose: the
// public CreateAgent API accepts neither `kind` nor `system_key`, so a client
// cannot mint an agent that would receive the system instruction layer or pass
// the onboarding gate. Letting it would both forge a "Mika" and break the
// one-per-workspace invariant.
const (
	mikaAgentMaxConcurrency = 3
	mikaAgentVisibility     = "workspace"
	mikaAgentPermissionMode = "public_to"
	mikaAgentAvatarURL      = "data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 128 128'%3E%3Crect width='128' height='128' rx='30' fill='%2317191F'/%3E%3Cg fill='%23FFFFFF'%3E%3Cpath d='M64 22c4 22 10 30 28 42-18 12-24 20-28 42-4-22-10-30-28-42 18-12 24-20 28-42Z'/%3E%3Ccircle cx='96' cy='31' r='7' fill='%238A8F98'/%3E%3C/g%3E%3C/svg%3E"
)

// mikaAgentDescriptions is user-facing copy, so it is localized. Unlike the
// instructions it is stored on the row: description is an owner-editable field
// and the product does not reclaim it after creation.
var mikaAgentDescriptions = map[string]string{
	"en": "Your workspace Chief of Staff. Mika turns goals into issues, coordinates agents, and helps build reusable workflows.",
	"zh": "你的工作区 Chief of Staff。Mika 会把目标转化为 issue、协调智能体，并帮你建立可复用的工作流。",
	"ko": "워크스페이스의 Chief of Staff입니다. Mika가 목표를 이슈로 구체화하고 에이전트를 조율하며 재사용 가능한 워크플로 구성을 돕습니다.",
	"ja": "ワークスペースの Chief of Staff。Mika は目標をイシューに落とし込み、エージェントを調整し、再利用できるワークフローづくりを支援します。",
}

type createMikaAgentRequest struct {
	RuntimeID string `json:"runtime_id"`
	Language  string `json:"language"`
}

// CreateMikaAgent provisions the workspace's built-in Chief of Staff on the
// given runtime, or returns the existing one.
//
// Idempotency is by system_key inside the workspace, not by name: names are
// owner-editable, and migration 172's unique index covers
// (workspace_id, owner_id, runtime_id, system_key), which would still admit a
// second Mika on a different runtime or under a different owner. The lookup
// below is the invariant that actually holds "one Mika per workspace".
func (h *Handler) CreateMikaAgent(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())

	var req createMikaAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	description, ok := mikaAgentDescriptions[req.Language]
	if !ok {
		writeError(w, http.StatusBadRequest, "language must be en, zh, ko, or ja")
		return
	}
	runtimeID, ok := parseUUIDOrBadRequest(w, req.RuntimeID, "runtime_id")
	if !ok {
		return
	}
	wsUUID := parseUUID(workspaceID)

	// Already provisioned: hand back the same agent so a retry, a second tab,
	// or a re-run of onboarding cannot produce a duplicate.
	if existing, err := h.Queries.GetAgentBySystemKey(r.Context(), db.GetAgentBySystemKeyParams{
		WorkspaceID: wsUUID,
		SystemKey:   pgtype.Text{String: service.MikaSystemKey, Valid: true},
	}); err == nil {
		h.writeMikaAgentResponse(w, r, existing, workspaceID, false)
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to look up the workspace agent")
		return
	}

	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	runtime, err := h.Queries.GetAgentRuntime(r.Context(), runtimeID)
	if err != nil || uuidToString(runtime.WorkspaceID) != workspaceID {
		writeError(w, http.StatusBadRequest, "runtime not found in this workspace")
		return
	}
	if !canUseRuntimeForAgent(member, runtime) {
		writeError(w, http.StatusForbidden, "you cannot bind an agent to this runtime")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start agent create transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	// Serialize provisioning per workspace. The check above is only a fast
	// path: without this, two members clicking "Start with Mika" at the same
	// moment both miss and both insert. Migration 172's unique index would not
	// stop them either — it covers (workspace_id, owner_id, runtime_id,
	// system_key), so different owners or different runtimes are distinct
	// tuples, and the workspace would end up with two live Mikas.
	if _, err := tx.Exec(r.Context(),
		"SELECT pg_advisory_xact_lock(hashtextextended($1, 0))",
		"mika:"+workspaceID,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock the workspace agent")
		return
	}
	// Re-check under the lock: a request that was mid-flight when we started
	// may have committed by now.
	if existing, err := qtx.GetAgentBySystemKey(r.Context(), db.GetAgentBySystemKeyParams{
		WorkspaceID: wsUUID,
		SystemKey:   pgtype.Text{String: service.MikaSystemKey, Valid: true},
	}); err == nil {
		h.writeMikaAgentResponse(w, r, existing, workspaceID, false)
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to look up the workspace agent")
		return
	}

	created, err := qtx.CreateSystemUserAgent(r.Context(), db.CreateSystemUserAgentParams{
		WorkspaceID:        wsUUID,
		Name:               service.MikaDefaultName,
		Description:        description,
		AvatarUrl:          pgtype.Text{String: mikaAgentAvatarURL, Valid: true},
		RuntimeMode:        runtime.RuntimeMode,
		RuntimeID:          runtime.ID,
		Visibility:         mikaAgentVisibility,
		PermissionMode:     mikaAgentPermissionMode,
		MaxConcurrentTasks: mikaAgentMaxConcurrency,
		OwnerID:            parseUUID(userID),
		SystemKey:          pgtype.Text{String: service.MikaSystemKey, Valid: true},
	})
	if err != nil {
		slog.Warn("create mika agent failed", append(logger.RequestAttrs(r), "error", err, "workspace_id", workspaceID)...)
		writeError(w, http.StatusInternalServerError, "failed to create the workspace agent")
		return
	}
	// Workspace-invocable: every member may chat with Mika and assign it work.
	if err := replaceInvocationTargetsWithQueries(r.Context(), qtx, created.ID, parseUUID(userID), []targetSpec{
		{targetType: invocationTargetWorkspace, targetID: wsUUID},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save agent access")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit agent create")
		return
	}
	slog.Info("mika agent created", append(logger.RequestAttrs(r), "agent_id", uuidToString(created.ID), "workspace_id", workspaceID)...)

	if runtime.Status == "online" {
		h.TaskService.ReconcileAgentStatus(r.Context(), created.ID)
		created, _ = h.Queries.GetAgent(r.Context(), created.ID)
	}
	h.writeMikaAgentResponse(w, r, created, workspaceID, true)
}

func (h *Handler) writeMikaAgentResponse(w http.ResponseWriter, r *http.Request, agent db.Agent, workspaceID string, created bool) {
	resp := h.agentToResponse(agent)
	if err := h.enrichAgentResponseWithTargets(r.Context(), &resp, agent.ID); err != nil {
		slog.Warn("mika agent: load invocation targets failed", append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(agent.ID))...)
	}
	if created {
		actorType, actorID := h.resolveActor(r, uuidToString(agent.OwnerID), workspaceID)
		h.publish(protocol.EventAgentCreated, workspaceID, actorType, actorID, map[string]any{"agent": broadcastAgentResponse(resp)})
		writeJSON(w, http.StatusCreated, resp)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// systemInstructionsFor exposes the product-owned half of a system agent's
// prompt so the UI can show it read-only. Ordinary agents get "" — their whole
// prompt is already in the editable Instructions field.
func systemInstructionsFor(a db.Agent) string {
	if a.SystemKey.String == service.MikaSystemKey {
		return service.MikaSystemInstructions(a.Name)
	}
	return ""
}
