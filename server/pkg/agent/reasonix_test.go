package agent

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestNewReturnsReasonixBackend(t *testing.T) {
	t.Parallel()
	b, err := New("reasonix", Config{ExecutablePath: "/nonexistent/reasonix"})
	if err != nil {
		t.Fatalf("New(reasonix) error: %v", err)
	}
	if _, ok := b.(*reasonixBackend); !ok {
		t.Fatalf("expected *reasonixBackend, got %T", b)
	}
}

func TestReasonixPermissionPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		params       string
		wantID       string
		wantGrant    bool
		wantOK       bool
		wantQuestion bool
	}{
		{
			name: "ordinary one-shot approval is allowed",
			params: `{"toolCall":{"toolCallId":"gate-1","_meta":{"reasonix.io":{"tool":"write_file"}}},` +
				`"options":[{"optionId":"allow_once","kind":"allow_once"},{"optionId":"reject_once","kind":"reject_once"}]}`,
			wantID: "allow_once", wantGrant: true, wantOK: true,
		},
		{
			name: "question selects cancel and remains visible",
			params: `{"toolCall":{"toolCallId":"ask-a-q1","title":"Choose a deployment"},` +
				`"options":[{"optionId":"q1:1","kind":"allow_once"},{"optionId":"q1:cancel","kind":"reject_once"}]}`,
			wantID: "q1:cancel", wantOK: true, wantQuestion: true,
		},
		{
			name: "fresh approval is rejected",
			params: `{"toolCall":{"toolCallId":"gate-2","_meta":{"reasonix.io":{"tool":"bash","fresh":true}}},` +
				`"options":[{"optionId":"allow_once","kind":"allow_once"},{"optionId":"reject_once","kind":"reject_once"}]}`,
			wantID: "reject_once", wantOK: true,
		},
		{
			name: "protected approval is rejected even without fresh metadata",
			params: `{"toolCall":{"toolCallId":"gate-3","_meta":{"reasonix.io":{"tool":"config_write"}}},` +
				`"options":[{"optionId":"allow_once","kind":"allow_once"},{"optionId":"reject_once","kind":"reject_once"}]}`,
			wantID: "reject_once", wantOK: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			id, grant, ok, question := selectReasonixPermissionOption(json.RawMessage(tt.params))
			if id != tt.wantID || grant != tt.wantGrant || ok != tt.wantOK {
				t.Fatalf("decision = (%q, %v, %v), want (%q, %v, %v)", id, grant, ok, tt.wantID, tt.wantGrant, tt.wantOK)
			}
			if (question != "") != tt.wantQuestion {
				t.Fatalf("question = %q, want present=%v", question, tt.wantQuestion)
			}
		})
	}
}

func TestReasonixStatusUsageTracker(t *testing.T) {
	t.Parallel()
	tracker := &reasonixStatusUsageTracker{}
	cost := 0.0123456789
	params := json.RawMessage(`{"schemaVersion":1,"sequence":7,"status":{"model":"deepseek-reasoner","usage":{"turn":{` +
		`"promptTokens":120,"completionTokens":30,"cacheHitTokens":80,"cacheMissTokens":40,` +
		`"estimatedCost":0.0123456789,"currency":"USD"}}}}`)
	tracker.observe("_reasonix.io/session/status_update", params)

	usage, model := tracker.snapshot()
	if model != "deepseek-reasoner" {
		t.Fatalf("model = %q", model)
	}
	if usage.InputTokens != 40 || usage.CacheReadTokens != 80 || usage.OutputTokens != 30 {
		t.Fatalf("usage = %+v", usage)
	}
	wantCost := int64(math.Round(cost * CostUSDTicksPerUSD))
	if usage.CostUSDTicks != wantCost {
		t.Fatalf("cost ticks = %d, want %d", usage.CostUSDTicks, wantCost)
	}

	// Older notifications cannot overwrite the newest turn snapshot.
	tracker.observe("_reasonix.io/session/status_update", json.RawMessage(`{"schemaVersion":1,"sequence":6,"status":{"model":"old","usage":{"turn":{"cacheMissTokens":1}}}}`))
	after, afterModel := tracker.snapshot()
	if after != usage || afterModel != model {
		t.Fatalf("older snapshot overwrote latest: %+v %q", after, afterModel)
	}
}

func TestReasonixResumeAndSetupErrors(t *testing.T) {
	t.Parallel()
	unknown := &acpRPCError{Method: "session/resume", Code: -32602, Message: "session/resume: unknown session dead"}
	if !isACPSessionNotFound(unknown) {
		t.Fatal("Reasonix unknown-session error was not classified")
	}
	lease := &acpRPCError{Method: "session/resume", Code: -32600, Message: "session is in use; close the other Reasonix window"}
	if !isReasonixSessionLeaseConflict(lease) {
		t.Fatal("Reasonix lease conflict was not classified")
	}
	if isReasonixSessionLeaseConflict(errors.New("close the other Reasonix window")) {
		t.Fatal("unstructured error was misclassified as a lease conflict")
	}
	if got := reasonixSessionNewError(errors.New("no default_model configured")); !strings.Contains(got, "reasonix setup") {
		t.Fatalf("setup hint missing from %q", got)
	}
}
