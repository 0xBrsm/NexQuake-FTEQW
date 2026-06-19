package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/0xBrsm/NexQuake/nexus/internal/access"
)

// fakeIdP is a minimal OIDC provider for exercising the PKCE token-exchange
// path end to end: it serves discovery + JWKS so access.InitJWT builds a real
// verifier, and a token endpoint that mints RS256 id_tokens this same key set
// can verify. Signing is hand-rolled on the stdlib to avoid pulling go-jose in
// as a direct test dependency.
type fakeIdP struct {
	server   *httptest.Server
	key      *rsa.PrivateKey
	kid      string
	issuer   string
	audience string

	// Token endpoint expectations. A mismatch yields HTTP 400 invalid_grant.
	wantCode        string
	wantVerifier    string
	wantRedirectURI string

	// extraClaims is merged into every minted id_token (e.g. groups for admin
	// gating, or a large padding claim to exceed the cookie size cap).
	extraClaims map[string]any
}

func newFakeIdP(t *testing.T, audience string) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	idp := &fakeIdP{key: key, kid: "test-key-1", audience: audience}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"issuer":                 idp.issuer,
			"authorization_endpoint": idp.issuer + "/authorize",
			"token_endpoint":         idp.issuer + "/token",
			"jwks_uri":               idp.issuer + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"keys": []any{idp.publicJWK()}})
	})
	mux.HandleFunc("/token", idp.handleToken)

	idp.server = httptest.NewServer(mux)
	idp.issuer = idp.server.URL
	t.Cleanup(idp.server.Close)
	return idp
}

func (idp *fakeIdP) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		tokenError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if r.Form.Get("grant_type") != "authorization_code" ||
		r.Form.Get("client_id") != idp.audience ||
		r.Form.Get("code") != idp.wantCode ||
		r.Form.Get("code_verifier") != idp.wantVerifier ||
		(idp.wantRedirectURI != "" && r.Form.Get("redirect_uri") != idp.wantRedirectURI) {
		tokenError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	writeJSON(w, map[string]any{"id_token": idp.mintIDToken(idp.extraClaims)})
}

// mintIDToken returns a signed RS256 id_token with the standard claims plus
// any extras. Test code uses it both via the token endpoint and directly (to
// seed an nq_session cookie).
func (idp *fakeIdP) mintIDToken(extra map[string]any) string {
	now := time.Now()
	claims := map[string]any{
		"iss": idp.issuer,
		"aud": idp.audience,
		"sub": "user-123",
		"iat": now.Unix(),
		"exp": now.Add(time.Hour).Unix(),
	}
	for k, v := range extra {
		claims[k] = v
	}
	return idp.sign(claims)
}

func (idp *fakeIdP) sign(claims map[string]any) string {
	header := map[string]any{"alg": "RS256", "typ": "JWT", "kid": idp.kid}
	signingInput := b64Segment(header) + "." + b64Segment(claims)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, idp.key, crypto.SHA256, digest[:])
	if err != nil {
		panic(err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (idp *fakeIdP) publicJWK() map[string]any {
	pub := idp.key.PublicKey
	return map[string]any{
		"kty": "RSA",
		"alg": "RS256",
		"use": "sig",
		"kid": idp.kid,
		"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}
}

func b64Segment(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func tokenError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": code})
}

// newPKCEApp builds a *nexusApp whose access gate is wired to idp with PKCE
// enabled (direct-exposure topology). adminID, when set, becomes AUTH_ADMIN_ID
// so callers can exercise the authorized vs. not-an-admin outcomes.
func newPKCEApp(t *testing.T, idp *fakeIdP, adminID string) *nexusApp {
	t.Helper()
	t.Setenv("AUTH_ISSUER", idp.issuer)
	t.Setenv("AUTH_AUDIENCE", idp.audience)
	t.Setenv("AUTH_CLIENT_ID", idp.audience)
	t.Setenv("AUTH_JWT_HEADER", "")
	t.Setenv("AUTH_ADMIN_ID", adminID)
	// EXTERNAL_URL set marks direct exposure, which (with the default
	// Authorization header) is what enables client-side PKCE.
	t.Setenv("EXTERNAL_URL", "https://quake.example.com")

	jwt, err := access.InitJWT(context.Background())
	if err != nil {
		t.Fatalf("InitJWT: %v", err)
	}
	if jwt == nil {
		t.Fatal("InitJWT returned nil; expected a configured verifier")
	}
	auth, err := access.InitAuth()
	if err != nil {
		t.Fatalf("InitAuth: %v", err)
	}
	gate := access.NewGate(jwt, access.NewIdentity(), auth, access.NewBlocklist())
	if !gate.PKCEEnabled() {
		t.Fatal("expected PKCE to be enabled for the direct-exposure topology")
	}
	return &nexusApp{access: gate}
}

func postSession(t *testing.T, app *nexusApp, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/rcon/session", strings.NewReader(body))
	rr := httptest.NewRecorder()
	app.handleRconSession(rr, req)
	return rr
}

func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}
