package trunk

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
)

const maxVirtualIPProbeAttempts = 4096

// virtualIPAllocator deterministically maps client source IPs to unique
// 127.x.x.x VirtualIP addresses. The mapping is stable within a process
// lifetime: the same source IP always hashes to the same candidate, with a
// hash-with-probe-counter walk on collision. Internal to trunk — the Trunk
// holds one and uses it during session construction and teardown.
type virtualIPAllocator struct {
	serverIP   net.IP     // 4-byte form, used for VIP collision-walking
	serverAddr netip.Addr // value-type form, used by Session.udpWrite

	mu   sync.Mutex
	used map[uint32]struct{}
}

func newAllocator(serverIP net.IP) *virtualIPAllocator {
	ip4 := serverIP.To4()
	a := &virtualIPAllocator{
		serverIP: ip4,
		used:     make(map[uint32]struct{}),
	}
	if ip4 != nil {
		a.serverAddr = netip.AddrFrom4([4]byte{ip4[0], ip4[1], ip4[2], ip4[3]})
	}
	return a
}

// alloc allocates a unique VirtualIP for the given source IP.
func (a *virtualIPAllocator) alloc(sourceKey string) ([4]byte, error) {
	sourceKey = strings.TrimSpace(sourceKey)
	if sourceKey == "" {
		sourceKey = "unknown"
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	for probe := 0; probe < maxVirtualIPProbeAttempts; probe++ {
		seed := "NQ:client-ip:v1|" + sourceKey + "|" + strconv.Itoa(probe)
		sum := fnv64aSum(seed)

		candidate := [4]byte{127, byte(sum >> 56), byte(sum >> 48), byte(sum >> 40)}
		if bytes.Equal(a.serverIP, candidate[:]) {
			continue
		}

		key := binary.BigEndian.Uint32(candidate[:])
		if _, used := a.used[key]; used {
			continue
		}

		a.used[key] = struct{}{}
		return candidate, nil
	}

	return [4]byte{}, fmt.Errorf("failed to allocate VirtualIP for %q", sourceKey)
}

func (a *virtualIPAllocator) release(ip4 [4]byte) {
	if ip4[0] != 127 {
		return
	}
	a.mu.Lock()
	delete(a.used, binary.BigEndian.Uint32(ip4[:]))
	a.mu.Unlock()
}

func fnv64aSum(text string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(text))
	return h.Sum64()
}
