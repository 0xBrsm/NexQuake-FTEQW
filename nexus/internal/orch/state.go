package orch

import (
	"fmt"
	"log/slog"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// serverSpec is resolved runtime metadata for a running server.
type serverSpec struct {
	Line       int
	ListenPort int
	SearchPath []string
}

// managedServer represents a running server process.
type managedServer struct {
	Cmd     *exec.Cmd
	Console *serverConsole
	done    chan error
}

// instance tracks one launched server process in Nexus registry.
type instance struct {
	id     int
	Launch serverLaunch
	spec   *serverSpec

	resolvedPortKnown  bool
	resolvedPort       int
	resolvedSearchPath []string

	Running   *managedServer
	lastError string

	Hostname   string
	MapName    string
	Players    byte
	MaxPlayers byte
	// polledGameDir is the game/mod directory reported by the most recent
	// getinfo infoResponse (modname/*gamedir/gamedir). Empty until first poll.
	polledGameDir string
	LastSeen      time.Time

	relayConsoleReady   bool
	awaitingServerInfo  bool
	startupTimedOutOnce bool
}

// ServerSnapshot is a point-in-time view of a managed server or instance server,
// used for display and admin API responses.
type ServerSnapshot struct {
	// Line is the 0-based index of the servers.ini entry that owns this server.
	Line int
	// CandidatePort is the suggested connect port for a s snapshot.
	// Zero if no instance port is currently available.
	CandidatePort int
	// ListenPort is the UDP port the server is (or was last) listening on.
	// Set for instance snapshots; zero for s snapshots.
	ListenPort int
	// GameDir is the active game directory (first entry in the search path).
	GameDir string
	// Hostname is the server's self-reported hostname from the last getinfo poll.
	Hostname string
	// MapName is the current map from the last getinfo poll.
	MapName string
	// Players is the current player count from the last getinfo poll.
	Players byte
	// MaxPlayers is the server capacity from the last getinfo poll.
	MaxPlayers byte
	// Instances is the visible instance instance count for s snapshots.
	// Zero hides the server suffix and is always zero for instance snapshots.
	Instances uint16
	// State is one of "stopped", "starting", "running", or "crashed".
	State string
	// PID is the OS process ID of the running server.
	// Zero when the server is not running, or when the s has multiple instances.
	PID int
	// LastError holds the most recent launch or stop error, if any.
	LastError string
}

func cloneServerLaunch(launch serverLaunch) serverLaunch {
	cloned := launch
	cloned.Args = append([]string(nil), launch.Args...)
	return cloned
}

func activeGameDir(searchPath []string) string {
	if len(searchPath) == 0 {
		return ""
	}
	return searchPath[0]
}

func recordListenPort(rec *instance) int {
	if rec == nil {
		return 0
	}
	if rec.spec != nil && rec.spec.ListenPort > 0 && rec.spec.ListenPort <= 65535 {
		return rec.spec.ListenPort
	}
	if rec.resolvedPortKnown && rec.resolvedPort > 0 && rec.resolvedPort <= 65535 {
		return rec.resolvedPort
	}
	return 0
}

func normalizeSearchPath(searchPath []string) []string {
	if len(searchPath) == 0 {
		return nil
	}

	normalized := make([]string, 0, len(searchPath))
	seen := make(map[string]struct{}, len(searchPath))
	for _, raw := range searchPath {
		gameDir := strings.TrimSpace(raw)
		if gameDir == "" {
			continue
		}
		if strings.ContainsAny(gameDir, `/\\`) {
			continue
		}
		if _, ok := seen[gameDir]; ok {
			continue
		}
		seen[gameDir] = struct{}{}
		normalized = append(normalized, gameDir)
	}
	return normalized
}

func (m *ServerManager) applyResolvedSpecLocked(rec *instance) {
	if !rec.resolvedPortKnown || len(rec.resolvedSearchPath) == 0 {
		return
	}
	rec.spec = &serverSpec{
		Line:       rec.Launch.Line,
		ListenPort: rec.resolvedPort,
		SearchPath: append([]string(nil), rec.resolvedSearchPath...),
	}
}

func (m *ServerManager) assignPortLocked(rec *instance, port int) {
	if rec == nil || port < 0 || port > 65535 {
		return
	}

	if rec.resolvedPortKnown {
		if rec.resolvedPort > 0 {
			return
		}
		if port == 0 {
			return
		}
	}

	rec.resolvedPortKnown = true
	rec.resolvedPort = port

	if port < 1 {
		return
	}

	if ids := m.instanceIDsByPort[port]; !slices.Contains(ids, rec.id) {
		m.instanceIDsByPort[port] = append(ids, rec.id)
	}
}

func (m *ServerManager) removeServerIDFromPortLocked(port int, serverID int) {
	if port <= 0 {
		return
	}
	ids := slices.DeleteFunc(m.instanceIDsByPort[port], func(id int) bool { return id == serverID })
	if len(ids) == 0 {
		delete(m.instanceIDsByPort, port)
		return
	}
	m.instanceIDsByPort[port] = ids
}

// updatePort updates the resolved listen port for a server record.
func (m *ServerManager) updatePort(rec *instance, port int) {
	if rec == nil || port < 0 || port > 65535 {
		return
	}
	m.mu.Lock()
	m.assignPortLocked(rec, port)
	m.applyResolvedSpecLocked(rec)
	m.mu.Unlock()
}

// updateSearchPathNormalized updates the resolved search path for an instance
// record. searchPath must already be normalized — see [normalizeSearchPath].
func (m *ServerManager) updateSearchPathNormalized(rec *instance, searchPath []string) {
	if rec == nil {
		return
	}
	m.mu.Lock()
	if len(searchPath) > 0 {
		rec.resolvedSearchPath = append([]string(nil), searchPath...)
	}
	m.applyResolvedSpecLocked(rec)
	m.mu.Unlock()
}

func (m *ServerManager) updateGameState(port int, hostname, mapName, gameDir string, players, maxPlayers byte) {
	if port < 1 || port > 65535 {
		return
	}

	type startupOnlineCommand struct {
		label   string
		console *serverConsole
	}
	var onlineCommands []startupOnlineCommand
	var observedServerIDs []int

	m.mu.Lock()
	now := time.Now()
	for _, serverID := range m.instanceIDsByPort[port] {
		rec := m.instancesByID[serverID]
		if rec == nil {
			continue
		}
		rec.Hostname = hostname
		rec.MapName = mapName
		rec.Players = players
		rec.MaxPlayers = maxPlayers
		if gameDir != "" {
			rec.polledGameDir = gameDir
		}
		rec.LastSeen = now
		if s := m.serverByInstanceID[rec.id]; s != nil {
			observedServerIDs = append(observedServerIDs, s.ServerID)
		}
		if rec.Running == nil || !rec.awaitingServerInfo {
			continue
		}
		rec.awaitingServerInfo = false
		rec.relayConsoleReady = true
		onlineCommands = append(onlineCommands, startupOnlineCommand{
			label:   m.serverConsoleLabelLocked(rec),
			console: rec.Running.Console,
		})
	}
	m.mu.Unlock()

	for _, cmd := range onlineCommands {
		if cmd.console == nil {
			slog.Error(fmt.Sprintf("server %s online marker failed: server console unavailable", cmd.label))
			continue
		}
		if err := cmd.console.writeCommandWithOptions(
			"echo online and accepting clients",
			true,
		); err != nil {
			slog.Error(fmt.Sprintf("server %s online marker failed: %v", cmd.label, err))
		}
	}

	m.reconcileServers(observedServerIDs)
}

type runningServerEntry struct {
	rec *instance
	srv *managedServer
}

func (m *ServerManager) runningServers() []runningServerEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []runningServerEntry
	for _, rec := range m.instancesByID {
		if rec == nil || rec.Running == nil {
			continue
		}
		out = append(out, runningServerEntry{rec: rec, srv: rec.Running})
	}
	return out
}

