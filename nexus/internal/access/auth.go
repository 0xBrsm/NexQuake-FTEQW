// Package access owns Nexus HTTP caller identity and authorization policy.
//
// It verifies OIDC JWTs, accepts the optional RCON shared secret for admin
// RPCs, resolves request callers into client identities, and keeps the
// in-process source blocklist consulted by HTTP entry points.
package access

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// SessionCookieName carries the verified OIDC id_token to the browser as an
// httpOnly cookie once a client-side PKCE login completes its server-side token
// exchange (see [JWTVerifier.ExchangeCode]). The raw id_token is the session:
// stateless, survives restarts, and expires with the token. Keeping it httpOnly
// is the point of the exchange hop — the token never enters page JavaScript.
const SessionCookieName = "nq_session"

// SessionCookieMaxLen guards against id_tokens too large to store in one cookie
// (browsers cap a cookie near 4 KiB including attributes).
const SessionCookieMaxLen = 3800

// JWTVerifier verifies OIDC JWTs and extracts their claims. It owns the
// go-oidc dependency so callers can work with plain claim maps. It also
// carries the public parameters a browser needs to mint those JWTs itself
// via client-side Authorization Code + PKCE (see [JWTVerifier.BrowserConfig]).
type JWTVerifier struct {
	verifier       *oidc.IDTokenVerifier
	headerName     string
	issuer         string
	clientID       string
	tokenURL       string
	directExposure bool
	httpClient     *http.Client
}

// InitJWT reads AUTH_ISSUER, AUTH_AUDIENCE, and AUTH_JWT_HEADER to set up
// OIDC JWT verification and returns the verifier. Returns (nil, nil) when
// OIDC is not configured (both ISSUER and AUDIENCE must be set to enable
// it). The combined "Admin access enabled/disabled" summary is logged once
// by main after both InitJWT and InitAuth complete.
//
// AUTH_CLIENT_ID is read for the browser's PKCE login flow only — it doesn't
// affect verification. It defaults to AUTH_AUDIENCE (the common IdP case where
// the id_token's aud is the client id); set it explicitly only when your IdP
// separates the API audience from the client id. The login scopes are not
// configured directly; they're derived from AUTH_ADMIN_ID (see
// [Auth.LoginScopes]).
func InitJWT(ctx context.Context) (*JWTVerifier, error) {
	issuer := strings.TrimSpace(os.Getenv("AUTH_ISSUER"))
	audience := strings.TrimSpace(os.Getenv("AUTH_AUDIENCE"))
	if issuer == "" || audience == "" {
		return nil, nil
	}
	headerName := strings.TrimSpace(os.Getenv("AUTH_JWT_HEADER"))
	if headerName == "" {
		headerName = "Authorization"
	}
	clientID := strings.TrimSpace(os.Getenv("AUTH_CLIENT_ID"))
	if clientID == "" {
		clientID = audience
	}
	// EXTERNAL_URL set marks the direct-exposure topology (this server is its own
	// public identity); the design requires it be UNSET behind a front. It gates
	// client-side PKCE so that a fronting gate which injects the JWT via the
	// standard Authorization: Bearer header (oauth2-proxy, Pomerium, Envoy) — not
	// only via a custom header like Cf-Access-Jwt-Assertion — still disables PKCE.
	directExposure := strings.TrimSpace(os.Getenv("EXTERNAL_URL")) != ""
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, err
	}
	return &JWTVerifier{
		verifier:       provider.Verifier(&oidc.Config{ClientID: audience}),
		headerName:     headerName,
		issuer:         issuer,
		clientID:       clientID,
		tokenURL:       provider.Endpoint().TokenURL,
		directExposure: directExposure,
		httpClient:     &http.Client{Timeout: 15 * time.Second},
	}, nil
}

