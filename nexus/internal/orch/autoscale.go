package orch

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"path/filepath"
	"slices"
	"time"
)

type serverInstanceCandidate struct {
	listenPort int
	players    int
	maxPlayers int
}

// SetServerMaxInstances sets the per-server instance cap used by autoscaling.
// Values below 1 are clamped to 1.
func (m *ServerManager) SetServerMaxInstances(size int) {
	if size < 1 {
		size = 1
	}
	m.mu.Lock()
	m.serverMaxInstances = size
	m.mu.Unlock()
}

func (m *ServerManager) serverRoutableCandidatesLocked(s *server, allowDraining bool) []serverInstanceCandidate {
	if s == nil {
		return nil
	}

	out := make([]serverInstanceCandidate, 0, len(s.InstanceIDs))
	for _, serverID := range s.InstanceIDs {
		rec := m.instancesByID[serverID]
		if !m.instanceRunningLocked(rec) {
			continue
		}
		if s.Autoscales {
			state := s.instanceStates[serverID]
			if state == nil {
				continue
			}
			if state.Lifecycle != instanceLifecycleActive &&
				!(allowDraining && state.Lifecycle == instanceLifecycleDraining) {
				continue
			}
		}

		port := recordListenPort(rec)
		if port < 1 || port > 65535 {
			continue
		}

		out = append(out, serverInstanceCandidate{
			listenPort: port,
			players:    int(rec.Players),
			maxPlayers: int(rec.MaxPlayers),
		})
	}

	return out
}

func serverCandidatesWithFreeSlots(candidates []serverInstanceCandidate) []serverInstanceCandidate {
	if len(candidates) == 0 {
		return nil
	}
	out := make([]serverInstanceCandidate, 0, len(candidates))
	for _, cand := range candidates {
		// maxplayers=0 can appear briefly before first poll; treat as usable.
		if cand.maxPlayers <= 0 || cand.players < cand.maxPlayers {
			out = append(out, cand)
		}
	}
	return out
}

func (m *ServerManager) serverRunningCountLocked(s *server) int {
	if s == nil {
		return 0
	}
	count := 0
	for _, serverID := range s.InstanceIDs {
		if m.instanceRunningLocked(m.instancesByID[serverID]) {
			count++
		}
	}
	return count
}

// pickServerInstanceLocked returns the listen port of the next instance a
// new client should be routed to. When advance is true the round-robin cursor
// is advanced (real routing); when false the pick is read-only (snapshot
// display). Returns (0, false) if no routable instance exists.
func (m *ServerManager) pickServerInstanceLocked(s *server, advance bool) (int, bool) {
	candidates := m.serverRoutableCandidatesLocked(s, false)
	if free := serverCandidatesWithFreeSlots(candidates); len(free) > 0 {
		candidates = free
	} else {
		// If every non-draining instance is full (or there are none), also
		// consider draining instances — prefer those with free slots.
		all := m.serverRoutableCandidatesLocked(s, true)
		if freeAll := serverCandidatesWithFreeSlots(all); len(freeAll) > 0 {
			candidates = freeAll
		} else if len(all) > 0 {
			candidates = all
		}
	}

	if len(candidates) == 0 {
		return 0, false
	}

	best := []serverInstanceCandidate{candidates[0]}
	for _, cand := range candidates[1:] {
		bestRef := best[0]
		candMax := max(cand.maxPlayers, 1)
		bestMax := max(bestRef.maxPlayers, 1)
		left := cand.players * bestMax
		right := bestRef.players * candMax
		switch {
		case left < right || (left == right && cand.players < bestRef.players):
			best = best[:1]
			best[0] = cand
		case left == right && cand.players == bestRef.players:
			best = append(best, cand)
		}
	}

	idx := 0
	if len(best) > 1 {
		idx = s.RoundRobinCursor % len(best)
	}
	if advance {
		s.RoundRobinCursor++
	}
	return best[idx].listenPort, true
}

func pruneServerDemandLocked(s *server, now time.Time) {
	if s == nil || len(s.joinDemandAt) == 0 {
		return
	}
	cutoff := now.Add(-demandWindow)
	s.joinDemandAt = slices.DeleteFunc(s.joinDemandAt, func(ts time.Time) bool {
		return ts.Before(cutoff)
	})
}

