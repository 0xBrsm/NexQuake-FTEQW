package orch

import (
	"os"
	"os/exec"
)

// NewTestServerLaunch returns a minimal launch spec for tests.
func NewTestServerLaunch(line int) serverLaunch {
	return serverLaunch{Line: line}
}

// NewTestServer creates a managed server stub backed by the current process.
func NewTestServer(port int) *managedServer {
	process, _ := os.FindProcess(os.Getpid())
	srv := &managedServer{
		Cmd:     &exec.Cmd{Process: process},
		Console: newServerConsole(nil),
	}
	return srv
}

// NewTestServerWithPTY creates a managed server stub with a writable console.
func NewTestServerWithPTY(port int, pty *os.File) *managedServer {
	srv := NewTestServer(port)
	srv.Console = newServerConsole(pty)
	return srv
}

// PublishConsoleLineForTest injects a console line into the server buffer.
func (s *managedServer) PublishConsoleLineForTest(line string) {
	if s == nil || s.Console == nil {
		return
	}
	s.Console.publishLine(line)
}

// SetServerRunningForTest assigns the running process for a test record.
func (m *ServerManager) SetServerRunningForTest(rec *instance, srv *managedServer) {
	if m == nil || rec == nil {
		return
	}
	m.mu.Lock()
	rec.Running = srv
	m.mu.Unlock()
}

// SetServerInfoForTest sets cached server-info fields on a test record.
func (m *ServerManager) SetServerInfoForTest(rec *instance, hostname, mapName string, players, maxPlayers byte) {
	if m == nil || rec == nil {
		return
	}
	m.mu.Lock()
	rec.Hostname = hostname
	rec.MapName = mapName
	rec.Players = players
	rec.MaxPlayers = maxPlayers
	m.mu.Unlock()
}

func (m *ServerManager) registerBareInstance(launch serverLaunch) *instance {
	m.mu.Lock()
	defer m.mu.Unlock()

	rec := &instance{
		id:     m.nextInstanceID,
		Launch: cloneServerLaunch(launch),
	}
	m.nextInstanceID++
	m.instancesByID[rec.id] = rec
	return rec
}

func buildServerSnapshot(rec *instance) ServerSnapshot {
	if rec == nil {
		return ServerSnapshot{State: "stopped"}
	}

	snap := ServerSnapshot{
		State:      "stopped",
		LastError:  rec.lastError,
		Hostname:   rec.Hostname,
		MapName:    rec.MapName,
		Players:    rec.Players,
		MaxPlayers: rec.MaxPlayers,
		Line:       rec.Launch.Line,
	}
	if rec.resolvedPortKnown {
		snap.ListenPort = rec.resolvedPort
	}
	if rec.spec != nil && rec.spec.ListenPort >= 0 && rec.spec.ListenPort <= 65535 {
		snap.ListenPort = rec.spec.ListenPort
	}
	if len(rec.resolvedSearchPath) > 0 {
		snap.GameDir = activeGameDir(rec.resolvedSearchPath)
	}
	if rec.spec != nil && len(rec.spec.SearchPath) > 0 {
		snap.GameDir = activeGameDir(rec.spec.SearchPath)
	}

	s := rec.Running
	if s != nil && s.Cmd != nil && s.Cmd.Process != nil && isProcessAlive(s.Cmd.Process) {
		snap.PID = s.Cmd.Process.Pid
		if rec.awaitingServerInfo {
			snap.State = "starting"
		} else {
			snap.State = "running"
		}
		return snap
	}
	if rec.lastError != "" {
		snap.State = "crashed"
	}
	return snap
}

func (m *ServerManager) registerServerSeed(rec *instance) error {
	if rec == nil {
		return nil
	}
	port, ok := launchConfiguredPort(rec.Launch)
	if !ok || port != 0 {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.serverByInstanceID[rec.id]; exists {
		return nil
	}

	configuredPort, hasConfiguredPort := launchConfiguredPort(rec.Launch)
	autoscales := hasConfiguredPort && configuredPort == 0 && max(1, m.serverMaxInstances) > 1

	s := &server{
		ServerID:       rec.Launch.Line + 1,
		Line:           rec.Launch.Line,
		TemplateLaunch: cloneServerLaunch(rec.Launch),
		Autoscales:     autoscales,
		InstanceIDs:    []int{rec.id},
		instanceStates: map[int]*instanceState{
			rec.id: &instanceState{Lifecycle: instanceLifecycleWarming},
		},
	}
	if !rec.LastSeen.IsZero() {
		s.instanceStates[rec.id].Lifecycle = instanceLifecycleActive
	}
	m.serversByID[s.ServerID] = s
	m.serverByInstanceID[rec.id] = s
	m.refreshServerSnapshotLocked(s)

	return nil
}
