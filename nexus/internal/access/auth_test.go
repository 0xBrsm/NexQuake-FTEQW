package access

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthorize_DeniesWithoutCredentials(t *testing.T) {
	auth := &Auth{rconPassword: "secret"}
	if auth.Authorize(nil, "") {
		t.Fatalf("expected unauthorized with no credentials")
	}
}

func TestAuthorize_NilAuthDenies(t *testing.T) {
	var auth *Auth
	if auth.Authorize(map[string]any{"email": "admin@example.com"}, "secret") {
		t.Fatalf("expected nil auth to deny access")
	}
}

func TestAuthorize_AdmitsMatchingPassword(t *testing.T) {
	auth := &Auth{rconPassword: "secret"}
	if !auth.Authorize(nil, "secret") {
		t.Fatalf("expected authorization with matching password")
	}
}

func TestAuthorize_RejectsWrongPassword(t *testing.T) {
	auth := &Auth{rconPassword: "secret"}
	if auth.Authorize(nil, "nope") {
		t.Fatalf("expected denial for wrong password")
	}
}

func TestAuthorize_RejectsEmptyToken(t *testing.T) {
	auth := &Auth{rconPassword: "secret"}
	if auth.Authorize(nil, "") {
		t.Fatal("expected denial for empty token")
	}
}

func TestAuthorize_RconPasswordNotValidAsBearer(t *testing.T) {
	a := &Auth{rconPassword: "testpw"}

	// Rcon token extracted from Bearer header is "" — not the password string
	if a.Authorize(nil, "") {
		t.Fatalf("expected Authorize=false when no Rcon token is present")
	}
}

func TestParseAdminPolicy_Rules(t *testing.T) {
	mode, got, err := parseAdminPolicy("group:admin, role:ops, email:Steve@OpenAI.com, custom:x, email:steve@openai.com")
	if err != nil {
		t.Fatalf("parseAdminPolicy() unexpected error: %v", err)
	}
	if mode != adminMatch {
		t.Fatalf("parseAdminPolicy() mode = %d, want adminMatch", mode)
	}

	want := map[string][]string{
		"group":  {"admin"},
		"role":   {"ops"},
		"custom": {"x"},
		"email":  {"steve@openai.com", "steve@openai.com"},
	}
	if len(got) != len(want) {
		t.Fatalf("parseAdminPolicy() len = %d, want %d (%#v)", len(got), len(want), got)
	}
	for k, expected := range want {
		gotVals, ok := got[k]
		if !ok {
			t.Fatalf("parseAdminPolicy() missing key %q in %#v", k, got)
		}
		if len(gotVals) != len(expected) {
			t.Fatalf("parseAdminPolicy() key %q len=%d want %d (%#v)", k, len(gotVals), len(expected), gotVals)
		}
		for i := range expected {
			if gotVals[i] != expected[i] {
				t.Fatalf("parseAdminPolicy() key %q value[%d]=%q want %q", k, i, gotVals[i], expected[i])
			}
		}
	}
}

func TestParseAdminPolicy_BlankDeniesAll(t *testing.T) {
	for _, raw := range []string{"", "   ", " , ,"} {
		mode, rules, err := parseAdminPolicy(raw)
		if err != nil {
			t.Fatalf("parseAdminPolicy(%q) unexpected error: %v", raw, err)
		}
		if mode != adminDeny || rules != nil {
			t.Fatalf("parseAdminPolicy(%q) = (%d, %#v), want (adminDeny, nil)", raw, mode, rules)
		}
	}
}

func TestParseAdminPolicy_AnySentinel(t *testing.T) {
	for _, raw := range []string{"any", "ANY", " Any "} {
		mode, rules, err := parseAdminPolicy(raw)
		if err != nil {
			t.Fatalf("parseAdminPolicy(%q) unexpected error: %v", raw, err)
		}
		if mode != adminAny || rules != nil {
			t.Fatalf("parseAdminPolicy(%q) = (%d, %#v), want (adminAny, nil)", raw, mode, rules)
		}
	}
}

func TestParseAdminPolicy_AllIsNotASentinel(t *testing.T) {
	// "all" is not a recognized keyword — it's a malformed rule (no key:value).
	if _, _, err := parseAdminPolicy("all"); err == nil {
		t.Fatal(`parseAdminPolicy("all") expected an error; only "any" is the wildcard`)
	}
}

