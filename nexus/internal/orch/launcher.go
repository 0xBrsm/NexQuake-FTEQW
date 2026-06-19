package orch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/0xBrsm/NexQuake/nexus/internal/assets"
	"github.com/creack/pty"
)

const serverStartupCCREPTimeout = 10 * time.Second

func resetRecordStartupState(rec *instance) {
	rec.relayConsoleReady = false
	rec.awaitingServerInfo = false
	rec.startupTimedOutOnce = false
}

// StartAll launches all servers from the servers.ini plan.
func (m *ServerManager) StartAll() error {
	if m.gameDir == "" {
		return fmt.Errorf("GAME_DIR is empty")
	}
	if st, err := os.Stat(m.gameDir); err != nil || !st.IsDir() {
		return fmt.Errorf("GAME_DIR is not a directory: %s", m.gameDir)
	}
	if m.logsDir == "" {
		return fmt.Errorf("LOGS_DIR is empty")
	}
	if err := os.MkdirAll(m.logsDir, 0o755); err != nil {
		return fmt.Errorf("failed to create LOGS_DIR: %w", err)
	}

	launches, mods, err := m.planLaunches()
	if err != nil {
		return err
	}
	slog.Info(fmt.Sprintf("Launching %d servers...", len(launches)))

	runtimeBasedir, stopOverlay, err := assets.PrepareRuntimeBasedir(m.gameDir, mods)
	if err != nil {
		return err
	}
	m.runtimeBasedir = runtimeBasedir
	m.stopOverlay = stopOverlay

	m.mu.Lock()
	m.instancesByID = make(map[int]*instance, len(launches))
	m.instanceIDsByPort = make(map[int][]int, len(launches))
	m.nextInstanceID = 0
	m.resetServerRegistryLocked()
	m.mu.Unlock()

	for _, launch := range launches {
		rec, err := m.registerServerLaunch(launch)
		if err != nil {
			_ = m.StopAll(context.Background(), 2*time.Second)
			return err
		}
		if err := m.startRecord(rec); err != nil {
			_ = m.StopAll(context.Background(), 2*time.Second)
			return err
		}
	}

	return nil
}

func (m *ServerManager) startRecord(rec *instance) error {
	if rec == nil {
		return fmt.Errorf("server record not found")
	}

	m.mu.Lock()
	if m.runtimeBasedir == "" {
		m.mu.Unlock()
		return fmt.Errorf("runtime not initialized")
	}
	if m.instanceRunningLocked(rec) {
		m.mu.Unlock()
		return errAlreadyRunning
	}
	runtimeBasedir := m.runtimeBasedir
	launch := cloneServerLaunch(rec.Launch)

	// Clear stale resolved-port state from any previous run so that
	// assignPortLocked will accept the new port the OS hands out
	// (critical for -port 0 servers that get a different ephemeral port
	// each time they start).
	m.removeServerIDFromPortLocked(rec.resolvedPort, rec.id)
	if rec.spec != nil {
		m.removeServerIDFromPortLocked(rec.spec.ListenPort, rec.id)
	}
	rec.resolvedPortKnown = false
	rec.resolvedPort = 0
	rec.resolvedSearchPath = nil
	rec.spec = nil
	m.mu.Unlock()

	srv, err := m.startServer(
		runtimeBasedir,
		launch,
		func(port int) {
			m.updatePort(rec, port)
			slog.Debug(fmt.Sprintf("Resolved server %s port: %d", m.serverConsoleLabel(rec), port))
		},
		func(searchPath []string) {
			normalized := normalizeSearchPath(searchPath)
			m.updateSearchPathNormalized(rec, normalized)
			slog.Debug(fmt.Sprintf("Resolved server search path (%s active=%q paths=%q)",
				m.serverConsoleLabel(rec), activeGameDir(normalized), normalized))
		},
	)
	if err != nil {
		m.mu.Lock()
		rec.Running = nil
		resetRecordStartupState(rec)
		rec.lastError = err.Error()
		m.mu.Unlock()
		return err
	}

	m.mu.Lock()
	rec.Running = srv
	rec.relayConsoleReady = false
	rec.awaitingServerInfo = true
	rec.startupTimedOutOnce = false
	rec.lastError = ""
	rec.Hostname = ""
	rec.MapName = ""
	rec.Players = 0
	rec.MaxPlayers = 0
	rec.LastSeen = time.Time{}
	m.mu.Unlock()
	m.resetServerInstanceState(rec.id)

	go m.relayServerConsoleToNexus(rec, srv.Console)
	go m.monitorServerStartupTimeout(rec, srv, serverStartupCCREPTimeout)

	return nil
}

