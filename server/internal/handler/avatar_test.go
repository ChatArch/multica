package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/storage"
)

// withAvatarStorage swaps the handler's storage/config for one test and
// restores it afterwards. Mirrors the setup the attachment download tests use.
func withAvatarStorage(t *testing.T, store storage.Storage, publicURL string) {
	t.Helper()
	origStorage := testHandler.Storage
	origCfg := testHandler.cfg
	origSigner := testHandler.CFSigner
	testHandler.Storage = store
	testHandler.cfg.PublicURL = publicURL
	testHandler.cfg.AttachmentDownloadMode = "auto"
	testHandler.CFSigner = nil
	t.Cleanup(func() {
		testHandler.Storage = origStorage
		testHandler.cfg = origCfg
		testHandler.CFSigner = origSigner
	})
}

const testAvatarKey = "workspaces/11111111-1111-1111-1111-111111111111/avatar.png"

// TestResolveAvatarURL_PrivateStorageRewritesToSignedEndpoint is the #6024
// case: a private bucket with no public CDN stored a raw S3 URL in avatar_url,
// which the browser can only 403 on. Reads must hand back the signed avatar
// endpoint instead.
func TestResolveAvatarURL_PrivateStorageRewritesToSignedEndpoint(t *testing.T) {
	withAvatarStorage(t, &mockStorageNoCdn{}, "")

	raw := "https://cdn.example.com/" + testAvatarKey
	got := testHandler.resolveAvatarURL(raw)

	want := avatarURLPathPrefix + signAvatarKey(testAvatarKey) + "/" + testAvatarKey
	if got != want {
		t.Fatalf("resolveAvatarURL = %q, want %q", got, want)
	}
	key, ok := avatarKeyFromServedURL(got)
	if !ok || key != testAvatarKey {
		t.Fatalf("resolved URL does not verify back to the key: key=%q ok=%v", key, ok)
	}
}

// TestResolveAvatarURL_PublicCdnUnchanged guards the deployments that already
// work: a public CDN domain with no per-request signing serves the raw URL
// fine, and routing it through the API would only add a hop.
func TestResolveAvatarURL_PublicCdnUnchanged(t *testing.T) {
	withAvatarStorage(t, &mockStorage{}, "")

	raw := "https://cdn.example.com/" + testAvatarKey
	if got := testHandler.resolveAvatarURL(raw); got != raw {
		t.Fatalf("resolveAvatarURL = %q, want unchanged %q", got, raw)
	}
}

// TestResolveAvatarURL_CloudFrontSignedRewrites covers the other private
// shape: a CDN domain IS configured, but it serves private content through
// per-request signed URLs, so the unsigned stored URL is a 403.
func TestResolveAvatarURL_CloudFrontSignedRewrites(t *testing.T) {
	withAvatarStorage(t, &mockStorage{}, "")
	testHandler.CFSigner = testCloudFrontSigner(t)

	raw := "https://cdn.example.com/" + testAvatarKey
	if got := testHandler.resolveAvatarURL(raw); !strings.HasPrefix(got, avatarURLPathPrefix) {
		t.Fatalf("resolveAvatarURL = %q, want the signed avatar endpoint", got)
	}
}

// TestResolveAvatarURL_LocalStorageUnchanged — LocalStorage objects are served
// by the public /uploads/* route, so they are already loadable as-is.
func TestResolveAvatarURL_LocalStorageUnchanged(t *testing.T) {
	local := storage.NewLocalStorageFromEnv()
	if local == nil {
		t.Skip("local storage unavailable")
	}
	withAvatarStorage(t, local, "")

	raw := local.ObjectURL(testAvatarKey)
	if got := testHandler.resolveAvatarURL(raw); got != raw {
		t.Fatalf("resolveAvatarURL = %q, want unchanged %q", got, raw)
	}
}

// TestResolveAvatarURL_PassesThroughForeignValues — avatar_url also holds
// emoji markers, inline data URIs, and third-party profile URLs. None of them
// are our storage objects and all must survive untouched.
func TestResolveAvatarURL_PassesThroughForeignValues(t *testing.T) {
	withAvatarStorage(t, &mockStorageNoCdn{}, "")

	for _, raw := range []string{
		"",
		"emoji:🐙",
		"data:image/svg+xml,%3Csvg%3E%3C/svg%3E",
		"https://lh3.googleusercontent.com/a/profile.png",
		"https://avatars.githubusercontent.com/u/12345?v=4",
	} {
		if got := testHandler.resolveAvatarURL(raw); got != raw {
			t.Errorf("resolveAvatarURL(%q) = %q, want unchanged", raw, got)
		}
	}
}

// TestResolveAvatarURL_NonImageKeyUnchanged — an avatar_url pointed at a
// non-image object must not be laundered into a publicly fetchable URL.
func TestResolveAvatarURL_NonImageKeyUnchanged(t *testing.T) {
	withAvatarStorage(t, &mockStorageNoCdn{}, "")

	for _, key := range []string{"workspaces/ws/secret.pdf", "workspaces/ws/logo.svg", "workspaces/ws/noext"} {
		raw := "https://cdn.example.com/" + key
		if got := testHandler.resolveAvatarURL(raw); got != raw {
			t.Errorf("resolveAvatarURL(%q) = %q, want unchanged", raw, got)
		}
	}
}

