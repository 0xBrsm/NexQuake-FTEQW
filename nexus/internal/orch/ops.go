package orch

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

var (
	errAlreadyRunning = errors.New("already running")
	errAlreadyStopped = errors.New("already stopped")
)

func (m *ServerManager) findServerByIndexLocked(target int) (*server, error) {
	servers := m.serversLocked()
	index := target - 1
	if index >= 0 && index < len(servers) {
		return servers[index], nil
	}
	return nil, fmt.Errorf("unknown target %d", target)
}

func (m *ServerManager) serversLocked() []*server {
	servers := make([]*server, 0, len(m.serversByID))
	for _, s := range m.serversByID {
		if s == nil {
			continue
		}
		servers = append(servers, s)
	}
	slices.SortFunc(servers, func(a, b *server) int {
		return cmp.Compare(a.Line, b.Line)
	})
	return servers
}

func (m *ServerManager) serverInstancesLocked(s *server) []*instance {
	if s == nil {
		return nil
	}
	out := make([]*instance, 0, len(s.InstanceIDs))
	for _, serverID := range s.InstanceIDs {
		rec := m.instancesByID[serverID]
		if rec == nil {
			continue
		}
		out = append(out, rec)
	}
	slices.SortFunc(out, func(a, b *instance) int {
		aPort, bPort := recordListenPort(a), recordListenPort(b)
		switch {
		case aPort > 0 && bPort > 0:
			if aPort != bPort {
				return cmp.Compare(aPort, bPort)
			}
		case aPort > 0:
			return -1
		case bPort > 0:
			return 1
		}
		return cmp.Compare(a.id, b.id)
	})
	return out
}

// LaunchServer registers a new s and starts a server with the given binary
// and args. The runtime basedir must already be initialized (i.e. [ServerManager.StartAll]
// must have run). Returns an error if the binary fails to start.
func (m *ServerManager) LaunchServer(binary string, args []string) error {
	binary = strings.TrimSpace(binary)
	if binary == "" {
		return fmt.Errorf("missing binary")
	}
	if unsupportedArg, ok := findUnsupportedLaunchArg(args); ok {
		return fmt.Errorf("%s is not currently supported", unsupportedArg)
	}

	m.mu.RLock()
	if m.runtimeBasedir == "" {
		m.mu.RUnlock()
		return fmt.Errorf("runtime not initialized")
	}
	m.mu.RUnlock()

	startTag := time.Now().UTC().Format("20060102T150405Z")

	m.mu.Lock()
	line := -1
	for _, s := range m.serversByID {
		if s != nil && s.Line > line {
			line = s.Line
		}
	}
	line++
	launch := serverLaunch{
		Line:   line,
		LogDir: fmt.Sprintf("%d-%s-%s", line, filepath.Base(binary), startTag),
		Binary: binary,
		Args:   append([]string(nil), args...),
	}
	m.mu.Unlock()

	rec, err := m.registerServerLaunch(launch)
	if err != nil {
		return err
	}
	return m.startRecord(rec)
}

// StartServer starts the server s identified by 1-based s index.
// Returns [errAlreadyRunning] if the server is already up.
func (m *ServerManager) StartServer(target int) error {
	if target <= 0 {
		return fmt.Errorf("invalid target %d", target)
	}

	m.mu.RLock()
	s, err := m.findServerByIndexLocked(target)
	if err != nil {
		m.mu.RUnlock()
		return err
	}
	serverID := s.ServerID
	records := m.serverInstancesLocked(s)
	m.mu.RUnlock()

	if len(records) == 0 {
		m.mu.Lock()
		s = m.serversByID[serverID]
		if s == nil {
			m.mu.Unlock()
			return fmt.Errorf("unknown target %d", target)
		}
		rec := m.appendServerInstanceLocked(s, s.TemplateLaunch, instanceLifecycleWarming)
		m.mu.Unlock()
		records = []*instance{rec}
	}

	started := false
	for _, rec := range records {
		err := m.startRecord(rec)
		if err == nil {
			started = true
			continue
		}
		if errors.Is(err, errAlreadyRunning) {
			continue
		}
		return err
	}
	if started {
		return nil
	}
	return errAlreadyRunning
}

