package trunk

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	keepalivePingInterval = 25 * time.Second

	// sessionDrainDeadline bounds how long [Session.End] waits for the
	// writeLoop to flush queued frames before forcibly closing the
	// transport. Long enough to drain a typical handful of frames
	// queued just before eviction; short enough that a stalled client
	// can't hold a teardown indefinitely.
	sessionDrainDeadline = 1 * time.Second
)

// Transport family names returned by [Transport.Name] and used in connection
// logs and SessionInfo.Transport (and thus rcon client.list). These are the
// canonical display strings — NOT the lowercase /start JSON keys.
const (
	TransportWebSocket    = "WebSocket"
	TransportWebTransport = "WebTransport"
)

// Transport is a bidirectional binary-frame tunnel between trunk and a
// single client. Adapters wrap the concrete connection type; the [Session]'s
// read/write loops are transport-agnostic.
type Transport interface {
	// Name returns a human-readable label for the transport family
	// ("WebSocket", "WebRTC", ...). Class-level: every instance from
	// the same adapter returns the same string. Surfaced via SessionInfo.Transport.
	Name() string
	// ReadFrame blocks until a binary frame is received. On connection close
	// or error it returns a non-nil error; the Session ends. Non-binary
	// messages (e.g. WS control frames) are skipped internally.
	ReadFrame() ([]byte, error)
	// WriteFrame sends a binary frame with a short write deadline.
	WriteFrame([]byte) error
	// Ping sends an application-level keepalive. Transports with native
	// connection-level keepalive may return nil.
	Ping() error
	// Close tears down the underlying connection.
	Close() error
}

// ControlHandler is an application callback invoked for every incoming
// control-channel frame (port 0). The handler may call [Session.SendControl]
// to send a reply (or any other control frame) — there's no return-value
// reply path. The payload slice is only valid for the duration of the
// call; copy it if retained.
type ControlHandler func(s *Session, payload []byte)

// PortFilter gates outbound UDP forwarding. Called for every incoming
// non-control frame before the payload is written to the local UDP socket;
// return false to drop the frame. When unset, all valid destination ports
// are allowed.
type PortFilter func(port int) bool

// Session is a single client's tunnel↔UDP bridge. It owns one [Transport]
// and one UDP socket bound to a unique virtual loopback IP. Three goroutines
// run concurrently while [Session.Run] is active: readLoop, writeLoop, and
// udpReadLoop; all exit when the session ends.
type Session struct {
	xport   Transport
	udpConn *net.UDPConn
	tx      chan []byte // outbound tunnel frames; bounded to 1024 entries

	virtualIP      [4]byte
	sourceKey      string
	connectedAt    time.Time
	lastServerPort atomic.Int32
	txDropped      atomic.Int64 // outbound frames dropped to a full tx queue (slow client)

	trunk *Trunk // back-reference for shared config + lifecycle teardown

	ctx           context.Context
	cancel        context.CancelFunc
	endOnce       sync.Once
	runStarted    atomic.Bool   // set by Run() before launching goroutines
	writeLoopDone chan struct{} // closed by writeLoop on exit
}

// newSession builds a *Session bound to the given Trunk. Internal — callers
// go through [Trunk.NewSession].
func newSession(t *Trunk, xport Transport, sourceKey string) (*Session, error) {
	ctx, cancel := context.WithCancel(context.Background())

	virtualIP, err := t.allocator.alloc(sourceKey)
	if err != nil {
		cancel()
		return nil, err
	}

	udpConn, err := net.ListenUDP("udp4", listenAddrForClient(virtualIP))
	if err != nil {
		t.allocator.release(virtualIP)
		cancel()
		return nil, fmt.Errorf("listen udp: %w", err)
	}

	s := &Session{
		xport:         xport,
		udpConn:       udpConn,
		tx:            make(chan []byte, 1024),
		virtualIP:     virtualIP,
		sourceKey:     strings.TrimSpace(sourceKey),
		connectedAt:   time.Now(),
		trunk:         t,
		ctx:           ctx,
		cancel:        cancel,
		writeLoopDone: make(chan struct{}),
	}
	t.registry.add(s)
	return s, nil
}

// End tears down the Session: unregisters from the registry, cancels the
// context, drains queued outbound frames so frames pushed via SendControl
// just before eviction are still delivered, then closes the UDP and
// tunnel connections and releases the VirtualIP. Safe to call more than
// once; all work happens at most once.
//
// The drain is bounded by sessionDrainDeadline so a stalled transport
// cannot hold teardown indefinitely. End called before [Session.Run]
// has started skips the drain entirely.
func (s *Session) End() {
	s.endOnce.Do(func() {
		s.trunk.registry.remove(s)
		s.cancel()

		if s.runStarted.Load() {
			select {
			case <-s.writeLoopDone:
			case <-time.After(sessionDrainDeadline + 500*time.Millisecond):
			}
		}

		if s.udpConn != nil {
			_ = s.udpConn.Close()
		}
		if s.xport != nil {
			_ = s.xport.Close()
		}
		s.trunk.allocator.release(s.virtualIP)
		s.virtualIP = [4]byte{}

		// One summary line per session instead of a warn per dropped frame:
		// a full tx queue is client-side congestion (UDP-equivalent loss), so
		// it shouldn't flood the log mid-session, but a session that dropped
		// anything is worth a single note at teardown. No more drops can land
		// after cancel() above, so the count is final here.
		if dropped := s.txDropped.Load(); dropped > 0 {
			slog.Warn(fmt.Sprintf("session %s dropped %d outbound frame(s): tx queue full (slow client)", s.sourceKey, dropped))
		}
	})
}

// Run starts the UDP and tunnel I/O goroutines and blocks until the session
// ends. It calls End before returning. Application protocol — including any
// initial control-channel handshake — is the caller's responsibility; queue
// such frames via [Session.SendControl] before invoking Run, or send them
// from inside the [ControlHandler] callback.
func (s *Session) Run() {
	s.runStarted.Store(true)
	go s.udpReadLoop()
	go s.writeLoop()
	s.readLoop()
	s.End()
}

// SendControl enqueues a control-channel frame (port 0) for delivery. Used
// by both the [ControlHandler] when replying to inbound frames and by
// the application for unsolicited server→client pushes. Returns an error
// if the session has ended before the frame could be queued.
func (s *Session) SendControl(payload []byte) error {
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	frame := buildFrame(controlPort, payload)
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case s.tx <- frame:
		return nil
	}
}

// TrySendControl is the non-blocking SendControl: when the tx queue is full
// (a stalled client) the frame is dropped and false is returned. Use it for
// push-style frames issued from HTTP handlers or the session's own read
// loop, where blocking until the client drains — up to the write/keepalive
// timeout — would park the caller.
func (s *Session) TrySendControl(payload []byte) bool {
	if s.ctx.Err() != nil {
		return false
	}
	select {
	case s.tx <- buildFrame(controlPort, payload):
		return true
	default:
		return false
	}
}

// VirtualIP returns the 4-byte VirtualIP allocated to this session. Returns
// [4]byte{} after the session has ended.
func (s *Session) VirtualIP() [4]byte {
	return s.virtualIP
}

func (s *Session) noteServerRoutePort(port int) {
	if isValidServerPort(port) {
		s.lastServerPort.Store(int32(port))
	}
}
