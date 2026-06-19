package orch

import (
	"os"
	"os/exec"
	"slices"
	"testing"
	"time"
)

func TestParsePortConsoleLine(t *testing.T) {
	port, ok := parsePortConsoleLine("\"port\" is \"26000\"")
	if !ok {
		t.Fatalf("expected parse success")
	}
	if port != 26000 {
		t.Fatalf("expected port 26000, got %d", port)
	}

	port, ok = parsePortConsoleLine("\"port\" is \"0\"")
	if !ok {
		t.Fatalf("expected parse success for port 0")
	}
	if port != 0 {
		t.Fatalf("expected port 0, got %d", port)
	}

	if _, ok := parsePortConsoleLine("UDP Initialized"); ok {
		t.Fatalf("unexpected parse success for unrelated line")
	}
	if _, ok := parsePortConsoleLine("\"port\" is \"abc\""); ok {
		t.Fatalf("unexpected parse success for non-numeric port")
	}
	if _, ok := parsePortConsoleLine("\"port\" is \"70000\""); ok {
		t.Fatalf("unexpected parse success for out-of-range port")
	}
}

func TestUpdatePort_AssignsResolvedPortBucket(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)

	rec := m.registerBareInstance(serverLaunch{})

	m.updatePort(rec, 26001)

	if rec.resolvedPort != 26001 {
		t.Fatalf("expected resolved port updated to 26001, got %d", rec.resolvedPort)
	}
	if rec.spec != nil {
		t.Fatalf("expected unresolved spec until search path is also known")
	}
	if got := m.instancesByID[rec.id]; got != rec {
		t.Fatalf("expected record stored by id %d", rec.id)
	}
	ids := m.instanceIDsByPort[26001]
	if len(ids) != 1 || ids[0] != rec.id {
		t.Fatalf("expected port index [id=%d], got %v", rec.id, ids)
	}
}

func TestUpdateSearchPath_FinalizesSpecWhenPortKnown(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)

	rec := &instance{
		Launch:            serverLaunch{Line: 3},
		resolvedPortKnown: true,
		resolvedPort:      26001,
		Running:           &managedServer{},
	}

	m.updateSearchPathNormalized(rec, normalizeSearchPath([]string{"ctf", "id1"}))

	if rec.spec == nil {
		t.Fatalf("expected resolved spec after port and search path are known")
	}
	if rec.spec.Line != 3 {
		t.Fatalf("expected line 3, got %d", rec.spec.Line)
	}
	if rec.spec.ListenPort != 26001 {
		t.Fatalf("expected listen port 26001, got %d", rec.spec.ListenPort)
	}
	if !slices.Equal(rec.spec.SearchPath, []string{"ctf", "id1"}) {
		t.Fatalf("expected resolved search path [ctf id1], got %v", rec.spec.SearchPath)
	}
}

func TestUpdateSearchPath_DedupesConsolePathEntries(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)

	rec := &instance{
		Launch:            serverLaunch{Line: 7},
		resolvedPortKnown: true,
		resolvedPort:      26007,
	}

	m.updateSearchPathNormalized(rec, normalizeSearchPath([]string{"arena", "arena", "id1", "id1", "id1"}))

	want := []string{"arena", "id1"}
	if !slices.Equal(rec.resolvedSearchPath, want) {
		t.Fatalf("expected deduped search path %v, got %v", want, rec.resolvedSearchPath)
	}
	if rec.spec == nil || !slices.Equal(rec.spec.SearchPath, want) {
		t.Fatalf("expected deduped spec search path %v, got %+v", want, rec.spec)
	}
}

func TestUpdateSearchPath_IgnoresPathFragmentsAndPakNames(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)

	rec := &instance{
		Launch:            serverLaunch{Line: 8},
		resolvedPortKnown: true,
		resolvedPort:      26008,
	}

	m.updateSearchPathNormalized(rec, normalizeSearchPath([]string{"arena", "arena/pak0.pak", "/tmp/id1", "id1"}))

	want := []string{"arena", "id1"}
	if !slices.Equal(rec.resolvedSearchPath, want) {
		t.Fatalf("expected sanitized search path %v, got %v", want, rec.resolvedSearchPath)
	}
	if rec.spec == nil || !slices.Equal(rec.spec.SearchPath, want) {
		t.Fatalf("expected sanitized spec search path %v, got %+v", want, rec.spec)
	}
}

