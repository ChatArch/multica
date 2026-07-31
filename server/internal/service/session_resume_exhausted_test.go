package service

import "testing"

// The GLM gateway rejection from GH #6143. It matches none of the poisoned-
// session text patterns, so a current daemon still files it as unknown — which
// is exactly the case the breaker exists to bound.
const gh6143GatewayError = "agent_error.unknown"

// TestEvaluateResumeExhaustionThreshold covers the core of GH #6143: an error
// nobody recognises must cost one wasted run, not a permanently dead
// (agent, issue) pair.
//
// The first failure deliberately still resumes. An unrecognised error is not
// yet evidence that the transcript is at fault, and starting fresh on every
// first stumble would throw away conversation context whenever a provider
// hiccupped in a way the classifier has not seen.
func TestEvaluateResumeExhaustionThreshold(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		currentReason string
		sessionID     string
		prior         []priorResumeFailure
		wantRetire    bool
		wantSessionID string
	}{
		{
			name:          "first unrecognised failure still resumes",
			currentReason: gh6143GatewayError,
			sessionID:     "sess-a",
			prior:         nil,
			wantRetire:    false,
		},
		{
			name:          "second consecutive failure retires the conversation",
			currentReason: gh6143GatewayError,
			sessionID:     "sess-a",
			prior:         []priorResumeFailure{{SessionID: "sess-a", FailureReason: gh6143GatewayError}},
			wantRetire:    true,
			wantSessionID: "sess-a",
		},
		{
			// The window resets on success, so these two failures are not
			// consecutive — ListResumeFailuresSinceLastSuccess would not
			// return the pre-success row at all.
			name:          "a success in between clears the window",
			currentReason: gh6143GatewayError,
			sessionID:     "sess-b",
			prior:         nil,
			wantRetire:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			retire, sessionID := evaluateResumeExhaustion(tc.currentReason, tc.sessionID, tc.prior)
			if retire != tc.wantRetire {
				t.Fatalf("retire = %v, want %v", retire, tc.wantRetire)
			}
			if retire && sessionID != tc.wantSessionID {
				t.Fatalf("retired session = %q, want %q", sessionID, tc.wantSessionID)
			}
		})
	}
}

// TestEvaluateResumeExhaustionTransient is the false-positive guard. Discarding
// a healthy session costs real conversation context, which is why the GH #6143
// review rejected the reporter's "any 400 is poison" patch — under it a single
// rate-limit whose body mentions 400ms would have burned the session.
func TestEvaluateResumeExhaustionTransient(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		currentReason string
		prior         []priorResumeFailure
		wantRetire    bool
	}{
		{
			name:          "repeated daemon outages never retire",
			currentReason: "runtime_offline",
			prior: []priorResumeFailure{
				{SessionID: "sess-a", FailureReason: "runtime_offline"},
				{SessionID: "sess-a", FailureReason: "runtime_offline"},
			},
			wantRetire: false,
		},
		{
			// Not auto-retried, but unambiguously transient: two rate-limited
			// runs say nothing about whether the transcript can be replayed.
			name:          "repeated rate limits never retire",
			currentReason: "agent_error.provider_capacity_or_rate_limit",
			prior: []priorResumeFailure{
				{SessionID: "sess-a", FailureReason: "agent_error.provider_capacity_or_rate_limit"},
			},
			wantRetire: false,
		},
		{
			name:          "provider 5xx never retires",
			currentReason: "agent_error.provider_server_error",
			prior: []priorResumeFailure{
				{SessionID: "sess-a", FailureReason: "agent_error.provider_server_error"},
			},
			wantRetire: false,
		},
		{
			// The interleaving case. A transient blip between two real
			// failures is skipped, NOT treated as a reset: letting a flaky
			// daemon clear the count would hide a genuinely dead session
			// forever.
			name:          "a transient blip between two real failures does not reset the count",
			currentReason: gh6143GatewayError,
			prior: []priorResumeFailure{
				{SessionID: "sess-a", FailureReason: gh6143GatewayError},
				{SessionID: "sess-a", FailureReason: "runtime_offline"},
			},
			wantRetire: true,
		},
		{
			// One real failure plus noise is still only one real failure.
			name:          "transient noise alone cannot reach the threshold",
			currentReason: gh6143GatewayError,
			prior: []priorResumeFailure{
				{SessionID: "sess-a", FailureReason: "runtime_offline"},
				{SessionID: "sess-a", FailureReason: "timeout"},
				{SessionID: "sess-a", FailureReason: "queued_expired"},
			},
			wantRetire: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			retire, _ := evaluateResumeExhaustion(tc.currentReason, "sess-a", tc.prior)
			if retire != tc.wantRetire {
				t.Fatalf("retire = %v, want %v", retire, tc.wantRetire)
			}
		})
	}
}

