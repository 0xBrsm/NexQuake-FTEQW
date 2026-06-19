package access

import (
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
)

// Client identifies the caller of a request. ID is an audit-safe label:
// OIDC identity (email, name, or sub), source IP, or "anonymous".
// SourceIP is the resolved network address, independent of any credential.
type Client struct {
	ID       string `json:"identity,omitempty"`
	SourceIP string `json:"source_ip,omitempty"`
}

// clientID computes a stable label for audit logs. Prefers a real
// authenticated identity over source IP.
func clientID(identity, sourceIP string) string {
	if trimmed := strings.TrimSpace(identity); trimmed != "" && !strings.EqualFold(trimmed, "anonymous") {
		return trimmed
	}
	if ip := strings.TrimSpace(sourceIP); ip != "" {
		return ip
	}
	return "anonymous"
}

// ResolveClient determines the full identity of an HTTP request caller: source
// IP from the network layer and display identity from pre-parsed JWT claims.
func (i *Identity) ResolveClient(r *http.Request, claims map[string]any) Client {
	sourceIP := i.ClientSourceIP(r)
	return Client{
		ID:       clientID(identityFromClaims(claims), sourceIP),
		SourceIP: sourceIP,
	}
}

func identityFromClaims(claims map[string]any) string {
	if claims == nil {
		return ""
	}
	for _, key := range []string{"email", "preferred_username", "name", "sub"} {
		if val, ok := claims[key].(string); ok && val != "" {
			return val
		}
	}
	return "oidc-user"
}

// Identity resolves a stable source IP and identity key for any incoming HTTP
// request. It is concerned with who / where a client is, independent of
// whether that client has privileged credentials.
//
// By default the TCP/QUIC remote address is authoritative — Nexus terminates
// its own connections. In the behind-a-front run path (e.g. a Cloudflare
// Tunnel fronting a plain-HTTP Nexus), every remote address is the proxy's, so
// AUTH_CLIENT_IP_HEADER opts in to a trusted header (e.g. "CF-Connecting-IP")
// for the real client IP; without it, source-IP bans would hit the proxy and
// block everyone. When unset or unparseable, resolution falls back to the
// direct remote address.
type Identity struct {
	clientIPHeader string
}

// NewIdentity constructs an [Identity] from the AUTH_CLIENT_IP_HEADER
// environment variable. Safe to call exactly once at startup.
func NewIdentity() *Identity {
	return &Identity{
		clientIPHeader: strings.TrimSpace(os.Getenv("AUTH_CLIENT_IP_HEADER")),
	}
}

// ClientSourceIP returns the external client IP for a request, preferring the
// configured trusted header over the direct connection address. Returns "" if
// neither yields a parseable IP.
func (i *Identity) ClientSourceIP(r *http.Request) string {
	if i != nil && i.clientIPHeader != "" {
		if ip, ok := parseClientIP(r.Header.Get(i.clientIPHeader)); ok {
			return ip.String()
		}
	}
	if ip, ok := parseClientIP(r.RemoteAddr); ok {
		return ip.String()
	}
	return ""
}

// parseClientIP extracts an IP address from a raw header value or remote addr
// string, handling Forwarded/X-Forwarded-For formats and port stripping.
func parseClientIP(raw string) (netip.Addr, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return netip.Addr{}, false
	}

	if comma := strings.IndexByte(value, ','); comma >= 0 {
		value = strings.TrimSpace(value[:comma])
	}
	if k, v, ok := strings.Cut(value, "="); ok && strings.EqualFold(strings.TrimSpace(k), "for") {
		value = strings.TrimSpace(v)
	}
	value = strings.Trim(strings.TrimSpace(value), "\"")
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	ip, err := netip.ParseAddr(strings.Trim(value, "[]"))
	if err != nil {
		return netip.Addr{}, false
	}
	return ip.Unmap(), true
}