func TestLaunchServer_RegistersNewEntry(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	m.runtimeBasedir = t.TempDir()

	if err := m.LaunchServer("definitely-not-a-real-binary", []string{"-dedicated"}); err == nil {
		t.Fatalf("expected launch error for missing binary")
	}

	snaps := m.Snapshots()
	if len(snaps) != 1 {
		t.Fatalf("expected one registered server snapshot, got %d", len(snaps))
	}
	if snaps[0].Line != 0 {
		t.Fatalf("expected launched server to get line 0, got %d", snaps[0].Line)
	}
	if snaps[0].State != "crashed" {
		t.Fatalf("expected crashed state after failed start, got %q", snaps[0].State)
	}
}

func TestRemoveServer_RemovesStoppedRecordByIndex(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)

	recA, err := m.registerServerLaunch(serverLaunch{
		Line:   0,
		Binary: "nqserver",
		Args:   []string{"-dedicated", "-port", "26001"},
	})
	if err != nil {
		t.Fatalf("register server A: %v", err)
	}
	m.updatePort(recA, 26001)
	m.updateSearchPathNormalized(recA, normalizeSearchPath([]string{"id1"}))

	recB, err := m.registerServerLaunch(serverLaunch{
		Line:   1,
		Binary: "nqserver",
		Args:   []string{"-dedicated", "-port", "26002"},
	})
	if err != nil {
		t.Fatalf("register server B: %v", err)
	}
	m.updatePort(recB, 26002)
	m.updateSearchPathNormalized(recB, normalizeSearchPath([]string{"ctf", "id1"}))

	if err := m.RemoveServer(1); err != nil {
		t.Fatalf("RemoveServer(index) error = %v", err)
	}

	if _, ok := m.instancesByID[recA.id]; ok {
		t.Fatalf("expected removed record id=%d to be deleted", recA.id)
	}
	if _, ok := m.instancesByID[recB.id]; !ok {
		t.Fatalf("expected other record id=%d to remain", recB.id)
	}
	if _, ok := m.instanceIDsByPort[26001]; ok {
		t.Fatalf("expected removed port bucket 26001 to be deleted, got %v", m.instanceIDsByPort[26001])
	}
	if ids := m.instanceIDsByPort[26002]; len(ids) != 1 || ids[0] != recB.id {
		t.Fatalf("expected remaining port bucket for id=%d, got %v", recB.id, ids)
	}
}

func TestRemoveServer_RunningServerMustBeStoppedFirst(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess(self): %v", err)
	}

	rec, err := m.registerServerLaunch(serverLaunch{
		Line:   0,
		Binary: "nqserver",
		Args:   []string{"-dedicated", "-port", "26000"},
	})
	if err != nil {
		t.Fatalf("register server: %v", err)
	}
	rec.Running = &managedServer{Cmd: &exec.Cmd{Process: process}}

	err = m.RemoveServer(1)
	if err == nil {
		t.Fatalf("expected remove to fail for running server")
	}
	if err.Error() != "server is running; stop server first" {
		t.Fatalf("expected running-server guard, got %q", err.Error())
	}
	if _, ok := m.instancesByID[rec.id]; !ok {
		t.Fatalf("expected running server record to remain registered")
	}
}

