package orch

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newRunningInstanceForTest(t *testing.T, m *ServerManager, line, port int, args []string, players, maxPlayers byte) *instance {
	t.Helper()

	rec := m.registerBareInstance(serverLaunch{
		Line:   line,
		Binary: "nqserver",
		Args:   append([]string(nil), args...),
	})
	m.updatePort(rec, port)
	m.updateSearchPathNormalized(rec, normalizeSearchPath([]string{"id1"}))
	m.SetServerRunningForTest(rec, NewTestServer(port))
	m.SetServerInfoForTest(rec, "server-"+strconv.Itoa(line), "dm6", players, maxPlayers)
	m.mu.Lock()
	rec.LastSeen = time.Now()
	m.mu.Unlock()
	return rec
}

func attachInstanceForTest(t *testing.T, m *ServerManager, seedID int, instance *instance) {
	t.Helper()

	m.mu.Lock()
	defer m.mu.Unlock()

	s := m.serverByInstanceID[seedID]
	if s == nil {
		t.Fatalf("seed instance %d has no server", seedID)
	}
	if _, ok := m.serverByInstanceID[instance.id]; !ok {
		m.serverByInstanceID[instance.id] = s
	}
	if !slices.Contains(s.InstanceIDs, instance.id) {
		s.InstanceIDs = append(s.InstanceIDs, instance.id)
	}
	if s.instanceStates == nil {
		s.instanceStates = make(map[int]*instanceState)
	}

	if seedState := s.instanceStates[seedID]; seedState == nil {
		s.instanceStates[seedID] = &instanceState{Lifecycle: instanceLifecycleActive}
	} else {
		seedState.Lifecycle = instanceLifecycleActive
		seedState.ZeroPollStreak = 0
	}
	if instanceStates := s.instanceStates[instance.id]; instanceStates == nil {
		s.instanceStates[instance.id] = &instanceState{Lifecycle: instanceLifecycleActive}
	} else {
		instanceStates.Lifecycle = instanceLifecycleActive
		instanceStates.ZeroPollStreak = 0
	}
	m.refreshServerSnapshotLocked(s)
}

func TestRegisterServerSeed_OnlyPortZeroLaunches(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	t.Cleanup(m.closeServerRegistry)

	recDynamic := m.registerBareInstance(serverLaunch{Line: 0, Binary: "nqserver", Args: []string{"-dedicated", "-port", "0"}})
	recStatic := m.registerBareInstance(serverLaunch{Line: 1, Binary: "nqserver", Args: []string{"-dedicated", "-port", "26000"}})

	if err := m.registerServerSeed(recDynamic); err != nil {
		t.Fatalf("register dynamic seed: %v", err)
	}
	if err := m.registerServerSeed(recStatic); err != nil {
		t.Fatalf("register static seed: %v", err)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.serversByID) != 1 {
		t.Fatalf("server count = %d, want 1", len(m.serversByID))
	}
	if m.serverByInstanceID[recDynamic.id] == nil {
		t.Fatalf("expected dynamic instance to be registered")
	}
	if m.serverByInstanceID[recStatic.id] != nil {
		t.Fatalf("expected static server to stay unregistered")
	}
}

func TestRegisterServerSeed_DisablesAutoscalingWhenServerSizeOne(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	m.SetServerMaxInstances(1)
	t.Cleanup(m.closeServerRegistry)

	rec := m.registerBareInstance(serverLaunch{Line: 0, Binary: "nqserver", Args: []string{"-dedicated", "-port", "0"}})
	if err := m.registerServerSeed(rec); err != nil {
		t.Fatalf("register dynamic seed: %v", err)
	}

	m.mu.RLock()
	s := m.serverByInstanceID[rec.id]
	m.mu.RUnlock()
	if s == nil {
		t.Fatalf("expected dynamic instance to be registered")
	}
	if s.Autoscales {
		t.Fatalf("expected SV_MAX_INSTANCES=1 seed to disable autoscaling")
	}
}

