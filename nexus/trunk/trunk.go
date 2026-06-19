// Package trunk provides a port-multiplexed binary tunnel that bridges
// modern web clients to UDP-based game servers.
// One [Trunk] owns the runtime state — VirtualIP pool, active session
// registry, and shared per-session callbacks — and produces per-client
// [Session] instances via [Trunk.NewSession].
//
// # Wire format
//
// Every tunnel message is a binary frame with a 2-byte big-endian port
// header followed by the payload:
//
//	byte 0    byte 1    byte 2 …
//	+---------+---------+----------+
//	| port (uint16, BE) | payload  |
//	+---------+---------+----------+
//
// Port 0 is the control channel; non-zero values are UDP destination ports
// on the backend. Control frames are passed to the application via the
// callback registered with [WithControlHandler] instead of being forwarded
// over UDP. The application sends control frames in either direction via
// [Session.SendControl].
//
// # Usage
//
//	tk := trunk.New(
//		trunk.WithControlHandler(handleControl),
//	)
//
//	http.HandleFunc("/connect", func(w http.ResponseWriter, r *http.Request) {
//		ws, err := websocket.Upgrader.Upgrade(w, r, nil)
//		if err != nil {
//			return
//		}
//		s, err := tk.NewSession(websocket.New(ws), sourceKey)
//		if err != nil {
//			_ = ws.Close()
//			return
//		}
//		s.Run() // blocks until the session ends
//	})
package trunk

import (
	"net"
	"os"
	"sync"
	"time"
)

// Trunk is the per-process runtime: owns the VirtualIP allocator, the
// active-session registry, and the configuration shared by every Session
// it creates. Construct one at startup with [New], accept sessions via
// [Trunk.NewSession], and inspect or evict them via [Trunk.Sessions] and
// [Trunk.EndSession].
//
// Severity logging (warn/debug) flows through log/slog's default logger.
type Trunk struct {
	allocator *virtualIPAllocator
	registry  *registry

	onCtrlFrame ControlHandler
	allowPort   PortFilter
	debugRelay  bool // mirrors DEBUG_RELAY=1 env, read once at construction
}

// Option configures a Trunk at construction time.
type Option func(*config)

type config struct {
	serverIP    net.IP
	onCtrlFrame ControlHandler
	allowPort   PortFilter
}

// WithServerIP sets the dedicated server's IP — excluded from the VirtualIP
// pool so allocations never collide with the game-server's loopback address.
// Default is 127.0.0.1.
func WithServerIP(ip net.IP) Option {
	return func(c *config) { c.serverIP = ip.To4() }
}

// WithControlHandler wires the application callback for control-channel
// frames (port 0). Without this, control frames are silently dropped.
func WithControlHandler(fn ControlHandler) Option {
	return func(c *config) { c.onCtrlFrame = fn }
}

// WithPortFilter installs a UDP port allowlist callback. Returning false
// drops the inbound frame instead of forwarding it. Without this, every
// valid destination port is allowed.
func WithPortFilter(fn PortFilter) Option {
	return func(c *config) { c.allowPort = fn }
}

// New builds a Trunk ready to accept sessions. All settings are optional;
// defaults: server IP 127.0.0.1, no control-frame handler, no port filter.
func New(opts ...Option) *Trunk {
	cfg := config{serverIP: net.ParseIP("127.0.0.1").To4()}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &Trunk{
		allocator:   newAllocator(cfg.serverIP),
		registry:    newRegistry(),
		onCtrlFrame: cfg.onCtrlFrame,
		allowPort:   cfg.allowPort,
		debugRelay:  os.Getenv("DEBUG_RELAY") == "1",
	}
}

// NewSession accepts an upgraded transport and runs a per-client tunnel as
// a Session. The returned *Session must have Run() called on it (typically
// in the same goroutine that handled the upgrade); Run blocks until the
// session ends, at which point the VirtualIP slot is released and the
// session is removed from the registry.
//
// sourceKey is the input to deterministic VirtualIP allocation: the same
// IP always hashes to the same 127.x.x.x candidate (subject to collision
// walking). The transport's [Transport.Name] supplies the label surfaced
// via [SessionInfo.Transport].
func (t *Trunk) NewSession(xport Transport, sourceKey string) (*Session, error) {
	return newSession(t, xport, sourceKey)
}

// Sessions returns a point-in-time snapshot of every active session.
func (t *Trunk) Sessions() []SessionInfo {
	return t.registry.snapshot()
}

// EndSession terminates the active session whose VirtualIP matches. Used
// by ban enforcement: callers add the source IP to their own ban list,
// then invoke this to evict the live session.
func (t *Trunk) EndSession(virtualIP [4]byte) {
	t.registry.endSession(virtualIP)
}

// SessionByVirtualIP returns the live session with the given VirtualIP,
// or nil if none. Returned sessions remain valid only as long as they are
// still active; callers acting on the result (e.g. [Session.SendControl])
// should treat operations as best-effort.
func (t *Trunk) SessionByVirtualIP(virtualIP [4]byte) *Session {
	return t.registry.sessionByVirtualIP(virtualIP)
}

// SessionInfo is a point-in-time snapshot of an active session. Carries
// only fields the trunk itself controls; application metadata (auth
// identity, etc.) is the caller's concern and lives outside trunk.
//
// VirtualIP is the raw 4 bytes of the trunk-allocated 127.x.x.x address;
// callers can render it via net.IP(vip[:]).String().
type SessionInfo struct {
	SourceKey   string
	VirtualIP   [4]byte
	Transport   string
	ConnectedAt time.Time
	// ActiveServerPort is the last non-control destination port this client sent
	// to. It is transport-agnostic route state, not a guarantee that the server
	// still considers the player fully connected.
	ActiveServerPort int
}

// registry tracks active *Session instances. Internal to trunk; the Trunk
// exposes Sessions() and EndSession() as the public read/control surface.
// Safe for concurrent use.
type registry struct {
	mu       sync.RWMutex
	sessions map[*Session]struct{}
}

func newRegistry() *registry {
	return &registry{sessions: make(map[*Session]struct{})}
}

func (r *registry) add(s *Session) {
	r.mu.Lock()
	r.sessions[s] = struct{}{}
	r.mu.Unlock()
}

func (r *registry) remove(s *Session) {
	r.mu.Lock()
	delete(r.sessions, s)
	r.mu.Unlock()
}

func (r *registry) snapshot() []SessionInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.sessions) == 0 {
		return nil
	}
	out := make([]SessionInfo, 0, len(r.sessions))
	for s := range r.sessions {
		out = append(out, sessionInfoOf(s))
	}
	return out
}

func (r *registry) endSession(target [4]byte) {
	if match := r.sessionByVirtualIP(target); match != nil {
		match.End()
	}
}

func (r *registry) sessionByVirtualIP(target [4]byte) *Session {
	if target[0] == 0 {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for s := range r.sessions {
		if s.virtualIP == target {
			return s
		}
	}
	return nil
}

func sessionInfoOf(s *Session) SessionInfo {
	return SessionInfo{
		SourceKey:        s.sourceKey,
		VirtualIP:        s.virtualIP,
		Transport:        s.xport.Name(),
		ConnectedAt:      s.connectedAt,
		ActiveServerPort: int(s.lastServerPort.Load()),
	}
}