func (m *ServerManager) monitorServerStartupTimeout(rec *instance, srv *managedServer, timeout time.Duration) {
	if rec == nil || srv == nil || timeout <= 0 {
		return
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	<-timer.C

	m.mu.Lock()
	if rec.Running != srv || !rec.awaitingServerInfo || rec.startupTimedOutOnce {
		m.mu.Unlock()
		return
	}
	rec.startupTimedOutOnce = true
	label := m.serverConsoleLabelLocked(rec)
	m.mu.Unlock()

	slog.Warn(fmt.Sprintf("server %s failed to start in %d seconds", label, int(timeout/time.Second)))
}

func (m *ServerManager) startServer(runtimeBasedir string, launch serverLaunch, onPort func(int), onSearchPath func([]string)) (*managedServer, error) {
	logDirName := launch.LogDir
	logDir := filepath.Join(m.logsDir, logDirName)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", logDir, err)
	}

	logPath := filepath.Join(logDir, "server.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", logPath, err)
	}

	cmd := exec.Command(launch.Binary, launch.Args...)
	cmd.Dir = runtimeBasedir
	launchLabel := formatLaunchLabel(launch)

	ptyParent, ptyChild, err := pty.Open()
	if err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("open pty for server %s bin=%q: %w", launchLabel, launch.Binary, err)
	}
	cmd.Stdin = ptyChild
	cmd.Stdout = ptyChild
	cmd.Stderr = ptyChild

	slog.Debug(fmt.Sprintf("Starting server %s: %s %s", launchLabel, launch.Binary, strings.Join(launch.Args, " ")))

	if err := cmd.Start(); err != nil {
		_ = ptyParent.Close()
		_ = ptyChild.Close()
		_ = logFile.Close()
		return nil, fmt.Errorf("start server %s bin=%q: %w", launchLabel, launch.Binary, err)
	}

	_ = ptyChild.Close()

	console := newServerConsole(ptyParent)
	srv := &managedServer{
		Cmd:     cmd,
		Console: console,
		done:    make(chan error, 1),
	}

	formatLogLine := m.formatLogLine
	go func() {
		go monitorServerStartup(console, onPort, onSearchPath)

		copyDone := make(chan struct{})
		go func() {
			console.run(logFile, formatLogLine)
			close(copyDone)
		}()

		err := cmd.Wait()
		_ = ptyParent.Close()
		<-copyDone
		srv.done <- err
		_ = logFile.Close()
	}()

	time.Sleep(50 * time.Millisecond)
	if !isProcessAlive(cmd.Process) {
		err := <-srv.done
		if err == nil {
			err = errors.New("process exited")
		}
		return nil, fmt.Errorf("server %s bin=%q exited immediately: %w (see %s)", launchLabel, launch.Binary, err, logPath)
	}

	return srv, nil
}

// StopAll gracefully stops all running servers.
func (m *ServerManager) StopAll(ctx context.Context, killAfter time.Duration) error {
	var errs []error
	running := m.runningServers()

	for _, entry := range running {
		if p := entry.srv.Cmd; p != nil && p.Process != nil && isProcessAlive(p.Process) {
			_ = p.Process.Signal(syscall.SIGTERM)
		}
	}

	for _, entry := range running {
		if err := m.stopServer(ctx, entry.rec, entry.srv, killAfter, false); err != nil {
			errs = append(errs, err)
		}
	}

	if m.stopOverlay != nil {
		m.stopOverlay()
		m.stopOverlay = nil
	}
	if m.runtimeBasedir != "" {
		_ = os.RemoveAll(m.runtimeBasedir)
		m.runtimeBasedir = ""
	}

	m.closeServerRegistry()

	return errors.Join(errs...)
}

func (m *ServerManager) stopServer(ctx context.Context, rec *instance, s *managedServer, killAfter time.Duration, sendSignal bool) error {
	if s == nil {
		return nil
	}
	waitAfterSignal := killAfter

	clearRunning := func(waitErr error) error {
		m.mu.Lock()
		if rec != nil && rec.Running == s {
			rec.Running = nil
			resetRecordStartupState(rec)
			rec.lastError = ""
			if owner := m.serverByInstanceID[rec.id]; owner != nil {
				m.refreshServerSnapshotLocked(owner)
			}
		}
		m.mu.Unlock()
		// The caller asked for this process to stop; any exit — clean or
		// signal-induced — is the success they wanted. cmd.Wait() returns
		// *exec.ExitError with "signal: terminated"/"signal: killed" when the
		// child dies from SIGTERM/SIGKILL we sent, which is expected here.
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return nil
		}
		return waitErr
	}

	if sendSignal && s.Cmd != nil && s.Cmd.Process != nil && isProcessAlive(s.Cmd.Process) {
		if s.Console != nil {
			_ = s.Console.writeCommand("quit")
		}

		quitGrace := 750 * time.Millisecond
		if killAfter > 0 && killAfter < quitGrace {
			quitGrace = killAfter
		}
		waitAfterSignal = max(0, killAfter-quitGrace)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-s.done:
			return clearRunning(err)
		case <-time.After(quitGrace):
		}

		if isProcessAlive(s.Cmd.Process) {
			_ = s.Cmd.Process.Signal(syscall.SIGTERM)
		}
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-s.done:
		return clearRunning(err)
	case <-time.After(waitAfterSignal):
		if s.Cmd != nil && s.Cmd.Process != nil && isProcessAlive(s.Cmd.Process) {
			_ = s.Cmd.Process.Kill()
		}
		select {
		case err := <-s.done:
			return clearRunning(err)
		case <-time.After(1 * time.Second):
			label := "server"
			gameDir := ""
			m.mu.Lock()
			if rec != nil {
				if rec.Running == s {
					rec.lastError = "did not exit after kill"
				}
				label = m.serverConsoleLabelLocked(rec)
				if rec.spec != nil {
					gameDir = activeGameDir(rec.spec.SearchPath)
				}
			}
			m.mu.Unlock()
			return fmt.Errorf("server %s mod=%q did not exit after kill", label, gameDir)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func isProcessAlive(p *os.Process) bool {
	if p == nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