func noteServerDemandLocked(s *server, now time.Time) {
	if s == nil {
		return
	}
	pruneServerDemandLocked(s, now)
	s.joinDemandAt = append(s.joinDemandAt, now)
}

func serverNeededHeadroomLocked(s *server, now time.Time) int {
	if s == nil {
		return demandMinFreeSlots
	}
	pruneServerDemandLocked(s, now)
	if demandWindow <= 0 {
		return demandMinFreeSlots
	}

	joinRPS := float64(len(s.joinDemandAt)) / demandWindow.Seconds()
	dynamicHeadroom := int(math.Ceil(joinRPS * demandSpawnReady.Seconds() * demandSafetyFactor))
	return max(demandMinFreeSlots, dynamicHeadroom)
}

func (m *ServerManager) decideServerActionsLocked(s *server, now time.Time) (scaleUpServerID int, despawnServerID int) {
	scaleUpServerID = -1
	despawnServerID = -1
	if s == nil {
		return scaleUpServerID, despawnServerID
	}
	if !s.Autoscales {
		m.refreshServerSnapshotLocked(s)
		return scaleUpServerID, despawnServerID
	}

	m.refreshServerSnapshotLocked(s)

	freeSlots := 0
	if s.aggregateMaxUsers > 0 {
		freeSlots = int(s.aggregateMaxUsers) - int(s.aggregateUsers)
		if freeSlots < 0 {
			freeSlots = 0
		}
	}
	neededHeadroom := serverNeededHeadroomLocked(s, now)
	runningCount := int(s.aggregateInstances)
	activeRoutableCount := int(s.joinableInstances)

	for _, serverID := range s.InstanceIDs {
		rec := m.instancesByID[serverID]
		if !m.instanceRunningLocked(rec) {
			continue
		}
		state := m.ensureServerInstanceStateLocked(s, serverID)

		setLifecycle := func(next instanceLifecycle) {
			prev := state.Lifecycle
			if prev == next {
				return
			}
			state.Lifecycle = next
			if prev == instanceLifecycleActive {
				activeRoutableCount--
			}
			if next == instanceLifecycleActive {
				activeRoutableCount++
			}
		}

		if state.Lifecycle == instanceLifecycleTerminating {
			continue
		}
		if rec.LastSeen.IsZero() {
			setLifecycle(instanceLifecycleWarming)
			state.ZeroPollStreak = 0
			continue
		}
		if rec.Players > 0 {
			setLifecycle(instanceLifecycleActive)
			state.ZeroPollStreak = 0
			continue
		}

		instanceFreeSlots := int(rec.MaxPlayers) - int(rec.Players)
		if instanceFreeSlots < 1 {
			// MaxPlayers can be unknown briefly right after startup.
			instanceFreeSlots = 1
		}

		canDrain := activeRoutableCount > 1 && freeSlots-instanceFreeSlots >= neededHeadroom
		if s.ScaleUpInFlight && state.Lifecycle != instanceLifecycleDraining {
			canDrain = false
		}
		if !canDrain {
			setLifecycle(instanceLifecycleActive)
			state.ZeroPollStreak = 0
			continue
		}

		setLifecycle(instanceLifecycleDraining)
		state.ZeroPollStreak++
		if state.ZeroPollStreak >= despawnZeroPolls && despawnServerID < 0 {
			setLifecycle(instanceLifecycleTerminating)
			state.ZeroPollStreak = 0
			despawnServerID = serverID
		}
	}

	if s.aggregateMaxUsers > 0 &&
		freeSlots < neededHeadroom &&
		!s.ScaleUpInFlight &&
		runningCount < max(1, m.serverMaxInstances) &&
		(s.LastScaleUpAt.IsZero() || now.Sub(s.LastScaleUpAt) >= scaleUpCooldown) {
		s.ScaleUpInFlight = true
		scaleUpServerID = s.ServerID
	}

	return scaleUpServerID, despawnServerID
}

func forEachUniqueInt(values []int, fn func(int)) {
	seen := make(map[int]struct{}, len(values))
	for _, value := range values {
		if _, dup := seen[value]; dup {
			continue
		}
		seen[value] = struct{}{}
		fn(value)
	}
}

