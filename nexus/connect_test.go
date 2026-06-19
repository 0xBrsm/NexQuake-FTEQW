package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xBrsm/NexQuake/nexus/internal/access"
	"github.com/0xBrsm/NexQuake/nexus/internal/admin"
	"github.com/0xBrsm/NexQuake/nexus/internal/assets"
)

// captureLogger returns a *slog.Logger that appends "msg key=value ..."
// lines to entries. Mirrors the same helper in the admin package's
// fakes_test.go (different package, so duplicated).
func captureLogger(entries *[]string) *slog.Logger {
	return slog.New(&captureHandlerTest{entries: entries})
}

type captureHandlerTest struct{ entries *[]string }

func (h *captureHandlerTest) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandlerTest) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value.Any())
		return true
	})
	*h.entries = append(*h.entries, b.String())
	return nil
}
func (h *captureHandlerTest) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandlerTest) WithGroup(string) slog.Handler      { return h }

func TestHandleRcon_AuditsUnauthorizedRequest(t *testing.T) {
	t.Setenv("AUTH_CLIENT_IP_HEADER", "")

	var entries []string
	id := access.NewIdentity()
	accessGate := access.NewGate(nil, id, &access.Auth{}, access.NewBlocklist())
	app := &nexusApp{
		access: accessGate,
		admin:  admin.New(nil, nil, captureLogger(&entries), nil, accessGate),
	}

	req := httptest.NewRequest("POST", "/rcon", strings.NewReader(`{"jsonrpc":"2.0","method":"server.list","id":1}`))
	req.RemoteAddr = "198.51.100.1:4242"
	rr := httptest.NewRecorder()

	app.handleRcon(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"code":-32000`) {
		t.Fatalf("expected unauthorized JSON-RPC response, got %s", rr.Body.String())
	}
	if len(entries) != 1 {
		t.Fatalf("expected one audit entry, got %d: %v", len(entries), entries)
	}
	if !strings.Contains(entries[0], `actor="198.51.100.1"`) ||
		!strings.Contains(entries[0], "method=server.list") ||
		!strings.Contains(entries[0], "admin-rcon error") ||
		!strings.Contains(entries[0], `error="unauthorized"`) {
		t.Fatalf("audit entry: %q", entries[0])
	}
}

func TestMux_BlocksSourceBeforeRoutes(t *testing.T) {
	blocklist := access.NewBlocklist()
	blocklist.Block("198.51.100.1")
	accessGate := access.NewGate(nil, &access.Identity{}, &access.Auth{}, blocklist)
	app := newTestApp(t, accessGate)

	req := httptest.NewRequest("GET", "/health", nil)
	req.RemoteAddr = "198.51.100.1:4242"
	rr := httptest.NewRecorder()

	accessGate.HTTPGate(app.newMux()).ServeHTTP(rr, req)

	if rr.Code != 403 {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestMux_BlocksAssetFetchAfterStart(t *testing.T) {
	blocklist := access.NewBlocklist()
	accessGate := access.NewGate(nil, &access.Identity{}, &access.Auth{}, blocklist)

	gameDir := t.TempDir()
	commonDir := filepath.Join(gameDir, "id1", "common")
	if err := os.MkdirAll(commonDir, 0o755); err != nil {
		t.Fatalf("mkdir common: %v", err)
	}
	if err := os.WriteFile(filepath.Join(commonDir, "config.cfg"), []byte("echo ok\n"), 0o644); err != nil {
		t.Fatalf("write game file: %v", err)
	}

	app := newTestApp(t, accessGate)
	app.cfg.gameDir = gameDir
	app.assetServer = assets.NewHashedAssetServer(gameDir, app.cfg.cdDir, assets.NewPakIndexCache())
	handler := accessGate.HTTPGate(app.newMux())

	startReq := httptest.NewRequest(http.MethodGet, "/start", nil)
	startReq.RemoteAddr = "198.51.100.1:4242"
	startRec := httptest.NewRecorder()
	handler.ServeHTTP(startRec, startReq)
	if startRec.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", startRec.Code, startRec.Body.String())
	}

	blocklist.Block("198.51.100.1")

	assetReq := httptest.NewRequest(http.MethodGet, "/nq/not-a-real-hash", nil)
	assetReq.RemoteAddr = "198.51.100.1:4242"
	assetRec := httptest.NewRecorder()
	handler.ServeHTTP(assetRec, assetReq)

	if assetRec.Code != http.StatusForbidden {
		t.Fatalf("asset status=%d body=%s", assetRec.Code, assetRec.Body.String())
	}
}

// newTestApp constructs a minimal *nexusApp suitable for mux-level tests.
// Callers may overwrite cfg fields and assetServer afterward.
func newTestApp(t *testing.T, gate *access.Gate) *nexusApp {
	t.Helper()
	cfg := runtimeConfig{
		clientDir: t.TempDir(),
		gameDir:   t.TempDir(),
		cdDir:     t.TempDir(),
	}
	return &nexusApp{
		cfg:         cfg,
		access:      gate,
		assetServer: assets.NewHashedAssetServer(cfg.gameDir, cfg.cdDir, assets.NewPakIndexCache()),
	}
}

// handleRconSession (POST /rcon/session) — the server-side half of the browser
// PKCE login. The fakeIdP harness lives in fakes_test.go.

func TestHandleRconSession_SetsHTTPOnlyCookieAndReportsAuthorized(t *testing.T) {
	idp := newFakeIdP(t, "nq-aud")
	idp.wantCode = "auth-code-xyz"
	idp.wantVerifier = "pkce-verifier-abc"
	// handleRconSession derives redirect_uri from the request authority; the
	// default httptest request Host is example.com.
	idp.wantRedirectURI = "https://example.com/rcon"
	idp.extraClaims = map[string]any{"groups": []any{"quake-admins"}}

	// AUTH_ADMIN_ID matches the token's group → authorized.
	app := newPKCEApp(t, idp, "groups:quake-admins")

	rr := postSession(t, app, `{"code":"auth-code-xyz","code_verifier":"pkce-verifier-abc"}`)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var out struct {
		Authorized bool `json:"authorized"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode body %q: %v", rr.Body.String(), err)
	}
	if !out.Authorized {
		t.Fatalf("expected authorized=true, got body %s", rr.Body.String())
	}

	cookie := findCookie(rr.Result().Cookies(), access.SessionCookieName)
	if cookie == nil {
		t.Fatalf("expected %s cookie to be set; got %v", access.SessionCookieName, rr.Result().Cookies())
	}
	if !cookie.HttpOnly {
		t.Error("session cookie must be HttpOnly so the token never reaches page JS")
	}
	if !cookie.Secure {
		t.Error("session cookie must be Secure")
	}
	if cookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("session cookie SameSite=%v, want Strict", cookie.SameSite)
	}
	if cookie.Path != "/" {
		t.Errorf("session cookie Path=%q, want /", cookie.Path)
	}
	if cookie.MaxAge <= 0 {
		t.Errorf("session cookie MaxAge=%d, want a positive lifetime from the token expiry", cookie.MaxAge)
	}
	// The cookie value is the verified id_token itself.
	if got := strings.Count(cookie.Value, "."); got != 2 {
		t.Errorf("session cookie value is not a JWT (dots=%d): %q", got, cookie.Value)
	}
}

