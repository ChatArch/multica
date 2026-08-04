package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestDeleteWorkspace_SweepsInboxV2Tables covers the workspace half of the
// inbox v2 lifecycle contract: deleting a workspace must leave no inbox_event
// or inbox_group rows behind.
//
// These two tables carry no foreign keys (repo rule), so nothing sweeps them
// implicitly — the DeleteWorkspaceLeafData statement has to name them. The
// manifest test catches a table that was never classified; this one catches the
// case the manifest cannot see, where a table is classified as `delete` but the
// deletion graph does not actually delete it.
//
// A neighbouring workspace is seeded with the same shapes so the test also
// pins the scope: the sweep is workspace_id = $1, not a global truncate.
func TestDeleteWorkspace_SweepsInboxV2Tables(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	const targetSlug = "handler-tests-delete-inbox-v2-target"
	const neighborSlug = "handler-tests-delete-inbox-v2-neighbor"

	_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE slug IN ($1, $2)`, targetSlug, neighborSlug)

	var targetWorkspaceID, neighborWorkspaceID string
	if err := testPool.QueryRow(ctx, `
INSERT INTO workspace (name, slug)
VALUES ('Inbox v2 delete target', $1)
RETURNING id
`, targetSlug).Scan(&targetWorkspaceID); err != nil {
		t.Fatalf("create target workspace: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
INSERT INTO workspace (name, slug)
VALUES ('Inbox v2 delete neighbor', $1)
RETURNING id
`, neighborSlug).Scan(&neighborWorkspaceID); err != nil {
		t.Fatalf("create neighbor workspace: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		for _, workspaceID := range []string{targetWorkspaceID, neighborWorkspaceID} {
			// inbox_event / inbox_group have no FK to workspace, so they
			// outlive the workspace row unless removed explicitly.
			_, _ = testPool.Exec(bg, `DELETE FROM inbox_event WHERE workspace_id = $1`, workspaceID)
			_, _ = testPool.Exec(bg, `DELETE FROM inbox_group WHERE workspace_id = $1`, workspaceID)
			_, _ = testPool.Exec(bg, `DELETE FROM workspace WHERE id = $1`, workspaceID)
		}
	})

	if _, err := testPool.Exec(ctx, `
INSERT INTO member (workspace_id, user_id, role)
VALUES ($1, $2, 'owner')
`, targetWorkspaceID, testUserID); err != nil {
		t.Fatalf("create target owner: %v", err)
	}

	// Seed one group plus two events per workspace. Two events matter: a sweep
	// that only removed the group would still be caught by the event count.
	seed := func(workspaceID string) {
		t.Helper()
		var groupID string
		if err := testPool.QueryRow(ctx, `
INSERT INTO inbox_group (workspace_id, recipient_id, source_kind, source_id, latest_seq, latest_event_at, surfaced_at)
VALUES ($1, $2, 'issue', gen_random_uuid(), 2, now(), now())
RETURNING id
`, workspaceID, testUserID).Scan(&groupID); err != nil {
			t.Fatalf("seed inbox_group for %s: %v", workspaceID, err)
		}
		for seq := 1; seq <= 2; seq++ {
			if _, err := testPool.Exec(ctx, `
INSERT INTO inbox_event (
	group_id, workspace_id, event_seq, type, target_kind, target_id, delivery_key, created_at
)
VALUES ($1, $2, $3, 'new_comment', 'comment', gen_random_uuid(), $4, now())
`, groupID, workspaceID, seq, "v1:workspace-delete-"+workspaceID+"-"+string(rune('0'+seq))); err != nil {
				t.Fatalf("seed inbox_event for %s: %v", workspaceID, err)
			}
		}
	}
	seed(targetWorkspaceID)
	seed(neighborWorkspaceID)

	recorder := httptest.NewRecorder()
	request := newRequest(http.MethodDelete, "/api/workspaces/"+targetWorkspaceID, nil)
	request = withURLParam(request, "id", targetWorkspaceID)
	testHandler.DeleteWorkspace(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("DeleteWorkspace returned %d: %s", recorder.Code, recorder.Body.String())
	}

	count := func(table, workspaceID string) int {
		t.Helper()
		var n int
		if err := testPool.QueryRow(ctx,
			`SELECT COUNT(*) FROM `+table+` WHERE workspace_id = $1`, workspaceID).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		return n
	}

	for _, table := range []string{"inbox_event", "inbox_group"} {
		if got := count(table, targetWorkspaceID); got != 0 {
			t.Errorf("%s left %d rows behind after workspace deletion, want 0", table, got)
		}
	}

	if got := count("inbox_group", neighborWorkspaceID); got != 1 {
		t.Errorf("neighbor inbox_group rows = %d, want 1 (the sweep must be workspace-scoped)", got)
	}
	if got := count("inbox_event", neighborWorkspaceID); got != 2 {
		t.Errorf("neighbor inbox_event rows = %d, want 2 (the sweep must be workspace-scoped)", got)
	}
}
