package admin

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/0xBrsm/NexQuake/nexus/internal/access"
	"github.com/0xBrsm/NexQuake/nexus/internal/clients"
	"github.com/0xBrsm/NexQuake/nexus/internal/orch"
	"github.com/0xBrsm/NexQuake/nexus/trunk"
)

func newClientSession() trunk.SessionInfo {
	return trunk.SessionInfo{
		SourceKey: "198.51.100.10",
		VirtualIP: [4]byte{127, 100, 10, 1},
		Transport: "WebSocket",
	}
}

// integrationAdmin builds a fresh *Admin wired to fresh fake trunk/orch
// implementations and a joined Clients view. Tests customize behaviour
// by assigning to the fake's function fields before invoking Dispatch.
func integrationAdmin() (*Admin, *clients.Registry, *fakeTrunk, *fakeOrch) {
	ft := &fakeTrunk{}
	fo := &fakeOrch{}
	cs := clients.NewRegistry(ft)
	a := New(cs, fo, nil, nil, access.NewBlocklist())
	cs.Add([4]byte{127, 100, 10, 1}, access.Client{SourceIP: "198.51.100.10"})
	return a, cs, ft, fo
}

func dispatchMethod(t *testing.T, a *Admin, method string, params any) *Response {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	req := &Request{Jsonrpc: "2.0", Method: method, Params: raw, ID: json.RawMessage(`1`)}
	return a.Dispatch(req, adminClient())
}

func TestIntegration_ServerList(t *testing.T) {
	a, _, _, fo := integrationAdmin()
	fo.SnapshotsFn = func() []orch.ServerSnapshot {
		return []orch.ServerSnapshot{{Hostname: "fragfest", State: "running"}}
	}
	resp := dispatchMethod(t, a, "server.list", struct{}{})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	got := resp.Result.(ServerListResult)
	if len(got.Servers) != 1 || got.Servers[0].Hostname != "fragfest" {
		t.Fatalf("got %+v", got.Servers)
	}
}

func TestIntegration_ServerStartByIndex(t *testing.T) {
	a, _, _, fo := integrationAdmin()
	var started int
	fo.StartServerFn = func(idx int) error { started = idx; return nil }

	resp := dispatchMethod(t, a, "server.start", map[string]any{"target": "1"})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if !resp.Result.(ServerLifecycleResult).OK {
		t.Fatal("expected OK=true")
	}
	if started != 1 {
		t.Fatalf("expected StartServer(1), got %d", started)
	}
}

func TestIntegration_ServerStartAll(t *testing.T) {
	a, _, _, fo := integrationAdmin()
	called := false
	fo.StartServersAllFn = func() error { called = true; return nil }
	resp := dispatchMethod(t, a, "server.start", map[string]any{"target": "all"})
	if resp.Error != nil || !called {
		t.Fatalf("expected success, got error=%+v called=%v", resp.Error, called)
	}
}

func TestIntegration_ServerStartRejectsBadTarget(t *testing.T) {
	a, _, _, _ := integrationAdmin()
	resp := dispatchMethod(t, a, "server.start", map[string]any{"target": "fragfest"})
	if resp.Error == nil || resp.Error.Code != ErrCodeInvalidParams {
		t.Fatalf("expected InvalidParams for hostname target, got %+v", resp.Error)
	}
}

func TestIntegration_ServerRemoveRequiresIndex(t *testing.T) {
	a, _, _, _ := integrationAdmin()
	resp := dispatchMethod(t, a, "server.remove", map[string]any{})
	if resp.Error == nil || resp.Error.Code != ErrCodeInvalidParams {
		t.Fatalf("expected InvalidParams, got %+v", resp.Error)
	}
}

func TestIntegration_ServerRemoveByIndex(t *testing.T) {
	a, _, _, fo := integrationAdmin()
	var removed int
	fo.RemoveServerFn = func(idx int) error { removed = idx; return nil }
	resp := dispatchMethod(t, a, "server.remove", map[string]any{"index": 2})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if !resp.Result.(ServerRemoveResult).Removed || removed != 2 {
		t.Fatalf("got removed=%d result=%+v", removed, resp.Result)
	}
}

func TestIntegration_ServerCommand(t *testing.T) {
	a, _, _, fo := integrationAdmin()
	fo.DispatchInstanceCmdFn = func(port int, cmd, actorID string) (string, error) {
		if port != 26000 || cmd != "status" {
			t.Fatalf("unexpected dispatch: port=%d cmd=%q", port, cmd)
		}
		return "host: fragfest\n", nil
	}
	resp := dispatchMethod(t, a, "server.instance.command", map[string]any{"port": 26000, "cmd": "status"})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if reply := resp.Result.(InstanceCommandResult).Reply; !strings.Contains(reply, "fragfest") {
		t.Fatalf("got reply %q", reply)
	}
}

func TestIntegration_ClientList(t *testing.T) {
	a, cs, ft, _ := integrationAdmin()
	cs.Add([4]byte{127, 100, 10, 2}, access.Client{SourceIP: "198.51.100.11", ID: "bob@example.com"})
	ft.SessionsFn = func() []trunk.SessionInfo {
		return []trunk.SessionInfo{
			{SourceKey: "198.51.100.10", VirtualIP: [4]byte{127, 100, 10, 1}},
			{SourceKey: "198.51.100.11", VirtualIP: [4]byte{127, 100, 10, 2}},
		}
	}
	resp := dispatchMethod(t, a, "client.list", struct{}{})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	got := resp.Result.(ClientListResult).Clients
	if len(got) != 2 {
		t.Fatalf("expected 2 clients, got %d", len(got))
	}
	// Sorted by VirtualIP bytes — 127.100.10.1 first.
	if got[0].VirtualAddr != "127.100.10.1" {
		t.Fatalf("got[0].VirtualAddr = %q, want 127.100.10.1", got[0].VirtualAddr)
	}
	if got[1].ID != "bob@example.com" {
		t.Fatalf("got[1].ID = %q, want bob@example.com", got[1].ID)
	}
}

func TestIntegration_ClientBanClosesAndReserves(t *testing.T) {
	sess := newClientSession()
	nqipStr := "127.100.10.1"

	a, _, ft, _ := integrationAdmin()
	ft.SessionsFn = func() []trunk.SessionInfo { return []trunk.SessionInfo{sess} }

	resp := dispatchMethod(t, a, "client.ban", map[string]any{"nqip": nqipStr})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	result := resp.Result.(ClientBanResult)
	if result.VirtualAddr != nqipStr {
		t.Fatalf("VirtualIP = %q, want %q", result.VirtualAddr, nqipStr)
	}
	if len(result.SourceIPs) != 1 || result.SourceIPs[0] != sess.SourceKey {
		t.Fatalf("SourceIPs = %v, want [%q]", result.SourceIPs, sess.SourceKey)
	}
	if !a.blocker.(*access.Blocklist).IsBlocked(sess.SourceKey) {
		t.Fatal("expected source IP to be blocked")
	}
}

func TestIntegration_ClientBanUnknownVIP(t *testing.T) {
	a, _, _, _ := integrationAdmin()
	resp := dispatchMethod(t, a, "client.ban", map[string]any{"nqip": "127.0.0.99"})
	if resp.Error == nil || resp.Error.Code != ErrCodeNotFound {
		t.Fatalf("expected NotFound, got %+v", resp.Error)
	}
}