// TestEvaluateResumeExhaustionSessionDrift covers the ACP backends, whose
// resolveResumedSessionID may answer a resume with a DIFFERENT session id when
// the provider's own state is gone.
//
// The upgraded failure_reason only excludes the row it is written on, so under
// drift the EARLIER id would stay eligible and be resumed on the next turn —
// the loop would survive the fix. Retiring the earlier id is what closes that.
func TestEvaluateResumeExhaustionSessionDrift(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		sessionID     string
		prior         []priorResumeFailure
		wantSessionID string
	}{
		{
			name:          "stable id retires itself",
			sessionID:     "sess-a",
			prior:         []priorResumeFailure{{SessionID: "sess-a", FailureReason: gh6143GatewayError}},
			wantSessionID: "sess-a",
		},
		{
			// The current id is already covered by the reason written on this
			// row; the drifted predecessor is the one that needs retiring.
			name:          "drifted id retires the predecessor",
			sessionID:     "sess-b",
			prior:         []priorResumeFailure{{SessionID: "sess-a", FailureReason: gh6143GatewayError}},
			wantSessionID: "sess-a",
		},
		{
			// A run that died before establishing a session records no id.
			name:          "empty prior id falls back to the current session",
			sessionID:     "sess-b",
			prior:         []priorResumeFailure{{SessionID: "", FailureReason: gh6143GatewayError}},
			wantSessionID: "sess-b",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			retire, sessionID := evaluateResumeExhaustion(gh6143GatewayError, tc.sessionID, tc.prior)
			if !retire {
				t.Fatalf("retire = false, want true")
			}
			if sessionID != tc.wantSessionID {
				t.Fatalf("retired session = %q, want %q", sessionID, tc.wantSessionID)
			}
		})
	}
}

// TestSessionResumeExhaustedIsResumeUnsafe pins the wiring that makes the
// breaker reach the manual-rerun path. That path reads the exact source task
// through ResumeUnsafeFailure and never consults the retired_sessions CTE, so
// without this the Rerun button would keep resuming a retired conversation —
// and Rerun is the first thing a user reaches for.
func TestSessionResumeExhaustedIsResumeUnsafe(t *testing.T) {
	t.Parallel()

	if !ResumeUnsafeFailure("session_resume_exhausted", "") {
		t.Fatal("session_resume_exhausted must be resume-unsafe so manual rerun starts fresh")
	}
	if !resumeUnsafeFailureReason("session_resume_exhausted") {
		t.Fatal("session_resume_exhausted missing from the resume-unsafe reason set")
	}
}

// TestTransientResumeSafeSupersetOfRetryable pins the superset relationship. If
// a reason is safe enough to auto-retry it is safe enough not to burn the
// session, and a future addition to retryableReasons must not silently start
// tripping the breaker.
func TestTransientResumeSafeSupersetOfRetryable(t *testing.T) {
	t.Parallel()

	for reason := range retryableReasons {
		if !transientResumeSafeReasons[reason] {
			t.Errorf("retryable reason %q is not in transientResumeSafeReasons", reason)
		}
	}
	// The members that are NOT auto-retryable are the whole reason this set
	// exists separately; losing them would reintroduce the false positive.
	for _, reason := range []string{
		"agent_error.provider_capacity_or_rate_limit",
		"agent_error.provider_server_error",
		"queued_expired",
	} {
		if !transientResumeSafeReasons[reason] {
			t.Errorf("transient reason %q must not count toward the breaker", reason)
		}
	}
}
