package orch

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestServerAutoscaleStressSimulatedClients(t *testing.T) {
	if os.Getenv("NQNET_STRESS_SERVER_AUTOSCALE") != "1" {
		t.Skip("set NQNET_STRESS_SERVER_AUTOSCALE=1 to run autoscale stress tiers")
	}

	tiers, err := parseServerStressTierList(os.Getenv("NQNET_STRESS_SERVER_AUTOSCALE_TIERS"), []int{100, 1000, 10000})
	if err != nil {
		t.Fatalf("parse NQNET_STRESS_SERVER_AUTOSCALE_TIERS: %v", err)
	}
	if len(tiers) == 0 {
		t.Fatalf("NQNET_STRESS_SERVER_AUTOSCALE_TIERS resolved to no tiers")
	}

	for _, clients := range tiers {
		clients := clients
		t.Run(fmt.Sprintf("%d_clients", clients), func(t *testing.T) {
			runServerAutoscaleStressTier(t, clients)
		})
	}
}

func runServerAutoscaleStressTier(t *testing.T, clientCount int) {
	t.Helper()

	const maxPlayersPerInstance = 16
	neededHeadroom := expectedServerHeadroom(clientCount)
	expectedInstances := int(math.Ceil(float64(neededHeadroom) / float64(maxPlayersPerInstance)))
	if expectedInstances < 1 {
		expectedInstances = 1
	}

	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	m.SetServerMaxInstances(expectedInstances + 4)
	t.Cleanup(m.closeServerRegistry)

	seed := newRunningInstanceForTest(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 0, maxPlayersPerInstance)
	if err := m.registerServerSeed(seed); err != nil {
		t.Fatalf("register server seed: %v", err)
	}

	serverID := serverIdentityForSeed(t, m, seed.id)

	joinMetrics := runConcurrentJoinBurst(t, m, serverID, clientCount)
	if joinMetrics.routeFailures != 0 {
		t.Fatalf("route failures = %d, want 0", joinMetrics.routeFailures)
	}
	if joinMetrics.rewriteFailures != 0 {
		t.Fatalf("rewrite failures = %d, want 0", joinMetrics.rewriteFailures)
	}
	if joinMetrics.recordedDemand != clientCount {
		t.Fatalf("recorded demand = %d, want %d", joinMetrics.recordedDemand, clientCount)
	}

	reconcileMetrics := driveServerScaleUpToDemand(
		t,
		m,
		serverID,
		expectedInstances,
		maxPlayersPerInstance,
	)
	if reconcileMetrics.finalInstances < expectedInstances {
		t.Fatalf("final instances = %d, want at least %d", reconcileMetrics.finalInstances, expectedInstances)
	}
	if reconcileMetrics.finalFreeSlots < reconcileMetrics.neededHeadroom {
		t.Fatalf(
			"final free slots = %d, want >= needed headroom %d",
			reconcileMetrics.finalFreeSlots,
			reconcileMetrics.neededHeadroom,
		)
	}

	t.Logf(
		"autoscale stress: clients=%d server=%d workers=%d join_burst=%s reconcile=%s demand=%d expected_instances=%d final_instances=%d launched=%d iterations=%d",
		clientCount,
		serverID,
		joinMetrics.workers,
		joinMetrics.duration,
		reconcileMetrics.duration,
		joinMetrics.recordedDemand,
		expectedInstances,
		reconcileMetrics.finalInstances,
		reconcileMetrics.launchedReplicas,
		reconcileMetrics.reconcileIterations,
	)
	t.Logf(
		"route latency: avg=%s max=%s p95=%s p99=%s samples=%d peak_joiners=%d",
		joinMetrics.latencyAvg,
		joinMetrics.latencyMax,
		joinMetrics.latencyP95,
		joinMetrics.latencyP99,
		clientCount,
		joinMetrics.peakJoiners,
	)
	t.Logf(
		"final headroom: needed=%d free=%d max_users=%d users=%d",
		reconcileMetrics.neededHeadroom,
		reconcileMetrics.finalFreeSlots,
		reconcileMetrics.finalAggregateMaxUsers,
		reconcileMetrics.finalAggregateUsers,
	)
}

