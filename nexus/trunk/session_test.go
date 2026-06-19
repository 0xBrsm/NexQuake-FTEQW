package trunk

import (
	"bytes"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTransport records writes and signals when its xport-level close fires.
// Reads block on readCh until Close.
type fakeTransport struct {
	mu       sync.Mutex
	writes   [][]byte
	pings    atomic.Int32
	closed   atomic.Bool
	readCh   chan []byte
	closedAt time.Time

	// writeBlock optionally blocks WriteFrame until released; lets tests
	// inject latency on the drain path.
	writeBlock <-chan struct{}
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{readCh: make(chan []byte)}
}

func (f *fakeTransport) Name() string { return "fake" }

func (f *fakeTransport) ReadFrame() ([]byte, error) {
	b, ok := <-f.readCh
	if !ok {
		return nil, errors.New("transport closed")
	}
	return b, nil
}

func (f *fakeTransport) WriteFrame(b []byte) error {
	if f.writeBlock != nil {
		<-f.writeBlock
	}
	if f.closed.Load() {
		return errors.New("transport closed")
	}
	f.mu.Lock()
	f.writes = append(f.writes, append([]byte(nil), b...))
	f.mu.Unlock()
	return nil
}

func (f *fakeTransport) Ping() error {
	f.pings.Add(1)
	return nil
}

func (f *fakeTransport) Close() error {
	if f.closed.Swap(true) {
		return nil
	}
	f.closedAt = time.Now()
	close(f.readCh)
	return nil
}

func (f *fakeTransport) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.writes)
}

func (f *fakeTransport) writeAt(i int) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i < 0 || i >= len(f.writes) {
		return nil
	}
	return f.writes[i]
}

// --- End() drains queued frames before closing transport -------------------

func TestEnd_DrainsQueuedFramesBeforeClose(t *testing.T) {
	tr := newFakeTransport()
	tk := New()
	sess, err := tk.NewSession(tr, "1.2.3.4")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	runDone := make(chan struct{})
	go func() {
		sess.Run()
		close(runDone)
	}()

	// Wait for Run() to mark the session running so writeLoop is live.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && !sess.runStarted.Load() {
		time.Sleep(time.Millisecond)
	}
	if !sess.runStarted.Load() {
		t.Fatal("Run did not start")
	}

	// Queue an arbitrary control payload, then evict.
	payload := []byte("queued-during-eviction")
	if err := sess.SendControl(payload); err != nil {
		t.Fatalf("SendControl: %v", err)
	}
	tk.EndSession(sess.virtualIP)

	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return after EndSession")
	}

	if !tr.closed.Load() {
		t.Fatal("transport not closed after End")
	}
	if tr.writeCount() == 0 {
		t.Fatal("expected drain to deliver the queued frame; got zero writes")
	}

	// The queued payload should have been flushed before Close.
	last := tr.writeAt(tr.writeCount() - 1)
	want := append([]byte{0, 0}, payload...) // 2-byte control-port header + payload
	if !bytes.Equal(last, want) {
		t.Fatalf("drained frame = %q, want %q", last, want)
	}
}

// --- Trunk.SessionByVirtualIP ----------------------------------------------

func TestSessionByVirtualIP(t *testing.T) {
	tk := New()
	tr := newFakeTransport()
	sess, err := tk.NewSession(tr, "203.0.113.5")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.End()

	got := tk.SessionByVirtualIP(sess.virtualIP)
	if got != sess {
		t.Fatalf("SessionByVirtualIP returned %p, want %p", got, sess)
	}

	if tk.SessionByVirtualIP([4]byte{}) != nil {
		t.Fatal("zero VIP should return nil")
	}
	if tk.SessionByVirtualIP([4]byte{127, 99, 99, 99}) != nil {
		t.Fatal("unknown VIP should return nil")
	}
}

// --- A dead transport reaps the session (server side of the wt fix) ---------

// When a transport dies (its peer closed the connection), ReadFrame returns an
// error, the read loop exits, Run returns via End, and the session is removed
// from the registry with its VirtualIP released. This is the server-side half
// of the WebTransport fall-forward fix: the client now explicitly closes a dead
// WT session, so the nexus must reap it promptly rather than leaving a ghost
// session (and a stuck VirtualIP) contending with the client's replacement.
func TestSession_ReapedWhenTransportCloses(t *testing.T) {
	tr := newFakeTransport()
	tk := New()
	sess, err := tk.NewSession(tr, "198.51.100.7")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	vip := sess.VirtualIP()

	runDone := make(chan struct{})
	go func() {
		sess.Run()
		close(runDone)
	}()

	// Wait until the session is live and registered.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && (!sess.runStarted.Load() || tk.SessionByVirtualIP(vip) == nil) {
		time.Sleep(time.Millisecond)
	}
	if tk.SessionByVirtualIP(vip) == nil {
		t.Fatal("session not registered while running")
	}
	if got := len(tk.Sessions()); got != 1 {
		t.Fatalf("Sessions() = %d while running, want 1", got)
	}

	// Transport dies (peer closed it) -> ReadFrame errors -> session reaps.
	_ = tr.Close()

	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after transport close — session not reaped")
	}

	if got := len(tk.Sessions()); got != 0 {
		t.Fatalf("after transport close: Sessions() = %d, want 0 (ghost session left behind)", got)
	}
	if tk.SessionByVirtualIP(vip) != nil {
		t.Fatal("after transport close: SessionByVirtualIP still returns the dead session")
	}

	// The VirtualIP must be released: a fresh session for the same source key
	// re-acquires the same deterministic VirtualIP. If the dead session still
	// held it, this would collision-walk to a different IP (the ghost-session
	// symptom that split a client's traffic across two VirtualIPs).
	sess2, err := tk.NewSession(newFakeTransport(), "198.51.100.7")
	if err != nil {
		t.Fatalf("NewSession after reap: %v", err)
	}
	defer sess2.End()
	if sess2.VirtualIP() != vip {
		t.Fatalf("VirtualIP not released after reap: reuse got %v, want %v", sess2.VirtualIP(), vip)
	}
}