func TestHandleRconSession_AnySentinelAdmitsAnyVerifiedJWT(t *testing.T) {
	idp := newFakeIdP(t, "nq-aud")
	idp.wantCode = "code"
	idp.wantVerifier = "verifier"
	idp.wantRedirectURI = "https://example.com/rcon"
	// No admin-shaped claims at all; AUTH_ADMIN_ID=any must still authorize.
	app := newPKCEApp(t, idp, "any")

	rr := postSession(t, app, `{"code":"code","code_verifier":"verifier"}`)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"authorized":true`) {
		t.Fatalf("expected authorized=true for AUTH_ADMIN_ID=any, got %s", rr.Body.String())
	}
}

func TestHandleRconSession_VerifiedButNotAdmin(t *testing.T) {
	idp := newFakeIdP(t, "nq-aud")
	idp.wantCode = "code"
	idp.wantVerifier = "verifier"
	idp.wantRedirectURI = "https://example.com/rcon"

	// Token verifies, but its claims match no admin rule.
	app := newPKCEApp(t, idp, "groups:some-other-team")

	rr := postSession(t, app, `{"code":"code","code_verifier":"verifier"}`)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"authorized":false`) {
		t.Fatalf("expected authorized=false, got %s", rr.Body.String())
	}
	// The session cookie is still set: the token is valid, and admin is
	// re-evaluated per request at POST /rcon.
	if findCookie(rr.Result().Cookies(), access.SessionCookieName) == nil {
		t.Fatal("expected the session cookie to be set even when not an admin")
	}
}

