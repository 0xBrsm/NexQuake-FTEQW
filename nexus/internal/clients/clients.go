// Package clients tracks live Nexus client presence.
//
// It joins access-layer caller metadata with trunk-layer session state so
// consumers can list, inspect, and disconnect connected clients without
// owning transport details or HTTP identity policy.
package clients

import (
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/0xBrsm/NexQuake/nexus/internal/access"
	"github.com/0xBrsm/NexQuake/nexus/trunk"
)

// sessionLookup is the subset of *trunk.Trunk that the client registry needs.
type sessionLookup interface {
	Sessions() []trunk.SessionInfo
	SessionByVirtualIP(virtualIP [4]byte) *trunk.Session
}

// Connection is the runtime view of one connected client: access identity,
// trunk-allocated virtual address, transport family, and - when available -
// the resolved active server port when trunk has observed client traffic to
// one.
//
// VirtualAddr is the dotted-quad presentation form of the trunk-allocated
// VirtualIP (which is [4]byte at the trunk layer).
type Connection struct {
	access.Client              // source IP + identity
	VirtualAddr      string    `json:"nqip"`
	Transport        string    `json:"transport,omitempty"`
	ActiveServerPort int       `json:"server_port,omitempty"`
	ActiveServerHost string    `json:"server_host,omitempty"`
	ConnectedAt      time.Time `json:"connected_at"`

	// trunk is captured at hydrate time so the connection can act on its own
	// live session (Disconnect). Unexported and ignored by encoding/json.
	trunk sessionLookup
}

// Disconnect ends this client's trunk session, optionally pushing a final
// control-channel payload first (e.g. an admin-framed quit command - the
// framing is the caller's concern). Returns false if no live session matches.
func (c Connection) Disconnect(payload []byte) bool {
	sess := c.liveSession()
	if sess == nil {
		return false
	}
	if len(payload) > 0 {
		_ = sess.TrySendControl(payload)
	}
	sess.End()
	return true
}

// PushControl sends a one-shot control-channel payload to this connection's
// active trunk session without disconnecting. Returns false if no live
// session matches. Used by admin pushes that aren't tied to teardown
// (e.g. a "you just authenticated" echo on rcon-login completion).
func (c Connection) PushControl(payload []byte) bool {
	sess := c.liveSession()
	if sess == nil {
		return false
	}
	return sess.TrySendControl(payload)
}

// liveSession resolves this connection's currently-active trunk session by
// VirtualIP, or nil if the connection's trunk reference is missing, the
// VirtualAddr can't be parsed, or no session is registered. Lookup is lazy
// so a freshly-stale Connection just returns nil.
func (c Connection) liveSession() *trunk.Session {
	if c.trunk == nil {
		return nil
	}
	vip, ok := parseVirtualAddr(c.VirtualAddr)
	if !ok {
		return nil
	}
	return c.trunk.SessionByVirtualIP(vip)
}

// Registry tracks who is currently connected. It stores access identities
// recorded at /connect by VirtualIP and joins them with live trunk sessions on
// demand to produce full [Connection] views.
//
// ActiveServerPort on [Connection] is populated from trunk route observations.
// ActiveServerHost is reserved for future orch enrichment and is empty today.
type Registry struct {
	trunk sessionLookup

	mu      sync.RWMutex
	clients map[[4]byte]access.Client // VirtualIP -> Client recorded at /connect
}

// NewRegistry constructs a *Registry joined to the given trunk handle. trunk may
// be nil for tests that exercise only the identity-tracking surface.
func NewRegistry(t sessionLookup) *Registry {
	return &Registry{
		trunk:   t,
		clients: make(map[[4]byte]access.Client),
	}
}

// Attach records client metadata for s and returns a detach function. Call from
// /connect after trunk accepts the session, then defer the returned function
// until the tunnel ends.
func (r *Registry) Attach(s *trunk.Session, client access.Client) func() {
	if s == nil {
		return func() {}
	}
	vip := s.VirtualIP()
	r.Add(vip, client)
	var once sync.Once
	return func() {
		once.Do(func() { r.Remove(vip) })
	}
}

// Add records the client for virtualIP. Empty VirtualIP is a no-op.
func (r *Registry) Add(virtualIP [4]byte, client access.Client) {
	if r == nil || virtualIP[0] == 0 {
		return
	}
	r.mu.Lock()
	r.clients[virtualIP] = client
	r.mu.Unlock()
}

// Remove drops the recorded client for virtualIP.
func (r *Registry) Remove(virtualIP [4]byte) {
	if r == nil || virtualIP[0] == 0 {
		return
	}
	r.mu.Lock()
	delete(r.clients, virtualIP)
	r.mu.Unlock()
}

// List returns the joined view of every currently connected client, sorted by
// VirtualIP for stable display order. Always non-nil.
func (r *Registry) List() []Connection {
	out := make([]Connection, 0)
	if r == nil || r.trunk == nil {
		return out
	}
	snaps := r.trunk.Sessions()
	slices.SortFunc(snaps, func(a, b trunk.SessionInfo) int {
		return slices.Compare(a.VirtualIP[:], b.VirtualIP[:])
	})
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, s := range snaps {
		out = append(out, hydrateConnection(r.clients, s, r.trunk))
	}
	return out
}

// ByVirtualAddr returns the active connection whose VirtualAddr matches the
// given dotted-quad string, or (zero, false) if none. The string is the same
// form admin sees on the wire.
func (r *Registry) ByVirtualAddr(virtualAddr string) (Connection, bool) {
	if r == nil || r.trunk == nil {
		return Connection{}, false
	}
	vip, ok := parseVirtualAddr(virtualAddr)
	if !ok {
		return Connection{}, false
	}
	snaps := r.trunk.Sessions()
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, s := range snaps {
		if s.VirtualIP == vip {
			return hydrateConnection(r.clients, s, r.trunk), true
		}
	}
	return Connection{}, false
}

func hydrateConnection(clients map[[4]byte]access.Client, s trunk.SessionInfo, t sessionLookup) Connection {
	cl := clients[s.VirtualIP]
	if cl.SourceIP == "" {
		cl.SourceIP = s.SourceKey
	}
	return Connection{
		Client:           cl,
		VirtualAddr:      netip.AddrFrom4(s.VirtualIP).String(),
		Transport:        s.Transport,
		ActiveServerPort: s.ActiveServerPort,
		ConnectedAt:      s.ConnectedAt,
		trunk:            t,
	}
}

func parseVirtualAddr(virtualAddr string) ([4]byte, bool) {
	addr, err := netip.ParseAddr(strings.TrimSpace(virtualAddr))
	if err != nil {
		return [4]byte{}, false
	}
	addr = addr.Unmap()
	if !addr.Is4() {
		return [4]byte{}, false
	}
	return addr.As4(), true
}