type serverJoinBurstMetrics struct {
	workers        int
	duration       time.Duration
	recordedDemand int
	peakJoiners    int64

	routeFailures   int64
	rewriteFailures int64

	latencyAvg time.Duration
	latencyMax time.Duration
	latencyP95 time.Duration
	latencyP99 time.Duration
}

func runConcurrentJoinBurst(t *testing.T, m *ServerManager, serverID, clientCount int) serverJoinBurstMetrics {
	t.Helper()

	if clientCount <= 0 {
		return serverJoinBurstMetrics{}
	}

	workers := min(clientCount, 1024)
	jobs := make(chan int, clientCount)
	latencies := make(chan int64, clientCount)
	var routeFailures atomic.Int64
	var rewriteFailures atomic.Int64
	var activeJoiners atomic.Int64
	var peakJoiners atomic.Int64

	startedAt := time.Now()
	var wg sync.WaitGroup
	wg.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func() {
			defer wg.Done()
			for range jobs {
				active := activeJoiners.Add(1)
				for {
					prev := peakJoiners.Load()
					if active <= prev || peakJoiners.CompareAndSwap(prev, active) {
						break
					}
				}

				begin := time.Now()
				entries := snapshotForSlist(m)
				latency := time.Since(begin)
				if len(entries) == 0 {
					routeFailures.Add(1)
					activeJoiners.Add(-1)
					continue
				}
				latencyNanos := latency.Nanoseconds()
				if latencyNanos < 1 {
					latencyNanos = 1
				}
				latencies <- latencyNanos

				activeJoiners.Add(-1)
			}
		}()
	}

	for idx := 0; idx < clientCount; idx++ {
		jobs <- idx
	}
	close(jobs)
	wg.Wait()
	close(latencies)

	latencySamples := make([]int64, 0, clientCount)
	for sample := range latencies {
		latencySamples = append(latencySamples, sample)
	}

	m.mu.RLock()
	recordedDemand := 0
	if s := m.serversByID[serverID]; s != nil {
		recordedDemand = len(s.joinDemandAt)
	}
	m.mu.RUnlock()

	latencyAvg, latencyMax, latencyP95, latencyP99 := summarizeNanosPercentiles(latencySamples)

	return serverJoinBurstMetrics{
		workers:         workers,
		duration:        time.Since(startedAt),
		recordedDemand:  recordedDemand,
		peakJoiners:     peakJoiners.Load(),
		routeFailures:   routeFailures.Load(),
		rewriteFailures: rewriteFailures.Load(),
		latencyAvg:      latencyAvg,
		latencyMax:      latencyMax,
		latencyP95:      latencyP95,
		latencyP99:      latencyP99,
	}
}

type serverReconcileMetrics struct {
	duration               time.Duration
	launchedReplicas       int
	reconcileIterations    int
	finalInstances         int
	neededHeadroom         int
	finalFreeSlots         int
	finalAggregateUsers    int
	finalAggregateMaxUsers int
}