// BrowserConfig returns the public OIDC parameters a browser needs to run a
// client-side Authorization Code + PKCE login: the issuer (for endpoint
// discovery), the public client_id, and the scopes to request (derived by the
// caller from the admin rules — see [Auth.LoginScopes]). Keys are JSON-shaped
// for direct inclusion in the /start client config; none of these values are
// secret.
//
// Returns nil — disabling PKCE so the client uses password or edge-gated login
// — unless this is the direct-exposure topology: EXTERNAL_URL is set (the server
// is its own public identity) AND AUTH_JWT_HEADER is the default Authorization.
//
// Both conditions are required because the header alone is ambiguous. A front
// may inject the JWT via a custom header (e.g. Cf-Access-Jwt-Assertion) OR via
// the standard Authorization: Bearer (oauth2-proxy, Pomerium, Envoy); the latter
// looks identical to direct-exposure on the header axis. EXTERNAL_URL is the
// disambiguator: it is set only when standing alone and must be unset behind a
// front. Behind any front, PKCE stays off and Nexus is verify-only — the front
// drives login via the GET /rcon popup and supplies the JWT it already holds.
// pkceEnabled reports whether client-side PKCE login should be offered: the
// direct-exposure topology (EXTERNAL_URL set) with the default Authorization
// header. See [JWTVerifier.BrowserConfig] for the rationale. Cheap: a constant
// predicate, safe on a nil receiver.
func (j *JWTVerifier) pkceEnabled() bool {
	return j != nil && j.directExposure && strings.EqualFold(j.headerName, "Authorization")
}

func (j *JWTVerifier) BrowserConfig(scopes string) map[string]any {
	if !j.pkceEnabled() {
		return nil
	}
	return map[string]any{
		"issuer":   j.issuer,
		"clientId": j.clientID,
		"scopes":   scopes,
	}
}