func TestRegisterServerSeed_EnablesAutoscalingWhenServerSizeAboveOne(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	m.SetServerMaxInstances(8)
	t.Cleanup(m.closeServerRegistry)

	rec := newRunningInstanceForTest(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 0, 16)
	if err := m.registerServerSeed(rec); err != nil {
		t.Fatalf("register dynamic seed: %v", err)
	}

	m.mu.RLock()
	s := m.serverByInstanceID[rec.id]
	m.mu.RUnlock()
	if s == nil {
		t.Fatalf("expected dynamic instance to be registered")
	}
	if !s.Autoscales {
		t.Fatalf("expected SV_MAX_INSTANCES>1 seed to enable autoscaling")
	}
	m.mu.Lock()
	port, ok := m.pickServerInstanceLocked(s, false)
	m.mu.Unlock()
	if !ok || port != 26000 {
		t.Fatalf("picker port = %d ok=%v, want 26000 true", port, ok)
	}
}

func TestServerRouting_PicksLeastLoadedInstance(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	m.SetServerMaxInstances(10)
	t.Cleanup(m.closeServerRegistry)

	// seed has 8/16 players, replica has 1/16 — replica is less loaded.
	seed := newRunningInstanceForTest(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 8, 16)
	if err := m.registerServerSeed(seed); err != nil {
		t.Fatalf("register server seed: %v", err)
	}
	replica := newRunningInstanceForTest(t, m, 1, 26001, []string{"-dedicated", "-port", "0"}, 1, 16)
	attachInstanceForTest(t, m, seed.id, replica)

	m.mu.Lock()
	s := m.serverByInstanceID[seed.id]
	port, ok := m.pickServerInstanceLocked(s, true)
	m.mu.Unlock()

	if !ok {
		t.Fatalf("expected routed destination")
	}
	if port != 26001 {
		t.Fatalf("routed instance = %d, want 26001 (less loaded)", port)
	}

	// After replica fills up, seed should be selected.
	m.SetServerInfoForTest(replica, replica.Hostname, replica.MapName, 16, 16)

	m.mu.Lock()
	port, ok = m.pickServerInstanceLocked(s, true)
	m.mu.Unlock()

	if !ok {
		t.Fatalf("expected routed destination after replica full")
	}
	if port != 26000 {
		t.Fatalf("routed instance = %d, want 26000 after replica full", port)
	}
}

func TestServerRouting_UsesDrainingInstanceWhenOnlyFreeSlotsRemain(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	m.SetServerMaxInstances(10)
	t.Cleanup(m.closeServerRegistry)

	seed := newRunningInstanceForTest(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 16, 16)
	if err := m.registerServerSeed(seed); err != nil {
		t.Fatalf("register server seed: %v", err)
	}
	replica := newRunningInstanceForTest(t, m, 1, 26001, []string{"-dedicated", "-port", "0"}, 0, 16)
	attachInstanceForTest(t, m, seed.id, replica)

	m.mu.Lock()
	s := m.serverByInstanceID[replica.id]
	state := s.instanceStates[replica.id]
	if state == nil {
		state = &instanceState{Lifecycle: instanceLifecycleActive}
		s.instanceStates[replica.id] = state
	}
	state.Lifecycle = instanceLifecycleDraining
	replicaDraining := state != nil && state.Lifecycle == instanceLifecycleDraining
	routed, ok := m.pickServerInstanceLocked(s, true)
	m.mu.Unlock()

	if !replicaDraining {
		t.Fatalf("expected replica to be marked draining")
	}
	if !ok {
		t.Fatalf("expected routed destination")
	}
	if routed != 26001 {
		t.Fatalf("routed instance = %d, want 26001", routed)
	}
}

