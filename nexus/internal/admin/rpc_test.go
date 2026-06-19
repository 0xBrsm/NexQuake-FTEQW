package admin

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/0xBrsm/NexQuake/nexus/internal/access"
	"github.com/0xBrsm/NexQuake/nexus/internal/orch"
)

// adminClient is the canonical caller used by Dispatch tests.
func adminClient() access.Client {
	return access.Client{ID: "admin", SourceIP: "198.51.100.1"}
}

// --- ParseRequest -----------------------------------------------------------

func TestParseRequest_Empty(t *testing.T) {
	_, resp := ParseRequest(nil)
	if resp == nil || resp.Error == nil || resp.Error.Code != ErrCodeInvalidReq {
		t.Fatalf("expected invalid-request error, got %+v", resp)
	}
}

func TestParseRequest_BadJSON(t *testing.T) {
	_, resp := ParseRequest([]byte("{not json"))
	if resp == nil || resp.Error == nil || resp.Error.Code != ErrCodeParseError {
		t.Fatalf("expected parse-error, got %+v", resp)
	}
}

func TestParseRequest_WrongVersion(t *testing.T) {
	_, resp := ParseRequest([]byte(`{"jsonrpc":"1.0","method":"x","id":1}`))
	if resp == nil || resp.Error == nil || resp.Error.Code != ErrCodeInvalidReq {
		t.Fatalf("expected invalid-request on wrong version, got %+v", resp)
	}
}

func TestParseRequest_MissingMethod(t *testing.T) {
	_, resp := ParseRequest([]byte(`{"jsonrpc":"2.0","id":1}`))
	if resp == nil || resp.Error == nil || resp.Error.Code != ErrCodeInvalidReq {
		t.Fatalf("expected invalid-request on missing method, got %+v", resp)
	}
}

func TestParseRequest_OK(t *testing.T) {
	req, resp := ParseRequest([]byte(`{"jsonrpc":"2.0","method":"server.list","id":7}`))
	if resp != nil {
		t.Fatalf("unexpected error: %+v", resp)
	}
	if req.Method != "server.list" {
		t.Fatalf("got method %q", req.Method)
	}
}

// --- Dispatch ---------------------------------------------------------------

func TestDispatch_UnknownMethod(t *testing.T) {
	a := New(nil, &fakeOrch{}, nil, nil, nil)
	req := &Request{Jsonrpc: "2.0", Method: "bogus.method", ID: json.RawMessage(`1`)}
	resp := a.Dispatch(req, adminClient())
	if resp.Error == nil || resp.Error.Code != ErrCodeMethodNotFound {
		t.Fatalf("expected method-not-found, got %+v", resp.Error)
	}
}

func TestDispatch_InvalidParams(t *testing.T) {
	a := New(nil, &fakeOrch{}, nil, nil, nil)
	// server.launch requires non-empty binary
	req := &Request{Jsonrpc: "2.0", Method: "server.launch", Params: json.RawMessage(`{}`), ID: json.RawMessage(`2`)}
	resp := a.Dispatch(req, adminClient())
	if resp.Error == nil || resp.Error.Code != ErrCodeInvalidParams {
		t.Fatalf("expected invalid-params, got %+v", resp.Error)
	}
}

func TestDispatch_ServerListOK(t *testing.T) {
	a := New(nil, &fakeOrch{
		SnapshotsFn: func() []orch.ServerSnapshot {
			return []orch.ServerSnapshot{{Line: 0, Hostname: "fragfest", State: "running"}}
		},
	}, nil, nil, nil)
	req := &Request{Jsonrpc: "2.0", Method: "server.list", ID: json.RawMessage(`3`)}
	resp := a.Dispatch(req, adminClient())
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	result := resp.Result.(ServerListResult)
	if len(result.Servers) != 1 || result.Servers[0].Hostname != "fragfest" {
		t.Fatalf("got %+v", result.Servers)
	}
}

func TestDispatch_ServerListUnavailable(t *testing.T) {
	a := New(nil, nil, nil, nil, nil)
	req := &Request{Jsonrpc: "2.0", Method: "server.list", ID: json.RawMessage(`4`)}
	resp := a.Dispatch(req, adminClient())
	if resp.Error == nil || resp.Error.Code != ErrCodeUnavailable {
		t.Fatalf("expected unavailable error, got %+v", resp.Error)
	}
}

func TestDispatch_LogsTailTrimsBlank(t *testing.T) {
	a := New(nil, &fakeOrch{}, nil, func(n int) []string {
		return []string{"hello\n", "", "world\r\n"}
	}, nil)
	req := &Request{Jsonrpc: "2.0", Method: "logs.tail", ID: json.RawMessage(`5`)}
	resp := a.Dispatch(req, adminClient())
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	got := resp.Result.(LogsTailResult).Lines
	if len(got) != 2 || got[0] != "hello" || got[1] != "world" {
		t.Fatalf("got lines %q", got)
	}
}

// --- audit logging ----------------------------------------------------------

func TestDispatch_EmitsAuditLogs(t *testing.T) {
	var entries []string
	a := New(nil, &fakeOrch{
		SnapshotsFn: func() []orch.ServerSnapshot { return nil },
	}, captureLogger(&entries), nil, nil)
	req := &Request{Jsonrpc: "2.0", Method: "server.list", ID: json.RawMessage(`9`)}
	_ = a.Dispatch(req, access.Client{ID: "alice@example.com"})
	if len(entries) != 2 {
		t.Fatalf("expected request+response audit logs, got %d: %v", len(entries), entries)
	}
	if !strings.Contains(entries[0], `actor="alice@example.com"`) || !strings.Contains(entries[0], "method=server.list") {
		t.Fatalf("request audit log: %q", entries[0])
	}
	if !strings.Contains(entries[1], "method=server.list") {
		t.Fatalf("response audit log: %q", entries[1])
	}
}

func TestAuditUnauthorized(t *testing.T) {
	var entries []string
	a := New(nil, nil, captureLogger(&entries), nil, nil)
	a.AuditUnauthorized(access.Client{ID: "198.51.100.1", SourceIP: "198.51.100.1"}, "server.list")
	if len(entries) != 1 {
		t.Fatalf("expected one audit log, got %d: %v", len(entries), entries)
	}
	if !strings.Contains(entries[0], `actor="198.51.100.1"`) ||
		!strings.Contains(entries[0], "method=server.list") ||
		!strings.Contains(entries[0], "admin-rcon error") ||
		!strings.Contains(entries[0], `error="unauthorized"`) {
		t.Fatalf("unauthorized audit log: %q", entries[0])
	}
}
