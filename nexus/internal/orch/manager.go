// Package orch manages the lifecycle of dedicated Quake server processes.
//
// The central type is [ServerManager], which owns a set of servers.
// Each server tracks one or more instance processes launched from the same
// servers.ini entry.  Servers whose launch line specifies "-port 0" autoscale:
// the manager spawns additional instance replicas when join demand outpaces
// available capacity, and despawns idle ones when headroom is sufficient.
//
// Typical usage:
//
//	mgr := orch.NewServerManager(gameDir, logsDir, consoleInfof, formatLogLine)
//	mgr.SetServerMaxInstances(cfg.serverMaxInstances)
//	if err := mgr.StartAll(); err != nil { ... }
//
//	stopPoller := mgr.StartInfoPoller(ctx, serverIP)
//	defer stopPoller()
//	...
//	mgr.StopAll(ctx, 2*time.Second)
//
// Severity logging (info/debug/warn/error) flows through the standard
// log/slog default logger; only the two non-severity callbacks — server
// console relay and per-server log-file line formatter — are injected.
package orch

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// ServerManager orchestrates all managed game servers.
// Use [NewServerManager] to construct; the zero value is not usable.
type ServerManager struct {
	gameDir string
	logsDir string

	consoleInfof  func(string, ...any)
	formatLogLine func(string, time.Time) string

	mu                sync.RWMutex
	instancesByID     map[int]*instance
	instanceIDsByPort map[int][]int

	serversByID        map[int]*server
	serverByInstanceID map[int]*server
	serverMaxInstances int
	nextInstanceID     int
	runtimeBasedir     string
	// stopOverlay halts the assets overlay watcher started alongside
	// runtimeBasedir; must be invoked before RemoveAll(runtimeBasedir)
	// so in-flight fsnotify events don't race the teardown.
	stopOverlay func()
}

func (m *ServerManager) serverConsoleLabel(rec *instance) string {
	if rec == nil {
		return "server"
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.serverConsoleLabelLocked(rec)
}

func (m *ServerManager) serverConsoleLabelLocked(rec *instance) string {
	if rec == nil {
		return "server"
	}

	s := m.serverByInstanceID[rec.id]

	hostname := "server"
	if s != nil {
		if name := strings.TrimSpace(s.DisplayHostname); name != "" {
			hostname = name
		}
	}
	if name := strings.TrimSpace(rec.Hostname); name != "" {
		hostname = name
	}

	port := recordListenPort(rec)
	if s != nil && s.Line >= 0 {
		if port > 0 {
			return fmt.Sprintf("%d-%s-%d", s.Line+1, hostname, port)
		}
		return fmt.Sprintf("%d-%s", s.Line+1, hostname)
	}
	if port > 0 {
		return fmt.Sprintf("%s-%d", hostname, port)
	}
	if rec.Launch.Line >= 0 {
		return fmt.Sprintf("%d-%s", rec.Launch.Line+1, hostname)
	}
	return hostname
}

func formatLaunchLabel(launch serverLaunch) string {
	if launch.Line >= 0 {
		return fmt.Sprintf("line=%d", launch.Line+1)
	}
	return "replica"
}

func (m *ServerManager) serverConsoleRelayEnabled(rec *instance, console *serverConsole) bool {
	if rec == nil || console == nil {
		return false
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if rec.Running == nil || rec.Running.Console != console {
		return false
	}
	return rec.relayConsoleReady
}

func shouldRelayServerConsoleLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, consoleSentinelPrefix) {
		return false
	}

	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "packfile:") {
		return false
	}
	if strings.Contains(lower, "findfile: can't find maps/") && strings.HasSuffix(lower, ".ent") {
		return false
	}
	if strings.Contains(lower, "findfile:") && !strings.Contains(lower, "can't find") {
		return false
	}

	return true
}

func (m *ServerManager) formatServerConsoleRelayLine(rec *instance, line string) (string, bool) {
	msg := strings.TrimRight(line, "\r\n")
	if !shouldRelayServerConsoleLine(msg) {
		return "", false
	}
	return fmt.Sprintf("[%s] %s", m.serverConsoleLabel(rec), msg), true
}

func (m *ServerManager) relayServerConsoleToNexus(rec *instance, console *serverConsole) {
	if console == nil {
		return
	}

	lines, cancel := console.subscribeFiltered(1024, func(line string) (string, bool) {
		if console.consumeSuppressedRelayEchoLine(line) {
			return "", false
		}
		if !m.serverConsoleRelayEnabled(rec, console) {
			return "", false
		}
		return m.formatServerConsoleRelayLine(rec, line)
	})
	defer cancel()

	for formatted := range lines {
		m.consoleInfof("%s", formatted)
	}
}

// NewServerManager creates a [ServerManager].
//
//   - gameDir: path to the game data directory (GAME_DIR); must exist before
//     calling [ServerManager.StartAll].
//   - logsDir: directory where per-server log files are written; created on
//     demand by [ServerManager.StartAll].
//   - consoleInfof: relay for server-console output (operator console only,
//     no tail buffer or log file); nil substitutes a no-op.
//   - formatLogLine: formats a raw console line with a timestamp for the
//     on-disk log file; nil substitutes the identity function.
//
// Severity logs (info/debug/warn/error) go through log/slog's default logger.
func NewServerManager(
	gameDir, logsDir string,
	consoleInfof func(string, ...any),
	formatLogLine func(string, time.Time) string,
) *ServerManager {
	if consoleInfof == nil {
		consoleInfof = func(string, ...any) {}
	}
	if formatLogLine == nil {
		formatLogLine = func(line string, _ time.Time) string { return line }
	}
	return &ServerManager{
		gameDir:            gameDir,
		logsDir:            logsDir,
		consoleInfof:       consoleInfof,
		formatLogLine:      formatLogLine,
		instancesByID:      make(map[int]*instance),
		instanceIDsByPort:  make(map[int][]int),
		serversByID:        make(map[int]*server),
		serverByInstanceID: make(map[int]*server),
		serverMaxInstances: defaultServerMaxInstances,
	}
}