func TestServerRouting_PrefersActiveLowerLoadOverDraining(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	m.SetServerMaxInstances(10)
	t.Cleanup(m.closeServerRegistry)

	// seed active with 8/16, replica draining with 0/16.
	// Active instance with free slots should be preferred.
	seed := newRunningInstanceForTest(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 8, 16)
	if err := m.registerServerSeed(seed); err != nil {
		t.Fatalf("register server seed: %v", err)
	}
	replica := newRunningInstanceForTest(t, m, 1, 26001, []string{"-dedicated", "-port", "0"}, 0, 16)
	attachInstanceForTest(t, m, seed.id, replica)

	m.mu.Lock()
	s := m.serverByInstanceID[replica.id]
	state := s.instanceStates[replica.id]
	if state == nil {
		state = &instanceState{Lifecycle: instanceLifecycleActive}
		s.instanceStates[replica.id] = state
	}
	state.Lifecycle = instanceLifecycleDraining
	port, ok := m.pickServerInstanceLocked(s, true)
	m.mu.Unlock()

	if !ok {
		t.Fatalf("expected routed destination")
	}
	if port != 26000 {
		t.Fatalf("routed instance = %d, want 26000 (active over draining)", port)
	}
}

func TestServerRouting_AllDrainingInstancesRemainRoutable(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	m.SetServerMaxInstances(10)
	t.Cleanup(m.closeServerRegistry)

	seed := newRunningInstanceForTest(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 0, 16)
	if err := m.registerServerSeed(seed); err != nil {
		t.Fatalf("register server seed: %v", err)
	}
	replica := newRunningInstanceForTest(t, m, 1, 26001, []string{"-dedicated", "-port", "0"}, 0, 16)
	attachInstanceForTest(t, m, seed.id, replica)

	m.mu.Lock()
	s := m.serverByInstanceID[seed.id]
	for _, serverID := range s.InstanceIDs {
		state := s.instanceStates[serverID]
		if state == nil {
			state = &instanceState{Lifecycle: instanceLifecycleDraining}
			s.instanceStates[serverID] = state
		}
		state.Lifecycle = instanceLifecycleDraining
		state.ZeroPollStreak = 0
	}
	routed, ok := m.pickServerInstanceLocked(s, true)
	m.mu.Unlock()

	if !ok {
		t.Fatalf("expected routed destination")
	}
	if routed != 26000 && routed != 26001 {
		t.Fatalf("routed instance = %d, want one of [26000 26001]", routed)
	}
}

func TestServerRouting_SkipsWarmingInstanceForNewSessions(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	m.SetServerMaxInstances(10)
	t.Cleanup(m.closeServerRegistry)

	seed := newRunningInstanceForTest(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 4, 16)
	if err := m.registerServerSeed(seed); err != nil {
		t.Fatalf("register server seed: %v", err)
	}
	replica := newRunningInstanceForTest(t, m, 1, 26001, []string{"-dedicated", "-port", "0"}, 0, 16)
	attachInstanceForTest(t, m, seed.id, replica)

	m.mu.Lock()
	s := m.serverByInstanceID[replica.id]
	state := s.instanceStates[replica.id]
	if state == nil {
		state = &instanceState{Lifecycle: instanceLifecycleWarming}
		s.instanceStates[replica.id] = state
	}
	state.Lifecycle = instanceLifecycleWarming
	routed, ok := m.pickServerInstanceLocked(s, true)
	m.mu.Unlock()

	if !ok {
		t.Fatalf("expected routed destination")
	}
	if routed != 26000 {
		t.Fatalf("routed instance = %d, want 26000 while replica is warming", routed)
	}
}

func TestServerRouting_RecordsDemandOnSlist(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	m.SetServerMaxInstances(10)
	t.Cleanup(m.closeServerRegistry)

	seed := newRunningInstanceForTest(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 4, 16)
	if err := m.registerServerSeed(seed); err != nil {
		t.Fatalf("register server seed: %v", err)
	}
	attachInstanceForTest(t, m, seed.id, seed)

	// No slist yet — demand should be zero.
	m.mu.RLock()
	s := m.serverByInstanceID[seed.id]
	beforeDemand := len(s.joinDemandAt)
	m.mu.RUnlock()
	if beforeDemand != 0 {
		t.Fatalf("demand before slist = %d, want 0", beforeDemand)
	}

	// Slist records one demand event per successful instance pick.
	entries := snapshotForSlist(m)
	if len(entries) == 0 {
		t.Fatalf("expected at least one slist entry")
	}

	m.mu.RLock()
	afterDemand := len(s.joinDemandAt)
	m.mu.RUnlock()
	if afterDemand != 1 {
		t.Fatalf("demand after slist = %d, want 1", afterDemand)
	}
}