func (m *ServerManager) reconcileServers(serverIDs []int) {
	if len(serverIDs) == 0 {
		return
	}

	now := time.Now()
	scaleUps := make([]int, 0, len(serverIDs))
	despawns := make([]int, 0, len(serverIDs))

	m.mu.Lock()
	forEachUniqueInt(serverIDs, func(serverID int) {
		s := m.serversByID[serverID]
		if s == nil {
			return
		}
		scaleUpServerID, despawnServerID := m.decideServerActionsLocked(s, now)
		if scaleUpServerID >= 0 {
			scaleUps = append(scaleUps, scaleUpServerID)
		}
		if despawnServerID >= 0 {
			despawns = append(despawns, despawnServerID)
		}
	})
	m.mu.Unlock()

	forEachUniqueInt(scaleUps, m.launchServerReplica)
	forEachUniqueInt(despawns, m.despawnServerInstance)
}

func (m *ServerManager) reconcileAllServers() {
	m.mu.RLock()
	if len(m.serversByID) == 0 {
		m.mu.RUnlock()
		return
	}
	serverIDs := make([]int, 0, len(m.serversByID))
	for serverID := range m.serversByID {
		serverIDs = append(serverIDs, serverID)
	}
	m.mu.RUnlock()
	m.reconcileServers(serverIDs)
}

func (m *ServerManager) launchServerReplica(serverID int) {
	rec, err := m.registerServerReplicaInstance(serverID)
	if err != nil {
		slog.Warn(fmt.Sprintf("Server %d scale-up failed: %v", serverID, err))
		return
	}
	if rec == nil {
		return
	}

	err = m.startRecord(rec)

	m.mu.Lock()
	s := m.serversByID[serverID]
	if s != nil {
		s.ScaleUpInFlight = false
		if err == nil {
			s.LastScaleUpAt = time.Now()
			m.refreshServerSnapshotLocked(s)
		} else {
			m.removeServerRecordLocked(rec.id)
		}
	}
	m.mu.Unlock()

	if err != nil {
		slog.Warn(fmt.Sprintf("Server %d scale-up failed: %v", serverID, err))
		return
	}
	slog.Info(fmt.Sprintf("Server %d launched replica", serverID))
}

func (m *ServerManager) registerServerReplicaInstance(serverID int) (*instance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s := m.serversByID[serverID]
	if s == nil {
		return nil, fmt.Errorf("unknown server")
	}
	if !s.ScaleUpInFlight {
		return nil, nil
	}
	if !s.Autoscales {
		s.ScaleUpInFlight = false
		return nil, nil
	}

	replicaID := m.nextInstanceID
	launch := cloneServerLaunch(s.TemplateLaunch)
	launch.Line = -1
	launch.LogDir = fmt.Sprintf("replica-%d-%s-%s", replicaID, filepath.Base(launch.Binary), time.Now().UTC().Format("20060102T150405Z"))
	launch.Args = forceLaunchPortZero(launch.Args)

	rec := m.appendServerInstanceLocked(s, launch, instanceLifecycleWarming)

	return rec, nil
}

func (m *ServerManager) despawnServerInstance(serverID int) {
	m.mu.Lock()
	s := m.serverByInstanceID[serverID]
	rec := m.instancesByID[serverID]
	if s == nil || rec == nil || !m.instanceRunningLocked(rec) {
		if s != nil {
			m.setServerInstanceLifecycleLocked(s, serverID, instanceLifecycleWarming)
		}
		m.mu.Unlock()
		return
	}
	if m.serverRunningCountLocked(s) <= 1 {
		m.setServerInstanceLifecycleLocked(s, serverID, instanceLifecycleActive)
		m.mu.Unlock()
		return
	}
	srv := rec.Running
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	err := m.stopServer(ctx, rec, srv, 2*time.Second, true)
	cancel()
	if err != nil {
		slog.Warn(fmt.Sprintf("Server %d despawn failed: %v", serverID, err))
	}

	m.mu.Lock()
	if s := m.serverByInstanceID[serverID]; s != nil {
		if err == nil {
			m.removeServerRecordLocked(serverID)
		} else {
			m.setServerInstanceLifecycleLocked(s, serverID, instanceLifecycleActive)
		}
	}
	m.mu.Unlock()
}
