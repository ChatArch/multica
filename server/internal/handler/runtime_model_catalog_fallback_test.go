package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestReportModelListResult_DecodesFallback pins the additive daemon → server
// field behind MUL-5549. An older daemon omits it entirely, which must decode
// to false so its reports keep the pre-fix behaviour rather than being treated
// as permanently degraded.
func TestReportModelListResult_DecodesFallback(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name:    "daemon reports a fallback catalog",
			payload: `{"status":"completed","supported":true,"fallback":true,"models":[{"id":"a","label":"A"}]}`,
			want:    true,
		},
		{
			name:    "daemon reports a real catalog",
			payload: `{"status":"completed","supported":true,"fallback":false,"models":[{"id":"a","label":"A"}]}`,
			want:    false,
		},
		{
			name:    "older daemon omits the field",
			payload: `{"status":"completed","supported":true,"models":[{"id":"a","label":"A"}]}`,
			want:    false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/daemon/runtimes/rt/models/req/result", bytes.NewBufferString(tc.payload))
			var body struct {
				Status   string       `json:"status"`
				Models   []ModelEntry `json:"models"`
				Fallback bool         `json:"fallback"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body.Fallback != tc.want {
				t.Errorf("fallback = %v, want %v", body.Fallback, tc.want)
			}
		})
	}
}

// TestModelCatalogCacheDecision pins the three-way branch ReportModelListResult
// takes on a completed report.
//
// The fallback row is the MUL-5549 regression. A static stand-in is non-empty
// and `supported`, so it used to be indistinguishable from a real catalog and
// got stored as last-known-good for the full 24h serve window — pinning one
// transient discovery failure as the answer. It must now be Keep: not stored,
// but also not allowed to evict a real catalog we already hold.
func TestModelCatalogCacheDecision(t *testing.T) {
	for _, tc := range []struct {
		name      string
		models    []ModelEntry
		supported bool
		fallback  bool
		want      modelCatalogCacheAction
	}{
		{
			name:      "real non-empty catalog is stored",
			models:    sampleCatalog(),
			supported: true,
			want:      modelCatalogCacheStore,
		},
		{
			name:      "fallback catalog leaves the cache alone",
			models:    sampleCatalog(),
			supported: true,
			fallback:  true,
			want:      modelCatalogCacheKeep,
		},
		{
			name:      "authoritatively empty catalog drops the snapshot",
			models:    nil,
			supported: true,
			want:      modelCatalogCacheDrop,
		},
		{
			name:      "runtime that ignores model selection drops the snapshot",
			models:    sampleCatalog(),
			supported: false,
			want:      modelCatalogCacheDrop,
		},
		{
			name:      "an empty fallback still only keeps",
			models:    nil,
			supported: true,
			fallback:  true,
			want:      modelCatalogCacheKeep,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := modelCatalogCacheDecision(tc.models, tc.supported, tc.fallback)
			if got != tc.want {
				t.Errorf("modelCatalogCacheDecision = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestModelCatalogCacheKeepPreservesLastKnownGood is the user-visible point of
// the Keep action: a runtime that discovered a real catalog and then hits a
// transient discovery failure keeps serving the real one, instead of having it
// replaced by (or evicted in favour of) a static stand-in.
func TestModelCatalogCacheKeepPreservesLastKnownGood(t *testing.T) {
	ctx := t.Context()
	cache := NewInMemoryModelCatalogCache()
	const runtimeID = "rt-fallback"

	if err := cache.Put(ctx, runtimeID, sampleCatalog(), true); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	// A fallback report arrives; the handler's decision is Keep, so it neither
	// Puts nor Invalidates.
	standIn := []ModelEntry{{ID: "claude-sonnet-4.6", Label: "Claude Sonnet 4.6"}}
	if got := modelCatalogCacheDecision(standIn, true, true); got != modelCatalogCacheKeep {
		t.Fatalf("expected Keep for a fallback report, got %v", got)
	}

	snapshot, err := cache.Get(ctx, runtimeID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if snapshot == nil {
		t.Fatal("a fallback report must not evict the last known good catalog")
	}
	if len(snapshot.Models) != len(sampleCatalog()) || snapshot.Models[0].ID != sampleCatalog()[0].ID {
		t.Errorf("cached catalog was overwritten by the stand-in: %+v", snapshot.Models)
	}
}