// AuthorizationToken extracts the credential value for scheme from the
// standard Authorization header. Scheme match is case-insensitive (RFC 7235).
// Returns "" if the header is absent, empty, or carries a different scheme.
func AuthorizationToken(r *http.Request, scheme string) string {
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	prefix := scheme + " "
	if len(raw) <= len(prefix) || !strings.EqualFold(raw[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(raw[len(prefix):])
}

// Parse verifies the JWT in r and returns the decoded claims, or nil if no
// valid token is present or OIDC is not configured.
func (j *JWTVerifier) Parse(r *http.Request) map[string]any {
	if j == nil {
		return nil
	}
	raw := r.Header.Get(j.headerName)
	if strings.EqualFold(j.headerName, "Authorization") {
		if token := AuthorizationToken(r, "Bearer"); token != "" {
			raw = token
		} else if c, err := r.Cookie(SessionCookieName); err == nil && c.Value != "" {
			// Hybrid-BFF browser login stores the verified id_token in an
			// httpOnly nq_session cookie (set by [JWTVerifier.ExchangeCode]'s
			// caller); treat it as the bearer credential. Only consulted on the
			// default Authorization header — a custom front-injected header
			// never carries our cookie.
			raw = c.Value
		}
	}
	if raw == "" {
		return nil
	}
	token, err := j.verifier.Verify(r.Context(), raw)
	if err != nil {
		slog.Debug(fmt.Sprintf("JWT verification failed: %v", err))
		return nil
	}
	var claims map[string]any
	if err := token.Claims(&claims); err != nil {
		slog.Debug(fmt.Sprintf("Failed to extract JWT claims: %v", err))
		return nil
	}
	return claims
}

// ExchangeCode completes the server-side half of a browser client-side PKCE
// login (hybrid BFF). It POSTs the authorization code and PKCE verifier to the
// IdP token endpoint as a public client (no secret — the verifier is the
// proof), then verifies the returned id_token against the same policy as
// [JWTVerifier.Parse]. Browsers can't call the token endpoint directly when the
// IdP omits CORS on it (e.g. Cloudflare Access), so Nexus performs this one hop
// on the browser's behalf — server-to-server, where CORS does not apply — and
// returns the verified id_token plus its expiry for the caller to store in an
// httpOnly session cookie.
//
// redirectURI must equal the value the browser sent to the authorize endpoint
// (its origin + /rcon); the caller derives it server-side rather than trusting
// the client. Only the configured token endpoint is contacted, so a hostile
// caller can't point this at an arbitrary URL.
func (j *JWTVerifier) ExchangeCode(ctx context.Context, code, verifier, redirectURI string) (idToken string, claims map[string]any, expiry time.Time, err error) {
	if j == nil || j.tokenURL == "" {
		return "", nil, time.Time{}, fmt.Errorf("OIDC token endpoint not configured")
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {j.clientID},
		"redirect_uri":  {redirectURI},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, j.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", nil, time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := j.httpClient.Do(req)
	if err != nil {
		return "", nil, time.Time{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", nil, time.Time{}, fmt.Errorf("token endpoint returned HTTP %d", resp.StatusCode)
	}

	var tok struct {
		IDToken          string `json:"id_token"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", nil, time.Time{}, fmt.Errorf("token response was not JSON")
	}
	if tok.Error != "" {
		return "", nil, time.Time{}, fmt.Errorf("token endpoint error: %s", tok.Error)
	}
	if tok.IDToken == "" {
		return "", nil, time.Time{}, fmt.Errorf("token response carried no id_token")
	}

	verified, err := j.verifier.Verify(ctx, tok.IDToken)
	if err != nil {
		return "", nil, time.Time{}, fmt.Errorf("id_token verification failed: %w", err)
	}
	if err := verified.Claims(&claims); err != nil {
		return "", nil, time.Time{}, fmt.Errorf("id_token claims unreadable: %w", err)
	}
	return tok.IDToken, claims, verified.Expiry, nil
}

// Authorize reports whether the caller is permitted to use admin RPCs.
// RCON wins if rconToken matches the configured password; otherwise the
// verified JWT claims are checked against the configured admin policy.
func (a *Auth) Authorize(claims map[string]any, rconToken string) bool {
	if a == nil {
		return false
	}
	if a.rconPassword != "" && rconToken != "" &&
		subtle.ConstantTimeCompare([]byte(rconToken), []byte(a.rconPassword)) == 1 {
		return true
	}
	if claims == nil {
		return false
	}
	switch a.adminMode {
	case adminAny:
		return true
	case adminMatch:
		return matchesAdminRules(claims, a.adminRules)
	default: // adminDeny
		return false
	}
}

// adminMode is the resolved interpretation of AUTH_ADMIN_ID.
type adminMode int

const (
	// adminDeny is the default (AUTH_ADMIN_ID unset or blank): no verified JWT
	// grants admin. Fail closed — admitting "any authenticated user" must be an
	// explicit choice, not a default an operator falls into.
	adminDeny adminMode = iota
	// adminAny ("any"): every verified JWT grants admin. Authorization is
	// delegated entirely to the IdP/edge (who it lets authenticate).
	adminAny
	// adminMatch: a verified JWT grants admin only if its claims match at least
	// one configured rule.
	adminMatch
)

// Auth holds admin access control state: the optional RCON shared secret and
// the policy that determines whether a verified JWT grants admin access.
// Construct with [InitAuth]; a nil *Auth disables all admin access.
//
// adminMode is the resolved AUTH_ADMIN_ID interpretation; adminRules is
// populated only when adminMode is adminMatch.
type Auth struct {
	rconPassword string
	adminMode    adminMode
	adminRules   map[string][]string
}

// AllowsJWTAdmin reports whether a verified JWT can grant admin under the
// current AUTH_ADMIN_ID policy. False when unset/deny-all, so callers can tell
// whether OIDC is an actual admin path or only establishes request identity.
func (a *Auth) AllowsJWTAdmin() bool {
	return a != nil && a.adminMode != adminDeny
}

// HasRconPassword reports whether AUTH_RCON_PASSWORD is configured. Used by
// main to compose the combined admin-access summary line at boot.
func (a *Auth) HasRconPassword() bool {
	return a != nil && a.rconPassword != ""
}

// InitAuth reads AUTH_RCON_PASSWORD and AUTH_ADMIN_ID and returns a
// ready-to-use [Auth]. Returns an error if AUTH_ADMIN_ID is malformed so the
// caller can fail fast rather than boot with a surprising policy. Logs the
// resolved AUTH_ADMIN_ID interpretation at debug level; the user-facing "Admin
// access enabled/disabled" summary is logged once by main after both InitJWT
// and InitAuth complete.
func InitAuth() (*Auth, error) {
	mode, rules, err := parseAdminPolicy(os.Getenv("AUTH_ADMIN_ID"))
	if err != nil {
		return nil, err
	}
	auth := &Auth{
		rconPassword: strings.TrimSpace(os.Getenv("AUTH_RCON_PASSWORD")),
		adminMode:    mode,
		adminRules:   rules,
	}
	switch mode {
	case adminAny:
		slog.Debug("IdP admin mode: AUTH_ADMIN_ID=any; every verified login grants admin")
	case adminMatch:
		slog.Debug("IdP admin mode: AUTH_ADMIN_ID claim matchers in effect")
	default:
		slog.Debug("IdP admin mode: AUTH_ADMIN_ID unset; no login grants admin (set AUTH_ADMIN_ID=any or claim matchers)")
	}
	return auth, nil
}

// LoginScopes returns the OIDC scopes the browser's PKCE login should request
// so the claims this Auth gates on actually arrive in the token. It is derived
// from AUTH_ADMIN_ID rather than configured directly, so an operator never has
// to know which scope name their IdP maps to which claim.
//
// "openid profile email" is always requested: those scopes are spec-defined,
// accepted by every IdP, and the source of the audit log's identity (email /
// preferred_username / name). "groups" is added only when an admin rule keys
// on it — a `groups` scope errors out login on IdPs where it isn't defined, so
// it's requested solely when group-based gating is actually in use. (On IdPs
// that emit groups via token config or a login action rather than a scope, the
// added scope is inert and the claim arrives regardless.)
func (a *Auth) LoginScopes() string {
	scopes := []string{"openid", "profile", "email"}
	if a != nil {
		if _, ok := a.adminRules["groups"]; ok {
			scopes = append(scopes, "groups")
		}
	}
	return strings.Join(scopes, " ")
}

// parseAdminPolicy interprets AUTH_ADMIN_ID into an admin-grant policy:
//
//   - ""            -> adminDeny: no JWT grants admin (fail-closed default).
//   - "any"         -> adminAny: every verified JWT grants admin. Must stand
//     alone — combining it with claim rules is a configuration error.
//   - "key:value,…" -> adminMatch with the parsed rules. Keys and values are
//     lowercased so matching is case-insensitive (emails, group names, role
//     names, any custom claim).
//
// Any other token is a hard error: a malformed rule (e.g. a bare "admins"
// missing its "groups:" key) must fail loudly at startup rather than silently
// collapsing to deny-all and locking everyone out.
func parseAdminPolicy(raw string) (adminMode, map[string][]string, error) {
	var tokens []string
	for _, token := range strings.Split(raw, ",") {
		if token = strings.TrimSpace(token); token != "" {
			tokens = append(tokens, token)
		}
	}
	if len(tokens) == 0 {
		return adminDeny, nil, nil
	}

	rules := make(map[string][]string)
	for _, token := range tokens {
		if strings.EqualFold(token, "any") {
			if len(tokens) != 1 {
				return adminDeny, nil, fmt.Errorf("AUTH_ADMIN_ID: \"any\" must be the only value (cannot combine with claim rules)")
			}
			return adminAny, nil, nil
		}

		key, value, ok := strings.Cut(token, ":")
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.ToLower(strings.TrimSpace(value))
		if !ok || key == "" || value == "" {
			return adminDeny, nil, fmt.Errorf("AUTH_ADMIN_ID: rule %q is not in key:value form (e.g. email:you@example.com or groups:admins); use \"any\" to admit any verified login", token)
		}
		rules[key] = append(rules[key], value)
	}
	return adminMatch, rules, nil
}

func matchesAdminRules(claims map[string]any, rules map[string][]string) bool {
	for key, values := range rules {
		claimVal := claims[key]
		for _, value := range values {
			if containsClaimValue(claimVal, value) {
				return true
			}
		}
	}
	return false
}

func containsClaimValue(val any, target string) bool {
	if val == nil || target == "" {
		return false
	}
	switch v := val.(type) {
	case []interface{}:
		for _, item := range v {
			if s, ok := item.(string); ok && strings.EqualFold(s, target) {
				return true
			}
		}
	case string:
		return strings.EqualFold(v, target)
	}
	return false
}
