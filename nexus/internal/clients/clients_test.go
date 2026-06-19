package clients

import (
	"testing"
	"time"

	"github.com/0xBrsm/NexQuake/nexus/internal/access"
	"github.com/0xBrsm/NexQuake/nexus/trunk"
)

type fakeTrunk struct {
	SessionsFn           func() []trunk.SessionInfo
	SessionByVirtualIPFn func(virtualIP [4]byte) *trunk.Session
}

func (f *fakeTrunk) Sessions() []trunk.SessionInfo {
	if f == nil || f.SessionsFn == nil {
		return nil
	}
	return f.SessionsFn()
}

func (f *fakeTrunk) SessionByVirtualIP(vip [4]byte) *trunk.Session {
	if f == nil || f.SessionByVirtualIPFn == nil {
		return nil
	}
	return f.SessionByVirtualIPFn(vip)
}

func TestConnections_Active_JoinsTrunkSessionsWithIdentity(t *testing.T) {
	now := time.Now()
	ft := &fakeTrunk{
		SessionsFn: func() []trunk.SessionInfo {
			return []trunk.SessionInfo{
				{
					SourceKey:        "198.51.100.10",
					VirtualIP:        [4]byte{127, 100, 10, 1},
					Transport:        "WebSocket",
					ConnectedAt:      now,
					ActiveServerPort: 26000,
				},
				{
					SourceKey: "198.51.100.11",
					VirtualIP: [4]byte{127, 100, 10, 2},
					Transport: "Custom",
				},
			}
		},
	}
	c := NewRegistry(ft)
	c.Add([4]byte{127, 100, 10, 1}, access.Client{SourceIP: "198.51.100.10", ID: "alice@example.com"})

	got := c.List()
	if len(got) != 2 {
		t.Fatalf("List() len = %d, want 2", len(got))
	}

	// Sorted by NQIP bytes - 127.100.10.1 first.
	if got[0].VirtualAddr != "127.100.10.1" {
		t.Fatalf("got[0].VirtualAddr = %q, want 127.100.10.1", got[0].VirtualAddr)
	}
	if got[0].ID != "alice@example.com" {
		t.Fatalf("got[0].ID = %q, want alice@example.com", got[0].ID)
	}
	if got[0].Transport != "WebSocket" {
		t.Fatalf("got[0].Transport = %q, want WebSocket", got[0].Transport)
	}
	if got[0].ActiveServerPort != 26000 || got[0].ActiveServerHost != "" {
		t.Fatalf("ActiveServer fields = port=%d host=%q, want port=26000 host empty",
			got[0].ActiveServerPort, got[0].ActiveServerHost)
	}

	// Second entry has no recorded identity, so source falls back to trunk SourceKey.
	if got[1].VirtualAddr != "127.100.10.2" || got[1].SourceIP != "198.51.100.11" || got[1].ID != "" {
		t.Fatalf("got[1] = %+v", got[1])
	}
}

func TestConnections_ByVirtualIP(t *testing.T) {
	ft := &fakeTrunk{
		SessionsFn: func() []trunk.SessionInfo {
			return []trunk.SessionInfo{
				{SourceKey: "1.1.1.1", VirtualIP: [4]byte{127, 1, 1, 1}},
				{SourceKey: "2.2.2.2", VirtualIP: [4]byte{127, 2, 2, 2}},
			}
		},
	}
	c := NewRegistry(ft)

	got, ok := c.ByVirtualAddr("127.1.1.1")
	if !ok || got.SourceIP != "1.1.1.1" {
		t.Fatalf("ByVirtualAddr got (%+v, %v)", got, ok)
	}

	if _, ok := c.ByVirtualAddr("127.99.99.99"); ok {
		t.Fatal("unknown VIP should return ok=false")
	}
	if _, ok := c.ByVirtualAddr("0.0.0.0"); ok {
		t.Fatal("zero VIP should return ok=false")
	}
	if _, ok := c.ByVirtualAddr("not-an-ip"); ok {
		t.Fatal("garbage input should return ok=false")
	}
}

func TestConnections_Active_NoTrunkReturnsEmpty(t *testing.T) {
	c := NewRegistry(nil)
	got := c.List()
	if got == nil {
		t.Fatal("nil trunk should give empty slice, got nil")
	}
	if len(got) != 0 {
		t.Fatalf("nil trunk should give empty slice, got %+v", got)
	}
}

func TestConnections_KeysMetadataByVirtualIP(t *testing.T) {
	ft := &fakeTrunk{
		SessionsFn: func() []trunk.SessionInfo {
			return []trunk.SessionInfo{
				{SourceKey: "198.51.100.10", VirtualIP: [4]byte{127, 100, 10, 1}},
				{SourceKey: "198.51.100.10", VirtualIP: [4]byte{127, 100, 10, 2}},
			}
		},
	}
	c := NewRegistry(ft)
	c.Add([4]byte{127, 100, 10, 1}, access.Client{SourceIP: "198.51.100.10", ID: "alice-tab"})
	c.Add([4]byte{127, 100, 10, 2}, access.Client{SourceIP: "198.51.100.10", ID: "alice-other-tab"})
	c.Remove([4]byte{127, 100, 10, 1})

	got := c.List()
	if len(got) != 2 {
		t.Fatalf("List() len = %d, want 2", len(got))
	}
	if got[0].ID != "" {
		t.Fatalf("first session metadata should be detached, got %+v", got[0])
	}
	if got[1].ID != "alice-other-tab" {
		t.Fatalf("second session metadata should remain, got %+v", got[1])
	}
}