func TestHandleRconSession_404WhenPKCEDisabled(t *testing.T) {
	// No EXTERNAL_URL → not direct exposure → PKCE off → the route 404s.
	t.Setenv("AUTH_ISSUER", "")
	t.Setenv("AUTH_AUDIENCE", "")
	t.Setenv("EXTERNAL_URL", "")
	auth, err := access.InitAuth()
	if err != nil {
		t.Fatalf("InitAuth: %v", err)
	}
	gate := access.NewGate(nil, access.NewIdentity(), auth, access.NewBlocklist())
	app := &nexusApp{access: gate}

	rr := postSession(t, app, `{"code":"c","code_verifier":"v"}`)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 when PKCE is disabled; body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleRconSession_RejectsMissingFields(t *testing.T) {
	idp := newFakeIdP(t, "nq-aud")
	app := newPKCEApp(t, idp, "")

	for _, body := range []string{
		`{"code":"","code_verifier":"v"}`,
		`{"code":"c","code_verifier":""}`,
		`not json`,
	} {
		rr := postSession(t, app, body)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("body %q: status=%d, want 400", body, rr.Code)
		}
	}
}

func TestHandleRconSession_TokenExchangeFailureIsBadGateway(t *testing.T) {
	idp := newFakeIdP(t, "nq-aud")
	idp.wantCode = "right-code"
	idp.wantVerifier = "right-verifier"
	app := newPKCEApp(t, idp, "")

	// Wrong code → the IdP returns invalid_grant → ExchangeCode fails.
	rr := postSession(t, app, `{"code":"wrong-code","code_verifier":"right-verifier"}`)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502 on token-exchange failure; body=%s", rr.Code, rr.Body.String())
	}
	if findCookie(rr.Result().Cookies(), access.SessionCookieName) != nil {
		t.Fatal("no session cookie should be set when the exchange fails")
	}
}

func TestHandleRconSession_RejectsOversizedToken(t *testing.T) {
	idp := newFakeIdP(t, "nq-aud")
	idp.wantCode = "code"
	idp.wantVerifier = "verifier"
	idp.wantRedirectURI = "https://example.com/rcon"
	// Pad the token past SessionCookieMaxLen with a large (but valid) claim so
	// it verifies yet won't fit in a cookie.
	idp.extraClaims = map[string]any{"padding": strings.Repeat("x", access.SessionCookieMaxLen)}
	app := newPKCEApp(t, idp, "")

	rr := postSession(t, app, `{"code":"code","code_verifier":"verifier"}`)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d, want 413 for an oversized id_token; body=%s", rr.Code, rr.Body.String())
	}
	if findCookie(rr.Result().Cookies(), access.SessionCookieName) != nil {
		t.Fatal("no session cookie should be set when the token is too large")
	}
}

// Guard against the redirect_uri derivation drifting: the IdP only mints a
// token when redirect_uri matches what the browser would have sent.
func TestHandleRconSession_DerivesRedirectURIFromHost(t *testing.T) {
	idp := newFakeIdP(t, "nq-aud")
	idp.wantCode = "code"
	idp.wantVerifier = "verifier"
	idp.wantRedirectURI = "https://custom-host.test/rcon"
	app := newPKCEApp(t, idp, "")

	req := httptest.NewRequest(http.MethodPost, "/rcon/session", strings.NewReader(`{"code":"code","code_verifier":"verifier"}`))
	req.Host = "custom-host.test"
	rr := httptest.NewRecorder()
	app.handleRconSession(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	// Sanity: the URL we told the IdP to require is well-formed.
	if _, err := url.Parse(idp.wantRedirectURI); err != nil {
		t.Fatalf("bad test redirect URI: %v", err)
	}
}