func TestRemoveServer_RemovesEveryInstanceFromServer(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)

	seed, err := m.registerServerLaunch(serverLaunch{
		Line:   0,
		Binary: "nqserver",
		Args:   []string{"-dedicated", "-port", "0"},
	})
	if err != nil {
		t.Fatalf("register server launch: %v", err)
	}

	var (
		replicaA   *instance
		replicaB   *instance
		serverID   int
		serverLine int
	)

	m.mu.Lock()
	s := m.serverByInstanceID[seed.id]
	if s == nil {
		m.mu.Unlock()
		t.Fatalf("expected server for seed instance")
	}
	replicaA = m.appendServerInstanceLocked(s, s.TemplateLaunch, instanceLifecycleWarming)
	replicaB = m.appendServerInstanceLocked(s, s.TemplateLaunch, instanceLifecycleWarming)
	serverID = s.ServerID
	serverLine = s.Line
	m.mu.Unlock()

	// Dynamic servers have no stable listen port; remove by 1-based line index.
	if err := m.RemoveServer(serverLine + 1); err != nil {
		t.Fatalf("RemoveServer(server line index) error = %v", err)
	}

	if _, ok := m.instancesByID[seed.id]; ok {
		t.Fatalf("expected seed record id=%d to be removed", seed.id)
	}
	if _, ok := m.instancesByID[replicaA.id]; ok {
		t.Fatalf("expected replica A record id=%d to be removed", replicaA.id)
	}
	if _, ok := m.instancesByID[replicaB.id]; ok {
		t.Fatalf("expected replica B record id=%d to be removed", replicaB.id)
	}
	if _, ok := m.serversByID[serverID]; ok {
		t.Fatalf("expected server id=%d to be removed", serverID)
	}
	if len(m.serverByInstanceID) != 0 {
		t.Fatalf("expected no server member mapping entries, got %d", len(m.serverByInstanceID))
	}
}

func TestServerConsoleLabel_UsesHostnameAndLine(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	rec := &instance{
		id:       7,
		Launch:   serverLaunch{Line: 3},
		Hostname: "fragfest",
	}

	got := m.serverConsoleLabel(rec)
	if got != "4-fragfest" {
		t.Fatalf("expected label 4-fragfest, got %q", got)
	}
}

func TestServerConsoleLabel_FallbacksWhenHostnameMissing(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	rec := &instance{
		id:     5,
		Launch: serverLaunch{Line: -1},
	}

	got := m.serverConsoleLabel(rec)
	if got != "server" {
		t.Fatalf("expected fallback label server, got %q", got)
	}
}

func TestFormatServerConsoleRelayLine_PrefixesAndTrims(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	rec := &instance{
		Launch:   serverLaunch{Line: 2},
		Hostname: "quake-a",
	}

	got, ok := m.formatServerConsoleRelayLine(rec, "hello from server\r\n")
	if !ok {
		t.Fatalf("expected formatted console line")
	}
	if got != "[3-quake-a] hello from server" {
		t.Fatalf("expected prefixed relay line, got %q", got)
	}

	if _, ok := m.formatServerConsoleRelayLine(rec, "\n"); ok {
		t.Fatalf("expected blank line to be ignored")
	}
}

func TestFormatServerConsoleRelayLine_FiltersFindAndPackNoise(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	rec := &instance{
		Launch:   serverLaunch{Line: 1},
		Hostname: "quake-b",
	}

	if _, ok := m.formatServerConsoleRelayLine(rec, "PackFile: id1/pak0.pak : maps/e1m1.bsp\n"); ok {
		t.Fatalf("expected PackFile line to be filtered")
	}
	if _, ok := m.formatServerConsoleRelayLine(rec, "FindFile: maps/e1m1.bsp\n"); ok {
		t.Fatalf("expected successful FindFile line to be filtered")
	}
	if _, ok := m.formatServerConsoleRelayLine(rec, "FindFile: can't find maps/e1m1.ent\n"); ok {
		t.Fatalf("expected missing .ent FindFile line to be filtered")
	}

	got, ok := m.formatServerConsoleRelayLine(rec, "FindFile: can't find progs.dat\n")
	if !ok {
		t.Fatalf("expected FindFile miss to be retained")
	}
	if got != "[2-quake-b] FindFile: can't find progs.dat" {
		t.Fatalf("expected retained FindFile miss with prefix, got %q", got)
	}
}