func (m *ServerManager) runServersAll(runOne func(target int) error, ignoreErr func(error) bool) error {
	servers := m.Snapshots()
	var errs []error
	for i := range servers {
		target := i + 1
		err := runOne(target)
		if err == nil {
			continue
		}
		if ignoreErr != nil && ignoreErr(err) {
			continue
		}
		errs = append(errs, fmt.Errorf("target %d: %w", target, err))
	}
	return errors.Join(errs...)
}

// StartServersAll calls [ServerManager.StartServer] on every registered s,
// ignoring servers that are already running.
func (m *ServerManager) StartServersAll() error {
	return m.runServersAll(
		m.StartServer,
		func(err error) bool { return errors.Is(err, errAlreadyRunning) },
	)
}

// StopServer stops all instances in the s identified by 1-based s
// index, removes their records, and returns [errAlreadyStopped] if no instance
// was running. killAfter is the grace period before SIGKILL.
func (m *ServerManager) StopServer(ctx context.Context, target int, killAfter time.Duration) error {
	if target <= 0 {
		return fmt.Errorf("invalid target %d", target)
	}

	m.mu.RLock()
	s, err := m.findServerByIndexLocked(target)
	if err != nil {
		m.mu.RUnlock()
		return err
	}
	// Capture rec.Running while still under the lock — the autoscale poller
	// writes it concurrently, so a post-unlock read is a data race.
	var records []runningServerEntry
	for _, rec := range m.serverInstancesLocked(s) {
		records = append(records, runningServerEntry{rec: rec, srv: rec.Running})
	}
	m.mu.RUnlock()

	stopped := false
	for _, entry := range records {
		rec, s := entry.rec, entry.srv
		if s == nil || s.Cmd == nil || s.Cmd.Process == nil || !isProcessAlive(s.Cmd.Process) {
			m.mu.Lock()
			m.removeServerRecordLocked(rec.id)
			m.mu.Unlock()
			continue
		}
		if err := m.stopServer(ctx, rec, s, killAfter, true); err != nil {
			return err
		}
		m.mu.Lock()
		m.removeServerRecordLocked(rec.id)
		m.mu.Unlock()
		stopped = true
	}

	if !stopped {
		return errAlreadyStopped
	}

	return nil
}

// StopServersAll calls [ServerManager.StopServer] on every registered s,
// ignoring servers that are already stopped.
func (m *ServerManager) StopServersAll(ctx context.Context, killAfter time.Duration) error {
	return m.runServersAll(
		func(target int) error { return m.StopServer(ctx, target, killAfter) },
		func(err error) bool { return errors.Is(err, errAlreadyStopped) },
	)
}

// RestartServer stops then starts the s identified by 1-based s index.
// A s that is not running is started directly without treating the missing
// stop as an error.
func (m *ServerManager) RestartServer(ctx context.Context, target int, killAfter time.Duration) error {
	if target <= 0 {
		return fmt.Errorf("invalid target %d", target)
	}
	if err := m.StopServer(ctx, target, killAfter); err != nil && !errors.Is(err, errAlreadyStopped) {
		return err
	}
	return m.StartServer(target)
}

// RestartServersAll calls [ServerManager.RestartServer] on every registered s.
func (m *ServerManager) RestartServersAll(ctx context.Context, killAfter time.Duration) error {
	return m.runServersAll(
		func(target int) error { return m.RestartServer(ctx, target, killAfter) },
		nil,
	)
}

// RemoveServer unregisters the s identified by 1-based s index and
// deletes all its instance records. Returns an error if any instance process is
// still alive; call [ServerManager.StopServer] first.
func (m *ServerManager) RemoveServer(target int) error {
	if target <= 0 {
		return fmt.Errorf("invalid target %d", target)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	s, err := m.findServerByIndexLocked(target)
	if err != nil {
		return err
	}

	instanceIDs := append([]int(nil), s.InstanceIDs...)
	for _, serverID := range instanceIDs {
		rec := m.instancesByID[serverID]
		if rec == nil {
			continue
		}
		if m.instanceRunningLocked(rec) {
			return fmt.Errorf("server is running; stop server first")
		}
		if rec.Running != nil {
			rec.Running = nil
			resetRecordStartupState(rec)
		}
		m.removeServerRecordLocked(rec.id)
	}

	delete(m.serversByID, s.ServerID)
	return nil
}
