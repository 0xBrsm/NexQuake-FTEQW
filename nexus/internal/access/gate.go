package access

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Request is the access-layer view of one HTTP caller.
type Request struct {
	Client Client
	Claims map[string]any
}

type requestContextKey struct{}

// Gate resolves request callers and answers common access-policy questions
// for HTTP endpoints.
type Gate struct {
	jwt              *JWTVerifier
	identity         *Identity
	auth             *Auth
	blocklist        *Blocklist
	unauthorizedHint string
}

// NewGate wires the dependencies used by Nexus HTTP access checks.
func NewGate(jwt *JWTVerifier, identity *Identity, auth *Auth, blocklist *Blocklist) *Gate {
	return &Gate{
		jwt:              jwt,
		identity:         identity,
		auth:             auth,
		blocklist:        blocklist,
		unauthorizedHint: buildUnauthorizedHint(auth, jwt),
	}
}

// Resolve identifies the caller and verified claims, if any, for r.
func (g *Gate) Resolve(r *http.Request) Request {
	var claims map[string]any
	if g != nil {
		claims = g.jwt.Parse(r)
	}
	var identity *Identity
	if g != nil {
		identity = g.identity
	}
	return Request{
		Client: identity.ResolveClient(r, claims),
		Claims: claims,
	}
}

// Request returns the access request cached on r by [Gate.HTTPGate], falling
// back to resolving directly for tests and call paths that bypass middleware.
func (g *Gate) Request(r *http.Request) Request {
	if req, ok := r.Context().Value(requestContextKey{}).(Request); ok {
		return req
	}
	return g.Resolve(r)
}

// HTTPGate resolves access once for every HTTP request and drops blocklisted
// sources before route handlers run.
func (g *Gate) HTTPGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := g.Resolve(r)
		if g.IsBlocked(req) {
			slog.Warn(fmt.Sprintf("Rejected blocked client source=%q remote=%s", req.Client.SourceIP, r.RemoteAddr))
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		ctx := context.WithValue(r.Context(), requestContextKey{}, req)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Blocklist tracks source IPs barred from future HTTP entry points.
// It is in-memory and intentionally process-local: bans last until restart.
type Blocklist struct {
	mu      sync.RWMutex
	blocked map[string]struct{}
}

// NewBlocklist constructs an empty source-IP blocklist.
func NewBlocklist() *Blocklist {
	return &Blocklist{blocked: make(map[string]struct{})}
}

// Block adds sourceIP to the in-process blocklist. Empty source keys are
// ignored because they cannot be matched reliably on future requests.
func (b *Blocklist) Block(sourceIP string) {
	if sourceIP == "" {
		return
	}
	b.mu.Lock()
	b.blocked[sourceIP] = struct{}{}
	b.mu.Unlock()
}

// IsBlocked reports whether sourceIP is currently blocklisted.
func (b *Blocklist) IsBlocked(sourceIP string) bool {
	if b == nil || sourceIP == "" {
		return false
	}
	b.mu.RLock()
	_, blocked := b.blocked[sourceIP]
	b.mu.RUnlock()
	return blocked
}

// IsBlocked reports whether req's source is currently barred.
func (g *Gate) IsBlocked(req Request) bool {
	return g != nil && g.blocklist.IsBlocked(req.Client.SourceIP)
}

// AuthorizeAdmin reports whether r carries credentials that can use admin RPCs.
func (g *Gate) AuthorizeAdmin(req Request, r *http.Request) bool {
	return g != nil && g.auth.Authorize(req.Claims, AuthorizationToken(r, "Rcon"))
}

// PKCEEnabled reports whether client-side PKCE login is offered — i.e. OIDC is
// configured with the default Authorization header. When false, POST
// /rcon/session has nothing to do and 404s.
func (g *Gate) PKCEEnabled() bool {
	return g != nil && g.jwt.pkceEnabled()
}

// ExchangeCode performs the server-side token exchange for a browser PKCE login
// and returns the verified id_token, its claims, and expiry. See
// [JWTVerifier.ExchangeCode].
func (g *Gate) ExchangeCode(ctx context.Context, code, verifier, redirectURI string) (string, map[string]any, time.Time, error) {
	if g == nil {
		return "", nil, time.Time{}, fmt.Errorf("OIDC not configured")
	}
	return g.jwt.ExchangeCode(ctx, code, verifier, redirectURI)
}

// AuthorizeClaims reports whether verified JWT claims alone grant admin (no RCON
// password involved). Used after a PKCE exchange to decide which outcome to echo
// to the player's console.
func (g *Gate) AuthorizeClaims(claims map[string]any) bool {
	return g != nil && g.auth.Authorize(claims, "")
}

// OIDCBrowserConfig exposes the public OIDC parameters for client-side PKCE
// login, or nil when OIDC is not configured. The requested scopes are derived
// from the admin rules so they cover whatever claims gating depends on. See
// [JWTVerifier.BrowserConfig] and [Auth.LoginScopes].
func (g *Gate) OIDCBrowserConfig() map[string]any {
	if g == nil {
		return nil
	}
	return g.jwt.BrowserConfig(g.auth.LoginScopes())
}

// UnauthorizedHint returns a one-line operator-facing instruction describing
// how to authenticate against this Nexus, derived from what's actually
// configured. Empty string if Gate is nil. Suitable for surfacing alongside a
// 401 unauthorized response so the caller knows what to do next.
//
// Snapshotted at NewGate time since auth config doesn't change at runtime;
// keeps Auth itself free of methods that exist purely for external readers.
func (g *Gate) UnauthorizedHint() string {
	if g == nil {
		return ""
	}
	return g.unauthorizedHint
}

func buildUnauthorizedHint(auth *Auth, jwt *JWTVerifier) string {
	password := auth != nil && auth.rconPassword != ""
	// Only suggest login when a JWT can actually grant admin; with AUTH_ADMIN_ID
	// unset (deny-all) a successful login still authorizes no one.
	jwtAdmin := jwt != nil && auth.AllowsJWTAdmin()
	switch {
	case password && jwtAdmin:
		return "set rcon_password <secret> or run rcon login"
	case password:
		return "set rcon_password <secret>"
	case jwtAdmin:
		return "run rcon login"
	default:
		return "admin auth not configured (set AUTH_RCON_PASSWORD, or AUTH_ISSUER/AUTH_AUDIENCE with AUTH_ADMIN_ID)"
	}
}

// Block adds sourceIP to the shared source blocklist.
func (g *Gate) Block(sourceIP string) {
	if g != nil && g.blocklist != nil {
		g.blocklist.Block(sourceIP)
	}
}