func TestServerConsoleRelayEnabled_GatedUntilFirstServerInfo(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	rec := m.registerBareInstance(serverLaunch{Line: 2})

	console := newServerConsole(nil, nil)
	srv := &managedServer{Console: console}

	m.mu.Lock()
	rec.Running = srv
	rec.awaitingServerInfo = true
	rec.relayConsoleReady = false
	m.mu.Unlock()

	if m.serverConsoleRelayEnabled(rec, console) {
		t.Fatalf("expected relay to stay disabled before first CCREP")
	}

	m.updatePort(rec, 26002)
	m.updateGameState(26002, "fragfest", "dm6", "id1", 1, 8)

	m.mu.RLock()
	defer m.mu.RUnlock()
	if rec.awaitingServerInfo {
		t.Fatalf("expected first CCREP to clear awaiting state")
	}
	if !rec.relayConsoleReady {
		t.Fatalf("expected first CCREP to enable relay")
	}
}

func TestUpdateServerState_FirstServerInfoWritesOnlineEchoCommand(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	rec := m.registerBareInstance(serverLaunch{Line: 6})

	ptyRead, ptyWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = ptyRead.Close()
		_ = ptyWrite.Close()
	})

	console := newServerConsole(nil, ptyWrite)
	srv := &managedServer{Console: console}

	m.mu.Lock()
	rec.Running = srv
	rec.awaitingServerInfo = true
	rec.relayConsoleReady = false
	m.mu.Unlock()

	m.updatePort(rec, 26006)
	m.updateGameState(26006, "fragfest", "e1m1", "id1", 0, 8)

	buf := make([]byte, 256)
	_ = ptyRead.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	n, err := ptyRead.Read(buf)
	if err != nil {
		t.Fatalf("expected startup echo command write, got read error: %v", err)
	}
	got := string(buf[:n])
	if got != "echo online and accepting clients;\n" {
		t.Fatalf("expected startup echo command, got %q", got)
	}
}

func TestBuildServerSnapshot_UsesServerInfoReadinessForState(t *testing.T) {
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess(self): %v", err)
	}

	rec := &instance{
		Launch:             serverLaunch{Line: 0},
		awaitingServerInfo: true,
		Running: &managedServer{
			Cmd: &exec.Cmd{Process: process},
		},
	}

	snap := buildServerSnapshot(rec)
	if snap.State != "starting" {
		t.Fatalf("expected starting state while awaiting first CCREP, got %q", snap.State)
	}

	rec.awaitingServerInfo = false
	snap = buildServerSnapshot(rec)
	if snap.State != "running" {
		t.Fatalf("expected running state after first CCREP, got %q", snap.State)
	}
}

func TestMonitorServerStartupTimeout_MarksTimedOutWhenStillStarting(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	rec := m.registerBareInstance(serverLaunch{Line: 4})
	srv := &managedServer{}

	m.mu.Lock()
	rec.Running = srv
	rec.awaitingServerInfo = true
	rec.startupTimedOutOnce = false
	m.mu.Unlock()

	m.monitorServerStartupTimeout(rec, srv, 20*time.Millisecond)

	m.mu.RLock()
	defer m.mu.RUnlock()
	if !rec.startupTimedOutOnce {
		t.Fatalf("expected startup timeout marker when still awaiting CCREP")
	}
}

func TestMonitorServerStartupTimeout_IgnoresAlreadyOnlineServer(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	rec := m.registerBareInstance(serverLaunch{Line: 5})
	srv := &managedServer{}

	m.mu.Lock()
	rec.Running = srv
	rec.awaitingServerInfo = false
	rec.startupTimedOutOnce = false
	m.mu.Unlock()

	m.monitorServerStartupTimeout(rec, srv, 20*time.Millisecond)

	m.mu.RLock()
	defer m.mu.RUnlock()
	if rec.startupTimedOutOnce {
		t.Fatalf("expected no startup timeout marker after server is already online")
	}
}
