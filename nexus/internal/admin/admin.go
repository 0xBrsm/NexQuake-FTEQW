// Package admin implements the Nexus admin subsystem: JSON-RPC envelope +
// dispatch and admin command handlers.
//
// The entry points for callers are:
//   - [New] — construct an [Admin] wiring registry, orch, and audit hooks.
//   - [Admin.Dispatch] — JSON-RPC envelope dispatch.
//   - [ParseRequest] — JSON-RPC envelope parse.
//
// Authentication and HTTP caller policy live in the sibling internal/access
// package. Admin commands receive an already-authorized caller.
//
// Request identity lives in internal/access; live client presence lives in
// internal/clients. Admin consumes both through narrow interfaces.
package admin

import (
	"context"
	"log/slog"
	"time"

	"github.com/0xBrsm/NexQuake/nexus/internal/clients"
	"github.com/0xBrsm/NexQuake/nexus/internal/orch"
)

// serverManager is the subset of *orch.ServerManager that admin needs.
// Declared here (consumer-side) so tests can substitute a fake without
// depending on orch's full surface.
type serverManager interface {
	Snapshots() []orch.ServerSnapshot
	InstanceSnapshots(target int) ([]orch.ServerSnapshot, error)
	StartServer(target int) error
	StartServersAll() error
	StopServer(ctx context.Context, target int, killAfter time.Duration) error
	StopServersAll(ctx context.Context, killAfter time.Duration) error
	RestartServer(ctx context.Context, target int, killAfter time.Duration) error
	RestartServersAll(ctx context.Context, killAfter time.Duration) error
	RemoveServer(target int) error
	LaunchServer(binary string, args []string) error
	DispatchInstanceCmd(port int, cmd, actorID string) (string, error)
}

// clientRegistry is the subset of *clients.Registry that admin RPC handlers
// need.
type clientRegistry interface {
	List() []clients.Connection
	ByVirtualAddr(virtualAddr string) (clients.Connection, bool)
}

// sourceBlocker is the subset of the access gate used by client.ban.
type sourceBlocker interface {
	Block(sourceIP string)
}

// Admin is the central admin subsystem. It owns the JSON-RPC dispatcher,
// direct handles to orch, and the joined registry view.
type Admin struct {
	registry clientRegistry
	orch     serverManager
	audit    *slog.Logger
	tailLog  func(n int) []string
	blocker  sourceBlocker
}

// New constructs an *Admin. audit and tailLog may be nil; registry
// and orch may be nil for tests that don't exercise those paths.
func New(
	registry clientRegistry,
	o serverManager,
	audit *slog.Logger,
	tailLog func(n int) []string,
	blocker sourceBlocker,
) *Admin {
	return &Admin{
		registry: registry,
		orch:     o,
		audit:    audit,
		tailLog:  tailLog,
		blocker:  blocker,
	}
}

// requireOrch returns an Unavailable MethodError when orch isn't wired up.
// Each server.* RPC handler calls it as its first guard.
func (a *Admin) requireOrch() error {
	if a.orch == nil {
		return &MethodError{Code: ErrCodeUnavailable, Message: "server manager unavailable"}
	}
	return nil
}

// requireRegistry returns an Unavailable MethodError when the client registry
// isn't wired up. Each client.* RPC handler calls it as its first guard.
func (a *Admin) requireRegistry() error {
	if a.registry == nil {
		return &MethodError{Code: ErrCodeUnavailable, Message: "client registry unavailable"}
	}
	return nil
}

// dispatchErr wraps a downstream-component error as a JSON-RPC dispatch
// error so handlers can return it without spelling out the MethodError each time.
func dispatchErr(err error) error {
	return &MethodError{Code: ErrCodeDispatch, Message: err.Error()}
}