// TestResolveAvatarURL_AnchorsOnPublicURL — clients that don't share the API's
// document origin (Desktop, mobile webview) need an absolute URL.
func TestResolveAvatarURL_AnchorsOnPublicURL(t *testing.T) {
	withAvatarStorage(t, &mockStorageNoCdn{}, "https://api.example.test/")

	got := testHandler.resolveAvatarURL("https://cdn.example.com/" + testAvatarKey)
	if !strings.HasPrefix(got, "https://api.example.test"+avatarURLPathPrefix) {
		t.Fatalf("resolveAvatarURL = %q, want it anchored on PublicURL", got)
	}
	key, ok := avatarKeyFromServedURL(got)
	if !ok || key != testAvatarKey {
		t.Fatalf("absolute URL does not verify back to the key: key=%q ok=%v", key, ok)
	}
}

// TestResolveAvatarURL_RoundTripIsStable — a client may PATCH a resolved
// response value straight back into avatar_url. Resolving it again must not
// nest a second prefix.
func TestResolveAvatarURL_RoundTripIsStable(t *testing.T) {
	withAvatarStorage(t, &mockStorageNoCdn{}, "")

	once := testHandler.resolveAvatarURL("https://cdn.example.com/" + testAvatarKey)
	if twice := testHandler.resolveAvatarURL(once); twice != once {
		t.Fatalf("resolveAvatarURL is not idempotent: %q then %q", once, twice)
	}
}

// TestResolveAvatarURL_ForgedAvatarPathNotReSigned — an unsigned or
// wrongly-signed avatar path must not be accepted and re-signed, or storing
// one would be a way to publish an arbitrary object.
func TestResolveAvatarURL_ForgedAvatarPathNotReSigned(t *testing.T) {
	withAvatarStorage(t, &mockStorageNoCdn{}, "")

	forged := avatarURLPathPrefix + "not-a-signature/" + testAvatarKey
	if got := testHandler.resolveAvatarURL(forged); got != forged {
		t.Fatalf("resolveAvatarURL(%q) = %q, want unchanged (no re-signing)", forged, got)
	}
}

// TestNormalizeStoredAvatarURL_RecoversObjectURL — the write side keeps the
// column holding a durable object reference even when a client sends back the
// resolved value.
func TestNormalizeStoredAvatarURL_RecoversObjectURL(t *testing.T) {
	withAvatarStorage(t, &mockStorageNoCdn{}, "https://api.example.test")

	raw := "https://cdn.example.com/" + testAvatarKey
	resolved := testHandler.resolveAvatarURL(raw)
	if got := testHandler.normalizeStoredAvatarURL(resolved); got != raw {
		t.Fatalf("normalizeStoredAvatarURL = %q, want the object URL %q", got, raw)
	}
	// Anything else is stored verbatim.
	for _, other := range []string{raw, "emoji:🐙", "https://lh3.googleusercontent.com/a/p.png"} {
		if got := testHandler.normalizeStoredAvatarURL(other); got != other {
			t.Errorf("normalizeStoredAvatarURL(%q) = %q, want unchanged", other, got)
		}
	}
}

func serveAvatarRequest(t *testing.T, sig, key string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/api/avatars/{sig}/*", testHandler.ServeAvatar)
	req := httptest.NewRequest(http.MethodGet, "/api/avatars/"+sig+"/"+key, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestServeAvatar_PresignRedirects — the private-bucket read path: a valid
// signature yields a 302 to a freshly presigned storage URL with no forced
// download disposition, so it renders inside an <img>.
func TestServeAvatar_PresignRedirects(t *testing.T) {
	withAvatarStorage(t, &mockStorageNoCdn{}, "")

	w := serveAvatarRequest(t, signAvatarKey(testAvatarKey), testAvatarKey)
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", w.Code, w.Body.String())
	}
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	if loc.Query().Get("X-Amz-Signature") == "" {
		t.Fatalf("Location = %q, want a presigned storage URL", loc.String())
	}
	if got := loc.Query().Get("response-content-disposition"); got != "" {
		t.Fatalf("response-content-disposition = %q, want inline-loadable URL", got)
	}
}

// TestServeAvatar_RejectsForgedSignature — the signature is the credential;
// without it the endpoint must not reach storage at all.
func TestServeAvatar_RejectsForgedSignature(t *testing.T) {
	withAvatarStorage(t, &mockStorageNoCdn{}, "")

	for _, sig := range []string{"", "deadbeef", signAvatarKey("workspaces/other/x.png")} {
		if w := serveAvatarRequest(t, sig, testAvatarKey); w.Code != http.StatusNotFound {
			t.Errorf("sig %q: status = %d, want 404", sig, w.Code)
		}
	}
}

// TestServeAvatar_RejectsNonImageKey — a correctly signed non-image key still
// 404s, so this route can never serve a document.
func TestServeAvatar_RejectsNonImageKey(t *testing.T) {
	withAvatarStorage(t, &mockStorageNoCdn{}, "")

	key := "workspaces/ws/private.pdf"
	if w := serveAvatarRequest(t, signAvatarKey(key), key); w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// TestServeAvatar_ProxiesPrivateHostBody — a bucket reachable only from inside
// the network (http://rustfs:9000) can't be redirected to, so the body streams
// through the API with an inline image content type.
func TestServeAvatar_ProxiesPrivateHostBody(t *testing.T) {
	store := &mockStorage{}
	withAvatarStorage(t, store, "")
	testHandler.cfg.AttachmentDownloadMode = "proxy"
	store.put(testAvatarKey, []byte("PNGBYTES"))

	w := serveAvatarRequest(t, signAvatarKey(testAvatarKey), testAvatarKey)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", got)
	}
	if got := w.Header().Get("Content-Disposition"); got != "inline" {
		t.Fatalf("Content-Disposition = %q, want inline", got)
	}
	if w.Body.String() != "PNGBYTES" {
		t.Fatalf("body = %q, want the stored object", w.Body.String())
	}
}