func (m *ServerManager) buildServerSnapshotLocked(s *server) ServerSnapshot {
	if s == nil {
		return ServerSnapshot{State: "stopped"}
	}

	snap := ServerSnapshot{
		Line:       s.Line,
		GameDir:    s.DisplayGameDir,
		Hostname:   s.DisplayHostname,
		MapName:    s.DisplayMap,
		Players:    byte(min(int(s.aggregateUsers), 0xff)),
		MaxPlayers: byte(min(int(s.aggregateMaxUsers), 0xff)),
		Instances:  0,
		State:      "stopped",
	}
	if port, ok := m.pickServerInstanceLocked(s, false); ok {
		snap.CandidatePort = port
	}
	if s.Autoscales {
		snap.Instances = s.joinableInstances
	}

	runningCount := 0
	awaitingInfo := false
	lastError := ""

	for _, serverID := range s.InstanceIDs {
		rec := m.instancesByID[serverID]
		if rec == nil {
			continue
		}
		if lastError == "" && rec.lastError != "" {
			lastError = rec.lastError
		}
		if !m.instanceRunningLocked(rec) {
			continue
		}

		runningCount++
		if rec.awaitingServerInfo {
			awaitingInfo = true
		}
		if snap.PID == 0 && rec.Running != nil && rec.Running.Cmd != nil && rec.Running.Cmd.Process != nil {
			snap.PID = rec.Running.Cmd.Process.Pid
		}
		if snap.GameDir == "" {
			snap.GameDir = recordGameDir(rec)
		}
		if snap.Hostname == "" && strings.TrimSpace(rec.Hostname) != "" {
			snap.Hostname = rec.Hostname
		}
		if snap.MapName == "" && strings.TrimSpace(rec.MapName) != "" {
			snap.MapName = rec.MapName
		}
	}

	snap.LastError = lastError
	if runningCount > 0 {
		if awaitingInfo {
			snap.State = "starting"
		} else {
			snap.State = "running"
		}
		if runningCount > 1 {
			snap.PID = 0
		}
		return snap
	}
	if snap.LastError != "" {
		snap.State = "crashed"
	}
	return snap
}