func TestServerRouting_ServerSizeOneHidesInstancesAndSkipsDemand(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	m.SetServerMaxInstances(1)
	t.Cleanup(m.closeServerRegistry)

	seed := newRunningInstanceForTest(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 4, 16)
	if err := m.registerServerSeed(seed); err != nil {
		t.Fatalf("register server seed: %v", err)
	}

	m.mu.RLock()
	s := m.serverByInstanceID[seed.id]
	m.mu.RUnlock()
	if s == nil {
		t.Fatalf("expected server for dynamic seed")
	}
	if s.Autoscales {
		t.Fatalf("expected SV_MAX_INSTANCES=1 to disable autoscaling")
	}

	entries := snapshotForSlist(m)
	if len(entries) != 1 {
		t.Fatalf("slist entries = %d, want 1", len(entries))
	}
	if entries[0].Instances != 0 {
		t.Fatalf("instances = %d, want 0 when autoscaling disabled", entries[0].Instances)
	}

	m.mu.RLock()
	demandCount := len(s.joinDemandAt)
	m.mu.RUnlock()
	if demandCount != 0 {
		t.Fatalf("disabled autoscaling should not record demand, got %d", demandCount)
	}
}

func TestSnapshotForSlist_ServerSizeOnePortZeroLaunchUsesObservedInstance(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	m.SetServerMaxInstances(1)
	t.Cleanup(m.closeServerRegistry)

	rec, err := m.registerServerLaunch(serverLaunch{
		Line:   0,
		Binary: "nqserver",
		Args:   []string{"-dedicated", "-port", "0"},
	})
	if err != nil {
		t.Fatalf("register server launch: %v", err)
	}

	m.updatePort(rec, 26000)
	m.updateSearchPathNormalized(rec, normalizeSearchPath([]string{"ffa", "id1"}))
	m.SetServerRunningForTest(rec, NewTestServer(26000))
	m.SetServerInfoForTest(rec, "ffa", "start", 3, 16)
	m.mu.Lock()
	rec.LastSeen = time.Now()
	s := m.serverByInstanceID[rec.id]
	m.refreshServerSnapshotLocked(s)
	m.mu.Unlock()

	if s == nil {
		t.Fatalf("expected server for dynamic launch")
	}
	if s.Autoscales {
		t.Fatalf("expected SV_MAX_INSTANCES=1 launch to disable autoscaling")
	}

	entries := snapshotForSlist(m)
	if len(entries) != 1 {
		t.Fatalf("slist entries = %d, want 1", len(entries))
	}
	if entries[0].ListenPort != 26000 {
		t.Fatalf("listen port = %d, want 26000", entries[0].ListenPort)
	}
	if entries[0].GameDir != "ffa" {
		t.Fatalf("game dir = %q, want ffa", entries[0].GameDir)
	}
	if entries[0].Instances != 0 {
		t.Fatalf("instances = %d, want 0 when autoscaling disabled", entries[0].Instances)
	}
}

func TestSnapshotForSlist_StaticPortHidesInstances(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	t.Cleanup(m.closeServerRegistry)

	rec, err := m.registerServerLaunch(serverLaunch{
		Line:   0,
		Binary: "nqserver",
		Args:   []string{"-dedicated", "-port", "26000"},
	})
	if err != nil {
		t.Fatalf("register fixed-port launch: %v", err)
	}
	m.updatePort(rec, 26000)
	m.updateSearchPathNormalized(rec, normalizeSearchPath([]string{"id1"}))
	m.SetServerRunningForTest(rec, NewTestServer(26000))
	m.SetServerInfoForTest(rec, "fixed", "dm6", 3, 16)
	m.mu.Lock()
	rec.LastSeen = time.Now()
	s := m.serverByInstanceID[rec.id]
	m.refreshServerSnapshotLocked(s)
	m.mu.Unlock()

	if s == nil {
		t.Fatalf("expected fixed-port launch to have a server record")
	}
	if s.Autoscales {
		t.Fatalf("expected fixed-port launch to stay non-autoscaling")
	}

	entries := snapshotForSlist(m)
	if len(entries) != 1 {
		t.Fatalf("slist entries = %d, want 1", len(entries))
	}
	if entries[0].Instances != 0 {
		t.Fatalf("instances = %d, want 0 for fixed-port launch", entries[0].Instances)
	}
}