func TestParseAdminPolicy_MalformedIsError(t *testing.T) {
	// A bare token missing its key:value form must fail loudly, not silently
	// collapse to deny-all (which would lock everyone out on a typo).
	for _, raw := range []string{"bad", "email:a@b.com, oops", "groups:", ":x", "any,email:a@b.com"} {
		if _, _, err := parseAdminPolicy(raw); err == nil {
			t.Fatalf("parseAdminPolicy(%q) expected an error, got nil", raw)
		}
	}
}

func TestMatchesAdminRules(t *testing.T) {
	claims := map[string]any{
		"email":  "steve@openai.com",
		"groups": []any{"players", "admin"},
		"roles":  []any{"viewer", "ops"},
	}

	if !matchesAdminRules(claims, map[string][]string{"email": {"steve@openai.com"}}) {
		t.Fatalf("expected email rule to pass")
	}
	if !matchesAdminRules(claims, map[string][]string{"groups": {"admin"}}) {
		t.Fatalf("expected group rule to pass")
	}
	if !matchesAdminRules(claims, map[string][]string{"roles": {"ops"}}) {
		t.Fatalf("expected role rule to pass")
	}
	if matchesAdminRules(claims, map[string][]string{"groups": {"nope"}}) {
		t.Fatalf("expected non-matching group to fail")
	}
}

func TestBrowserConfig_NilWhenUnconfigured(t *testing.T) {
	var j *JWTVerifier
	if j.BrowserConfig("openid") != nil {
		t.Fatal("expected nil BrowserConfig for nil verifier")
	}
	var g *Gate
	if g.OIDCBrowserConfig() != nil {
		t.Fatal("expected nil OIDCBrowserConfig for nil gate")
	}
	// A gate wired without OIDC must also report nil so the client keeps its
	// password / edge-gated login paths.
	auth, err := InitAuth()
	if err != nil {
		t.Fatalf("InitAuth: %v", err)
	}
	gate := NewGate(nil, NewIdentity(), auth, NewBlocklist())
	if gate.OIDCBrowserConfig() != nil {
		t.Fatal("expected nil OIDCBrowserConfig when JWT is not configured")
	}
}

