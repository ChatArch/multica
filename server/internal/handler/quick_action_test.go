package handler

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestValidateQuickActionPromptWhitelist locks the closed variable set
// (MUL-5465 D5). An unknown {{token}} must be REJECTED at write time rather
// than surviving into the rendered prompt as a literal — the same rule
// autopilot's issue-title template enforces, and for the same reason: a typo
// that lands silently is a prompt nobody notices is broken.
func TestValidateQuickActionPromptWhitelist(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prompt  string
		wantErr bool
	}{
		{"plain text", "review this code", false},
		{"known variable", "review {{issue.title}} today", false},
		{"several known", "{{user.name}} asked on {{date}} about {{issue.identifier}}", false},
		{"whitespace tolerated", "review {{ issue.title }}", false},
		{"unknown variable", "review {{issue.body}}", true},
		{"typo in known variable", "review {{issue.titel}}", true},
		{"empty prompt", "   ", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateQuickActionPrompt(tc.prompt, false)
			if tc.wantErr && err == nil {
				t.Fatalf("expected %q to be rejected", tc.prompt)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected %q to be accepted, got %v", tc.prompt, err)
			}
		})
	}
}

// TestValidateQuickActionPromptInputAgreement locks the two-way {{input}}
// contract. Both mismatches are real product bugs: an enabled input with no
// slot silently discards what the user typed, and a slot with the input
// disabled renders literal braces into the agent's instructions.
func TestValidateQuickActionPromptInputAgreement(t *testing.T) {
	if _, err := validateQuickActionPrompt("review this", true); err == nil {
		t.Fatal("input enabled but prompt has no {{input}} must be rejected")
	}
	if _, err := validateQuickActionPrompt("review this {{input}}", false); err == nil {
		t.Fatal("prompt references {{input}} but input disabled must be rejected")
	}
	if _, err := validateQuickActionPrompt("review this {{input}}", true); err != nil {
		t.Fatalf("matching pair must be accepted, got %v", err)
	}
	if _, err := validateQuickActionPrompt("review this", false); err != nil {
		t.Fatalf("neither side using input must be accepted, got %v", err)
	}
}

// TestValidateQuickActionPromptLimits guards the length ceilings so a
// pathological prompt cannot be stored and then re-rendered on every click.
func TestValidateQuickActionPromptLimits(t *testing.T) {
	if _, err := validateQuickActionPrompt(strings.Repeat("x", maxQuickActionPromptLen+1), false); err == nil {
		t.Fatal("an over-long prompt must be rejected")
	}
	if _, err := validateQuickActionName(strings.Repeat("x", maxQuickActionNameLen+1)); err == nil {
		t.Fatal("an over-long name must be rejected")
	}
	if _, err := validateQuickActionName("  "); err == nil {
		t.Fatal("a blank name must be rejected")
	}
}

// TestRenderQuickActionPrompt covers the substitution itself, including the
// runtime input. Rendering is server-side precisely so this behaviour has ONE
// implementation; these assertions are that contract.
func TestRenderQuickActionPrompt(t *testing.T) {
	issue := db.Issue{
		ID:    pgtype.UUID{Bytes: [16]byte{0x11, 0x22, 0x33}, Valid: true},
		Title: "Login redirect loops",
	}

	got := renderQuickActionPrompt(
		"Review {{issue.identifier}} ({{issue.title}}) for {{user.name}}. Focus: {{input}}",
		issue, "MUL-42", "Jiayuan", "", "SQL injection",
	)
	want := "Review MUL-42 (Login redirect loops) for Jiayuan. Focus: SQL injection"
	if got != want {
		t.Fatalf("render mismatch:\n got: %q\nwant: %q", got, want)
	}

	// An optional input left blank renders to nothing rather than leaving the
	// literal token behind — the "click and Enter" path must produce a clean
	// prompt, not one with a dangling placeholder.
	blank := renderQuickActionPrompt("Review the code. {{input}}", issue, "MUL-42", "Jiayuan", "", "")
	if strings.Contains(blank, "{{input}}") {
		t.Fatalf("blank input must not leave the token behind, got %q", blank)
	}

	// {{date}} must resolve to a real date, never the literal token.
	dated := renderQuickActionPrompt("On {{date}}", issue, "MUL-42", "Jiayuan", "", "")
	if strings.Contains(dated, "{{date}}") || len(dated) != len("On 2006-01-02") {
		t.Fatalf("date must interpolate to YYYY-MM-DD, got %q", dated)
	}
}

// TestRenderQuickActionIssueURL locks the degradation path: with no configured
// public URL the variable renders empty instead of emitting a broken relative
// link into an agent's instructions.
func TestRenderQuickActionIssueURL(t *testing.T) {
	if got := quickActionIssueURL("", "abc"); got != "" {
		t.Fatalf("no public URL must render empty, got %q", got)
	}
	if got := quickActionIssueURL("https://app.example/", "abc"); got != "https://app.example/issues/abc" {
		t.Fatalf("unexpected issue URL %q", got)
	}
}

// TestValidateQuickActionAssignee locks the polymorphic binding to the two
// supported actor kinds; anything else would store a target the run path
// cannot resolve.
func TestValidateQuickActionAssignee(t *testing.T) {
	if err := validateQuickActionAssignee("agent", "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0"); err != nil {
		t.Fatalf("agent binding must be accepted, got %v", err)
	}
	if err := validateQuickActionAssignee("squad", "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0"); err != nil {
		t.Fatalf("squad binding must be accepted, got %v", err)
	}
	if err := validateQuickActionAssignee("member", "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0"); err == nil {
		t.Fatal("a member binding must be rejected: members are not runnable targets")
	}
	if err := validateQuickActionAssignee("agent", "  "); err == nil {
		t.Fatal("a blank assignee id must be rejected")
	}
}
