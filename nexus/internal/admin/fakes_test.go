package admin

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/0xBrsm/NexQuake/nexus/internal/orch"
	"github.com/0xBrsm/NexQuake/nexus/trunk"
)

// captureLogger returns a *slog.Logger that appends "msg key=value ..." lines
// to the given slice. Tests use this to assert which audit records the admin
// dispatcher emitted.
func captureLogger(entries *[]string) *slog.Logger {
	return slog.New(&captureHandler{entries: entries})
}

type captureHandler struct{ entries *[]string }

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value.Any())
		return true
	})
	*h.entries = append(*h.entries, b.String())
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

// fakeTrunk satisfies the unexported session-lookup contract for tests that
// build a real clients.Registry on top of fake trunk state. Fields are
// nil-friendly: an unset Fn returns the zero value for that method's signature.
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

// fakeOrch satisfies the serverManager interface for tests.
type fakeOrch struct {
	SnapshotsFn           func() []orch.ServerSnapshot
	InstanceSnapshotsFn   func(target int) ([]orch.ServerSnapshot, error)
	StartServerFn         func(target int) error
	StartServersAllFn     func() error
	StopServerFn          func(ctx context.Context, target int, killAfter time.Duration) error
	StopServersAllFn      func(ctx context.Context, killAfter time.Duration) error
	RestartServerFn       func(ctx context.Context, target int, killAfter time.Duration) error
	RestartServersAllFn   func(ctx context.Context, killAfter time.Duration) error
	RemoveServerFn        func(target int) error
	LaunchServerFn        func(binary string, args []string) error
	DispatchInstanceCmdFn func(port int, cmd, actorID string) (string, error)
}

func (f *fakeOrch) Snapshots() []orch.ServerSnapshot {
	if f == nil || f.SnapshotsFn == nil {
		return nil
	}
	return f.SnapshotsFn()
}

func (f *fakeOrch) InstanceSnapshots(target int) ([]orch.ServerSnapshot, error) {
	if f == nil || f.InstanceSnapshotsFn == nil {
		return nil, nil
	}
	return f.InstanceSnapshotsFn(target)
}

func (f *fakeOrch) StartServer(target int) error {
	if f == nil || f.StartServerFn == nil {
		return nil
	}
	return f.StartServerFn(target)
}

func (f *fakeOrch) StartServersAll() error {
	if f == nil || f.StartServersAllFn == nil {
		return nil
	}
	return f.StartServersAllFn()
}

func (f *fakeOrch) StopServer(ctx context.Context, target int, killAfter time.Duration) error {
	if f == nil || f.StopServerFn == nil {
		return nil
	}
	return f.StopServerFn(ctx, target, killAfter)
}

func (f *fakeOrch) StopServersAll(ctx context.Context, killAfter time.Duration) error {
	if f == nil || f.StopServersAllFn == nil {
		return nil
	}
	return f.StopServersAllFn(ctx, killAfter)
}

func (f *fakeOrch) RestartServer(ctx context.Context, target int, killAfter time.Duration) error {
	if f == nil || f.RestartServerFn == nil {
		return nil
	}
	return f.RestartServerFn(ctx, target, killAfter)
}

func (f *fakeOrch) RestartServersAll(ctx context.Context, killAfter time.Duration) error {
	if f == nil || f.RestartServersAllFn == nil {
		return nil
	}
	return f.RestartServersAllFn(ctx, killAfter)
}

func (f *fakeOrch) RemoveServer(target int) error {
	if f == nil || f.RemoveServerFn == nil {
		return nil
	}
	return f.RemoveServerFn(target)
}

func (f *fakeOrch) LaunchServer(binary string, args []string) error {
	if f == nil || f.LaunchServerFn == nil {
		return nil
	}
	return f.LaunchServerFn(binary, args)
}

func (f *fakeOrch) DispatchInstanceCmd(port int, cmd, actorID string) (string, error) {
	if f == nil || f.DispatchInstanceCmdFn == nil {
		return "", nil
	}
	return f.DispatchInstanceCmdFn(port, cmd, actorID)
}