func TestBrowserConfig_ExposesPublicParams(t *testing.T) {
	j := &JWTVerifier{issuer: "https://idp.example.com", clientID: "abc123", headerName: "Authorization", directExposure: true}
	got := j.BrowserConfig("openid email")
	want := map[string]any{
		"issuer":   "https://idp.example.com",
		"clientId": "abc123",
		"scopes":   "openid email",
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("BrowserConfig()[%q] = %v, want %v", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("BrowserConfig() has %d keys, want %d (%#v)", len(got), len(want), got)
	}
}

func TestBrowserConfig_NilWithFrontInjectedHeader(t *testing.T) {
	// A custom JWT header means a fronting access gate supplies the token and
	// Parse ignores Authorization: Bearer — so PKCE must be disabled and the
	// client falls back to the edge-gated popup login.
	j := &JWTVerifier{issuer: "https://vergent.cloudflareaccess.com", clientID: "aud", headerName: "Cf-Access-Jwt-Assertion", directExposure: true}
	if j.BrowserConfig("openid profile email") != nil {
		t.Fatal("expected nil BrowserConfig when a front-injected JWT header is configured")
	}
}

func TestBrowserConfig_NilWithoutExternalURL(t *testing.T) {
	// Without EXTERNAL_URL we're behind a front — which may inject the JWT via the
	// standard Authorization: Bearer, indistinguishable from direct exposure on
	// the header axis. PKCE must stay off even with the default Authorization
	// header; EXTERNAL_URL is the disambiguator.
	j := &JWTVerifier{issuer: "https://idp.example.com", clientID: "abc123", headerName: "Authorization"}
	if j.BrowserConfig("openid profile email") != nil {
		t.Fatal("expected nil BrowserConfig without EXTERNAL_URL (behind a front)")
	}
}

func TestLoginScopes_DerivedFromAdminRules(t *testing.T) {
	// Base set is always requested — spec-safe and powers audit identity.
	base := "openid profile email"

	if got := (*Auth)(nil).LoginScopes(); got != base {
		t.Fatalf("nil Auth scopes = %q, want %q", got, base)
	}
	if got := (&Auth{adminMode: adminDeny}).LoginScopes(); got != base {
		t.Fatalf("no rules scopes = %q, want %q", got, base)
	}
	// Gating on email/sub does not pull in groups.
	emailRule := &Auth{adminMode: adminMatch, adminRules: map[string][]string{"email": {"a@b.com"}}}
	if got := emailRule.LoginScopes(); got != base {
		t.Fatalf("email-rule scopes = %q, want %q", got, base)
	}
	// Gating on groups adds the groups scope.
	groupRule := &Auth{adminMode: adminMatch, adminRules: map[string][]string{"groups": {"admins"}}}
	if got := groupRule.LoginScopes(); got != base+" groups" {
		t.Fatalf("groups-rule scopes = %q, want %q", got, base+" groups")
	}
}

func TestAuthorize_AdminClaimsCheck(t *testing.T) {
	claims := map[string]any{
		"email":  "steve@openai.com",
		"groups": []any{"players", "admin"},
	}

	// adminAny (AUTH_ADMIN_ID=any): every verified JWT grants admin
	anyJWT := &Auth{adminMode: adminAny}
	if !anyJWT.Authorize(claims, "") {
		t.Fatal("expected adminAny to grant admin for any verified JWT")
	}

	// rule-based: only matching claims grant admin
	ruleBased := &Auth{adminMode: adminMatch, adminRules: map[string][]string{"groups": {"admin"}}}
	if !ruleBased.Authorize(claims, "") {
		t.Fatal("expected rule-based admin match to pass")
	}

	// adminDeny (AUTH_ADMIN_ID unset, the default): no JWT grants admin
	denyAll := &Auth{adminMode: adminDeny}
	if denyAll.Authorize(claims, "") {
		t.Fatal("expected deny-all default to deny admin for a verified JWT")
	}
}

// Parse's credential precedence and the nq_session cookie branch (the
// browser hybrid-BFF login stores the verified id_token there). Uses the
// idpStub harness from fakes_test.go.

func TestParse_AcceptsNQSessionCookie(t *testing.T) {
	idp := newIDPStub(t, "nq-aud")
	j := initVerifier(t, idp, "")

	req := httptest.NewRequest(http.MethodPost, "/rcon", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: idp.token(t)})

	claims := j.Parse(req)
	if claims == nil {
		t.Fatal("expected claims from a valid nq_session cookie")
	}
	if claims["email"] != "admin@example.com" {
		t.Fatalf("email claim = %v, want admin@example.com", claims["email"])
	}
}

func TestParse_BearerHeaderTakesPrecedenceOverCookie(t *testing.T) {
	idp := newIDPStub(t, "nq-aud")
	j := initVerifier(t, idp, "")

	req := httptest.NewRequest(http.MethodPost, "/rcon", nil)
	req.Header.Set("Authorization", "Bearer "+idp.token(t))
	// A garbage cookie must be ignored when a valid Bearer header is present.
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "not-a-token"})

	if claims := j.Parse(req); claims == nil {
		t.Fatal("expected the valid Bearer header to be used over the bad cookie")
	}
}

func TestParse_IgnoresCookieWithFrontInjectedHeader(t *testing.T) {
	// With a custom JWT header, a fronting gate owns auth; our cookie must never
	// be consulted (it would let any holder of a stale cookie bypass the front).
	idp := newIDPStub(t, "nq-aud")
	j := initVerifier(t, idp, "Cf-Access-Jwt-Assertion")

	req := httptest.NewRequest(http.MethodPost, "/rcon", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: idp.token(t)})

	if claims := j.Parse(req); claims != nil {
		t.Fatal("expected the nq_session cookie to be ignored when a custom header is configured")
	}
}

func TestParse_RejectsInvalidCookie(t *testing.T) {
	idp := newIDPStub(t, "nq-aud")
	j := initVerifier(t, idp, "")

	req := httptest.NewRequest(http.MethodPost, "/rcon", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "garbage.not.jwt"})

	if claims := j.Parse(req); claims != nil {
		t.Fatal("expected nil claims for an unverifiable cookie")
	}
}

func TestParse_NilWithoutCredentials(t *testing.T) {
	idp := newIDPStub(t, "nq-aud")
	j := initVerifier(t, idp, "")

	req := httptest.NewRequest(http.MethodPost, "/rcon", nil)
	if claims := j.Parse(req); claims != nil {
		t.Fatal("expected nil claims when no token or cookie is present")
	}
}