func TestServerRouting_FailedSlistDoesNotRecordDemand(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	t.Cleanup(m.closeServerRegistry)

	// Register s seed but don't set it running — no routable instance.
	seed := m.registerBareInstance(serverLaunch{Line: 0, Binary: "nqserver", Args: []string{"-dedicated", "-port", "0"}})
	if err := m.registerServerSeed(seed); err != nil {
		t.Fatalf("register server seed: %v", err)
	}

	entries := snapshotForSlist(m)
	if len(entries) != 0 {
		t.Fatalf("expected no slist entries with no running instances, got %d", len(entries))
	}

	m.mu.RLock()
	s := m.serverByInstanceID[seed.id]
	demandCount := len(s.joinDemandAt)
	m.mu.RUnlock()
	if demandCount != 0 {
		t.Fatalf("failed slist should not record demand, got %d", demandCount)
	}
}

func TestResolveRCONTargetPort_DirectInstancePort(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	t.Cleanup(m.closeServerRegistry)

	seed := newRunningInstanceForTest(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 0, 16)
	if err := m.registerServerSeed(seed); err != nil {
		t.Fatalf("register server seed: %v", err)
	}

	// RCON to the actual instance port works directly — no proxy resolution.
	_, err := m.DispatchInstanceCmd(26000, "status", "")
	// Server has no real console so we expect "server console unavailable", not "unknown server instance".
	if err == nil {
		t.Fatalf("expected error (no console), got nil")
	}
	if strings.Contains(err.Error(), "unknown server instance") {
		t.Fatalf("DispatchInstanceCmd should find instance 26000, got: %v", err)
	}

	// Out-of-range port should return unknown server instance.
	if _, err := m.DispatchInstanceCmd(0, "status", ""); err == nil || !strings.Contains(err.Error(), "unknown server instance") {
		t.Fatalf("DispatchInstanceCmd invalid port = %v, want unknown server instance", err)
	}

	// Unknown valid port should return unknown server instance.
	if _, err := m.DispatchInstanceCmd(9999, "status", ""); err == nil || !strings.Contains(err.Error(), "unknown server instance") {
		t.Fatalf("DispatchInstanceCmd unknown port = %v, want unknown server instance", err)
	}

}