func driveServerScaleUpToDemand(
	t *testing.T,
	m *ServerManager,
	serverID int,
	expectedInstances int,
	maxPlayersPerInstance int,
) serverReconcileMetrics {
	t.Helper()

	now := time.Now()
	startedAt := time.Now()
	launched := 0
	iterations := 0
	nextPort := 26001
	maxIterations := max(64, expectedInstances*4)

	for ; iterations < maxIterations; iterations++ {
		var scaleUpServerID int
		var freeSlots int
		var neededHeadroom int
		var runningInstances int
		var maxSize int

		m.mu.Lock()
		s := m.serversByID[serverID]
		if s == nil {
			m.mu.Unlock()
			t.Fatalf("server %d missing", serverID)
		}

		// Accelerate cooldown for simulation so we can validate many replicas
		// without waiting wall-clock 30s per scale-up.
		s.LastScaleUpAt = now.Add(-scaleUpCooldown)
		scaleUpServerID, _ = m.decideServerActionsLocked(s, now)
		neededHeadroom = serverNeededHeadroomLocked(s, now)
		freeSlots = int(s.aggregateMaxUsers) - int(s.aggregateUsers)
		if freeSlots < 0 {
			freeSlots = 0
		}
		runningInstances = m.serverRunningCountLocked(s)
		maxSize = m.serverMaxInstances
		m.mu.Unlock()

		if freeSlots >= neededHeadroom || runningInstances >= max(1, maxSize) || scaleUpServerID < 0 {
			break
		}

		replica, err := m.registerServerReplicaInstance(scaleUpServerID)
		if err != nil {
			t.Fatalf("registerServerReplicaInstance(%d): %v", scaleUpServerID, err)
		}
		if replica == nil {
			break
		}

		m.updatePort(replica, nextPort)
		m.updateSearchPathNormalized(replica, normalizeSearchPath([]string{"id1"}))
		m.SetServerRunningForTest(replica, NewTestServer(nextPort))
		m.SetServerInfoForTest(replica, "server-1", "dm6", 0, byte(maxPlayersPerInstance))
		m.mu.Lock()
		replica.LastSeen = now
		if s := m.serverByInstanceID[replica.id]; s != nil {
			s.ScaleUpInFlight = false
			s.LastScaleUpAt = now
			m.refreshServerSnapshotLocked(s)
		}
		m.mu.Unlock()
		launched++
		nextPort++
	}

	m.mu.RLock()
	s := m.serversByID[serverID]
	if s == nil {
		m.mu.RUnlock()
		t.Fatalf("server %d missing after reconcile", serverID)
	}
	finalFreeSlots := int(s.aggregateMaxUsers) - int(s.aggregateUsers)
	if finalFreeSlots < 0 {
		finalFreeSlots = 0
	}
	neededHeadroom := serverNeededHeadroomLocked(s, now)
	finalInstances := m.serverRunningCountLocked(s)
	finalUsers := int(s.aggregateUsers)
	finalMaxUsers := int(s.aggregateMaxUsers)
	m.mu.RUnlock()

	return serverReconcileMetrics{
		duration:               time.Since(startedAt),
		launchedReplicas:       launched,
		reconcileIterations:    iterations,
		finalInstances:         finalInstances,
		neededHeadroom:         neededHeadroom,
		finalFreeSlots:         finalFreeSlots,
		finalAggregateUsers:    finalUsers,
		finalAggregateMaxUsers: finalMaxUsers,
	}
}

func serverIdentityForSeed(t *testing.T, m *ServerManager, seedServerID int) int {
	t.Helper()

	m.mu.RLock()
	defer m.mu.RUnlock()

	s := m.serverByInstanceID[seedServerID]
	if s == nil {
		t.Fatalf("seed instance %d has no server", seedServerID)
	}
	return s.ServerID
}

func expectedServerHeadroom(clientCount int) int {
	if clientCount <= 0 {
		return demandMinFreeSlots
	}
	joinRPS := float64(clientCount) / demandWindow.Seconds()
	dynamicHeadroom := int(math.Ceil(joinRPS * demandSpawnReady.Seconds() * demandSafetyFactor))
	return max(demandMinFreeSlots, dynamicHeadroom)
}

func parseServerStressTierList(raw string, defaults []int) ([]int, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		out := make([]int, len(defaults))
		copy(out, defaults)
		return out, nil
	}

	parts := strings.Split(text, ",")
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		token := strings.TrimSpace(part)
		if token == "" {
			continue
		}
		n, err := strconv.Atoi(token)
		if err != nil {
			return nil, fmt.Errorf("invalid tier %q: %w", token, err)
		}
		if n <= 0 {
			return nil, fmt.Errorf("tier must be > 0, got %d", n)
		}
		out = append(out, n)
	}
	return out, nil
}

func summarizeNanosPercentiles(samples []int64) (avg, maxLatency, p95, p99 time.Duration) {
	if len(samples) == 0 {
		return 0, 0, 0, 0
	}

	sorted := append([]int64(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i] < sorted[j]
	})

	var total int64
	var maxNanos int64
	for _, sample := range sorted {
		total += sample
		if sample > maxNanos {
			maxNanos = sample
		}
	}

	return time.Duration(total / int64(len(sorted))),
		time.Duration(maxNanos),
		time.Duration(percentileSample(sorted, 95)),
		time.Duration(percentileSample(sorted, 99))
}

func percentileSample(sorted []int64, percentile float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	if percentile <= 0 {
		return sorted[0]
	}
	if percentile >= 100 {
		return sorted[len(sorted)-1]
	}

	rank := int(math.Ceil((percentile / 100) * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}