func (m *ServerManager) buildInstanceSnapshotLocked(s *server, rec *instance) ServerSnapshot {
	snap := ServerSnapshot{
		Line:       rec.Launch.Line,
		ListenPort: recordListenPort(rec),
		GameDir:    recordGameDir(rec),
		Hostname:   strings.TrimSpace(rec.Hostname),
		MapName:    strings.TrimSpace(rec.MapName),
		Players:    rec.Players,
		MaxPlayers: rec.MaxPlayers,
		State:      "stopped",
		LastError:  rec.lastError,
	}
	if s != nil {
		snap.Line = s.Line
		if snap.Hostname == "" {
			snap.Hostname = s.DisplayHostname
		}
		if snap.MapName == "" {
			snap.MapName = s.DisplayMap
		}
		if snap.GameDir == "" {
			snap.GameDir = s.DisplayGameDir
		}
	}
	if !m.instanceRunningLocked(rec) {
		if snap.LastError != "" {
			snap.State = "crashed"
		}
		return snap
	}
	snap.State = "running"
	if rec.awaitingServerInfo {
		snap.State = "starting"
	}
	if running := rec.Running; running != nil && running.Cmd != nil && running.Cmd.Process != nil {
		snap.PID = running.Cmd.Process.Pid
	}
	return snap
}

// Snapshots returns a point-in-time view of all managed servers.
func (m *ServerManager) Snapshots() []ServerSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]ServerSnapshot, 0, len(m.serversByID))
	for _, s := range m.serversLocked() {
		out = append(out, m.buildServerSnapshotLocked(s))
	}
	return out
}

// InstanceSnapshots returns a point-in-time view of instance servers.
// target=0 includes every s; positive targets resolve as a server index.
func (m *ServerManager) InstanceSnapshots(target int) ([]ServerSnapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var servers []*server
	if target > 0 {
		s, err := m.findServerByIndexLocked(target)
		if err != nil {
			return nil, err
		}
		servers = []*server{s}
	} else {
		servers = m.serversLocked()
	}

	out := make([]ServerSnapshot, 0, len(m.instancesByID))
	for _, s := range servers {
		for _, rec := range m.serverInstancesLocked(s) {
			out = append(out, m.buildInstanceSnapshotLocked(s, rec))
		}
	}
	return out, nil
}