func TestInstanceSnapshots_AllIncludesServerLinePerInstance(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	m.SetServerMaxInstances(10)
	t.Cleanup(m.closeServerRegistry)

	seed := newRunningInstanceForTest(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 1, 16)
	if err := m.registerServerSeed(seed); err != nil {
		t.Fatalf("register server seed: %v", err)
	}
	replica := newRunningInstanceForTest(t, m, 1, 26001, []string{"-dedicated", "-port", "0"}, 0, 16)
	attachInstanceForTest(t, m, seed.id, replica)

	snaps, err := m.InstanceSnapshots(0)
	if err != nil {
		t.Fatalf("InstanceSnapshots(all) error = %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("InstanceSnapshots(all) len = %d, want 2", len(snaps))
	}
	if snaps[0].Line != 0 || snaps[0].ListenPort != 26000 {
		t.Fatalf("first instance snapshot = %+v, want line 0 port 26000", snaps[0])
	}
	if snaps[1].Line != 0 || snaps[1].ListenPort != 26001 {
		t.Fatalf("second instance snapshot = %+v, want line 0 port 26001", snaps[1])
	}
}

func setServerDemandEventsForTest(t *testing.T, m *ServerManager, seedServerID int, count int, now time.Time) {
	t.Helper()

	m.mu.Lock()
	defer m.mu.Unlock()

	s := m.serverByInstanceID[seedServerID]
	if s == nil {
		t.Fatalf("seed instance %d has no server", seedServerID)
	}
	s.joinDemandAt = s.joinDemandAt[:0]
	for i := 0; i < count; i++ {
		s.joinDemandAt = append(s.joinDemandAt, now)
	}
}

func TestServerScaleUp_UsesDemandHeadroom(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	m.SetServerMaxInstances(10)
	t.Cleanup(m.closeServerRegistry)

	seed := newRunningInstanceForTest(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 10, 16)
	if err := m.registerServerSeed(seed); err != nil {
		t.Fatalf("register server seed: %v", err)
	}
	// Aggregate headroom is 22, demand is low, so no scale-up.
	replica := newRunningInstanceForTest(t, m, 1, 26001, []string{"-dedicated", "-port", "0"}, 0, 16)
	attachInstanceForTest(t, m, seed.id, replica)
	setServerDemandEventsForTest(t, m, seed.id, 2, time.Now())

	m.mu.Lock()
	s := m.serverByInstanceID[seed.id]
	scaleUpServerID, _ := m.decideServerActionsLocked(s, time.Now())
	m.mu.Unlock()
	if scaleUpServerID != -1 {
		t.Fatalf("scale-up server id = %d, want no scale-up", scaleUpServerID)
	}
}

func TestServerScaleUp_TriggersOnDemandHeadroom(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	m.SetServerMaxInstances(10)
	t.Cleanup(m.closeServerRegistry)

	seed := newRunningInstanceForTest(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 14, 16)
	if err := m.registerServerSeed(seed); err != nil {
		t.Fatalf("register server seed: %v", err)
	}
	replica := newRunningInstanceForTest(t, m, 1, 26001, []string{"-dedicated", "-port", "0"}, 13, 16)
	attachInstanceForTest(t, m, seed.id, replica)
	// 20 joins in a 30s window => needed headroom ~= 12 slots.
	setServerDemandEventsForTest(t, m, seed.id, 20, time.Now())

	m.mu.Lock()
	s := m.serverByInstanceID[seed.id]
	scaleUpServerID, _ := m.decideServerActionsLocked(s, time.Now())
	scalingUp := s.ScaleUpInFlight
	m.mu.Unlock()

	if scaleUpServerID != s.ServerID {
		t.Fatalf("scale-up server id = %d, want %d", scaleUpServerID, s.ServerID)
	}
	if !scalingUp {
		t.Fatalf("expected server to enter scaling-up lifecycle")
	}
}

func TestServerScaleUp_HonorsServerSizeCap(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	m.SetServerMaxInstances(1)
	t.Cleanup(m.closeServerRegistry)

	seed := newRunningInstanceForTest(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 13, 16)
	if err := m.registerServerSeed(seed); err != nil {
		t.Fatalf("register server seed: %v", err)
	}
	setServerDemandEventsForTest(t, m, seed.id, 10, time.Now())

	m.mu.Lock()
	s := m.serverByInstanceID[seed.id]
	scaleUpServerID, _ := m.decideServerActionsLocked(s, time.Now())
	m.mu.Unlock()

	if scaleUpServerID != -1 {
		t.Fatalf("scale-up server id = %d, want no scale-up with SV_MAX_INSTANCES=1", scaleUpServerID)
	}
}

func TestServerScaleUp_NoScaleUpWhileWarmingNoCapacityInfo(t *testing.T) {
	// Seed is running but hasn't yet received server info (MaxPlayers==0, LastSeen
	// zero). Scale-up must not fire based on the apparent-but-spurious 0 free slots.
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	m.SetServerMaxInstances(10) // allow scale-up so only the AggregateMaxUsers guard blocks it
	t.Cleanup(m.closeServerRegistry)

	seed := m.registerBareInstance(serverLaunch{
		Line: 0, Binary: "nqserver", Args: []string{"-dedicated", "-port", "0"},
	})
	m.updatePort(seed, 26000)
	if err := m.registerServerSeed(seed); err != nil {
		t.Fatalf("register server seed: %v", err)
	}
	m.SetServerRunningForTest(seed, NewTestServer(26000))
	// No SetServerInfoForTest / no LastSeen: AggregateMaxUsers remains 0.

	m.mu.Lock()
	s := m.serverByInstanceID[seed.id]
	scaleUpServerID, _ := m.decideServerActionsLocked(s, time.Now())
	m.mu.Unlock()

	if scaleUpServerID != -1 {
		t.Fatalf("scale-up server id = %d, want -1 (no scale-up while seed is warming)", scaleUpServerID)
	}
}

func TestReconcileAllServers_AttemptsScaleUpWithoutServerInfoEvents(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	m.SetServerMaxInstances(10)
	t.Cleanup(m.closeServerRegistry)

	seed := newRunningInstanceForTest(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 16, 16)
	if err := m.registerServerSeed(seed); err != nil {
		t.Fatalf("register server seed: %v", err)
	}

	m.mu.Lock()
	beforeNextServerID := m.nextInstanceID
	s := m.serverByInstanceID[seed.id]
	beforeInstances := len(s.InstanceIDs)
	m.mu.Unlock()

	m.reconcileAllServers()

	m.mu.Lock()
	afterNextServerID := m.nextInstanceID
	s = m.serverByInstanceID[seed.id]
	afterInstances := len(s.InstanceIDs)
	scaleUpInFlight := s.ScaleUpInFlight
	m.mu.Unlock()

	if afterNextServerID != beforeNextServerID+1 {
		t.Fatalf("next server id = %d, want %d (one scale-up attempt)", afterNextServerID, beforeNextServerID+1)
	}
	if afterInstances != beforeInstances {
		t.Fatalf("instance count = %d, want %d after failed start cleanup", afterInstances, beforeInstances)
	}
	if scaleUpInFlight {
		t.Fatalf("scale-up flag should clear after reconcile attempt")
	}
}

func TestServerDrain_DoesNotMarkEmptyInstanceWhenHeadroomIsNeeded(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	m.SetServerMaxInstances(10)
	t.Cleanup(m.closeServerRegistry)

	seed := newRunningInstanceForTest(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 1, 1)
	if err := m.registerServerSeed(seed); err != nil {
		t.Fatalf("register server seed: %v", err)
	}
	replica := newRunningInstanceForTest(t, m, 1, 26001, []string{"-dedicated", "-port", "0"}, 0, 1)
	attachInstanceForTest(t, m, seed.id, replica)

	m.mu.Lock()
	s := m.serverByInstanceID[seed.id]
	scaleUpServerID, despawnServerID := m.decideServerActionsLocked(s, time.Now())
	state := s.instanceStates[replica.id]
	draining := state != nil && state.Lifecycle == instanceLifecycleDraining
	zeroStreak := 0
	if state != nil {
		zeroStreak = state.ZeroPollStreak
	}
	m.mu.Unlock()

	if draining {
		t.Fatalf("replica should remain joinable when server headroom is below target")
	}
	if zeroStreak != 0 {
		t.Fatalf("zero-player streak = %d, want 0", zeroStreak)
	}
	if despawnServerID != -1 {
		t.Fatalf("despawn server id = %d, want no despawn", despawnServerID)
	}
	if scaleUpServerID != s.ServerID {
		t.Fatalf("scale-up server id = %d, want %d", scaleUpServerID, s.ServerID)
	}
}

func TestServerDemand_DecaysOutsideWindow(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	t.Cleanup(m.closeServerRegistry)

	seed := newRunningInstanceForTest(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 4, 16)
	if err := m.registerServerSeed(seed); err != nil {
		t.Fatalf("register server seed: %v", err)
	}
	oldNow := time.Now().Add(-demandWindow - time.Second)
	setServerDemandEventsForTest(t, m, seed.id, 10, oldNow)

	m.mu.Lock()
	s := m.serverByInstanceID[seed.id]
	needed := serverNeededHeadroomLocked(s, time.Now())
	remaining := len(s.joinDemandAt)
	m.mu.Unlock()

	if needed != demandMinFreeSlots {
		t.Fatalf("needed headroom after decay = %d, want %d", needed, demandMinFreeSlots)
	}
	if remaining != 0 {
		t.Fatalf("expected all old demand events pruned, got %d", remaining)
	}
}
