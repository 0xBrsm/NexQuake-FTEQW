package orch

import (
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Quake wire-protocol constants (see quakedef.h / net.h).
const (
	netFlagLengthMask  uint32 = 0x0000ffff
	netFlagCtl         uint32 = 0x80000000
	netProtocolVersion byte   = 3
	ccreqServerInfo    byte   = 0x02
	ccrepServerInfo    byte   = 0x83
)

// dpmaster-style (Quake3/FTEQW) out-of-band query constants. FTE servers answer
// the `getinfo` query with an `infoResponse` carrying a backslash-delimited
// infostring. See docker/serverlist-poller.sh for the reference shell poll this
// mirrors.
var (
	// connectionlessHeader is the four 0xFF bytes prefixing all out-of-band
	// (connectionless) dpmaster-style packets.
	connectionlessHeader = []byte{0xFF, 0xFF, 0xFF, 0xFF}
	// getInfoQuery is the full datagram sent to a backend server to request its
	// current serverinfo: `\xFF\xFF\xFF\xFFgetinfo poll\n`.
	getInfoQuery = append(append([]byte(nil), connectionlessHeader...), []byte("getinfo poll\n")...)
	// infoResponsePrefix follows the connectionless header in a valid reply:
	// `\xFF\xFF\xFF\xFFinfoResponse\n\key\value...`.
	infoResponsePrefix = []byte("infoResponse")
)

// buildGetInfoQuery returns the getinfo datagram sent to backend servers.
func buildGetInfoQuery() []byte {
	return append([]byte(nil), getInfoQuery...)
}

// serverInfoFields holds the subset of an FTE infoResponse infostring that the
// orchestrator consumes.
type serverInfoFields struct {
	Hostname   string
	MapName    string
	GameDir    string
	Players    int
	MaxPlayers int
}

// parseInfoResponse parses an FTE dpmaster-style infoResponse datagram into the
// per-server fields the orchestrator tracks. The wire format is:
//
//	\xFF\xFF\xFF\xFFinfoResponse\n\key\value\key\value\...
//
// Field normalization mirrors docker/serverlist-poller.sh:
//   - hostname: key "hostname" (fallback "sv_hostname")
//   - mapname:  key "mapname"
//   - gamedir:  key "modname" (fallback "*gamedir", then "gamedir")
//   - players:  key "clients"
//   - maxplayers: key "sv_maxclients" (fallback "maxclients")
func parseInfoResponse(payload []byte) (serverInfoFields, bool) {
	if len(payload) < len(connectionlessHeader)+len(infoResponsePrefix) {
		return serverInfoFields{}, false
	}
	if !bytes.HasPrefix(payload, connectionlessHeader) {
		return serverInfoFields{}, false
	}
	rest := payload[len(connectionlessHeader):]
	if !bytes.HasPrefix(rest, infoResponsePrefix) {
		return serverInfoFields{}, false
	}
	rest = rest[len(infoResponsePrefix):]

	// Skip the separator between "infoResponse" and the infostring. FTE emits a
	// newline here; tolerate CR and stray whitespace as well. The infostring
	// itself begins at the first backslash.
	if idx := bytes.IndexByte(rest, '\\'); idx >= 0 {
		rest = rest[idx:]
	} else {
		return serverInfoFields{}, false
	}

	kv := parseInfostring(string(rest))

	pick := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := kv[k]; ok && v != "" {
				return v
			}
		}
		return ""
	}

	fields := serverInfoFields{
		Hostname: pick("hostname", "sv_hostname"),
		MapName:  pick("mapname"),
		GameDir:  pick("modname", "*gamedir", "gamedir"),
	}
	if v := pick("clients"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			fields.Players = n
		}
	}
	if v := pick("sv_maxclients", "maxclients"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			fields.MaxPlayers = n
		}
	}
	return fields, true
}

// parseInfostring parses a backslash-delimited infostring (`\key\value\...`)
// into a map. A leading backslash is expected; trailing CR/LF is tolerated.
// Later occurrences of a key overwrite earlier ones.
func parseInfostring(s string) map[string]string {
	s = strings.Trim(s, "\r\n")
	out := make(map[string]string)
	if s == "" {
		return out
	}
	parts := strings.Split(s, "\\")
	// A well-formed string starts with "\", so parts[0] is empty; iterate over
	// the remaining key/value pairs.
	for i := 1; i+1 < len(parts); i += 2 {
		key := parts[i]
		val := parts[i+1]
		if key == "" {
			continue
		}
		out[key] = val
	}
	return out
}

func appendCString(buf []byte, s string) []byte {
	buf = append(buf, []byte(s)...)
	buf = append(buf, 0)
	return buf
}

func readCString(buf []byte, start int) (string, int, bool) {
	if start < 0 || start >= len(buf) {
		return "", 0, false
	}
	rest := buf[start:]
	n := bytes.IndexByte(rest, 0)
	if n < 0 {
		return "", 0, false
	}
	return string(rest[:n]), start + n + 1, true
}

// Quake client hostcache field limits (see net.h in the patched client).
const (
	hostcacheNameMax  = 23
	hostcacheFieldMax = 15
)

// serverListEntry describes a single server entry in the aggregated slist response.
type serverListEntry struct {
	ListenPort int    // UDP port the server is listening on
	Hostname   string // server hostname (truncated to hostcacheNameMax)
	MapName    string // current map (truncated to hostcacheFieldMax)
	GameDir    string // active game directory (truncated to hostcacheFieldMax)
	Users      uint16 // current player count
	MaxUsers   uint16 // server capacity
	Instances  uint16 // instance instance count for autoscaled servers; 0 hides the server suffix
}

func normalizeServerListEntry(e serverListEntry) serverListEntry {
	if e.Hostname == "" {
		e.Hostname = "UNNAMED"
	}
	if e.MapName == "" {
		e.MapName = "?"
	}
	if e.GameDir == "" {
		e.GameDir = "id1"
	}
	e.Hostname = truncateSlistField(e.Hostname, hostcacheNameMax)
	e.MapName = truncateSlistField(e.MapName, hostcacheFieldMax)
	e.GameDir = truncateSlistField(e.GameDir, hostcacheFieldMax)
	return e
}

// buildCCREPServerList builds an aggregated CCREP_SERVER_INFO response
// containing multiple server entries. Returns the datagram and entry count.
func buildCCREPServerList(entries []serverListEntry) ([]byte, int) {
	const maxNetDatagramSize = 1024 + 8

	buf := make([]byte, 0, 512)
	buf = append(buf, 0, 0, 0, 0) // placeholder header
	buf = append(buf, ccrepServerInfo)
	countIndex := len(buf)
	buf = append(buf, 0) // count placeholder

	count := 0
	for _, e := range entries {
		e = normalizeServerListEntry(e)
		serverPort := e.ListenPort
		if serverPort <= 0 || serverPort > 65535 {
			continue
		}
		serverPortText := strconv.Itoa(serverPort)
		entrySize := len(serverPortText) + 1 + len(e.Hostname) + 1 + len(e.MapName) + 1 + len(e.GameDir) + 1 + 7
		if len(buf)+entrySize > maxNetDatagramSize {
			break
		}

		buf = appendCString(buf, serverPortText)
		buf = appendCString(buf, e.Hostname)
		buf = appendCString(buf, e.MapName)
		buf = appendCString(buf, e.GameDir)
		buf = append(buf, byte(e.Users&0xff), byte(e.Users>>8))
		buf = append(buf, byte(e.MaxUsers&0xff), byte(e.MaxUsers>>8))
		buf = append(buf, byte(e.Instances&0xff), byte(e.Instances>>8))
		buf = append(buf, netProtocolVersion)
		count++
	}

	buf[countIndex] = byte(count)
	control := netFlagCtl | uint32(len(buf))
	binary.BigEndian.PutUint32(buf[0:4], control)
	return buf, count
}

func truncateSlistField(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes]
}

func serverListEntriesLocked(mgr *ServerManager, now time.Time, noteDemand bool) []serverListEntry {
	out := make([]serverListEntry, 0, len(mgr.serversByID))
	for _, s := range mgr.serversLocked() {
		if s.aggregateInstances == 0 {
			continue
		}
		instancePort, ok := mgr.pickServerInstanceLocked(s, true)
		if !ok {
			continue
		}
		instances := uint16(0)
		if s.Autoscales {
			if noteDemand {
				noteServerDemandLocked(s, now)
			}
			instances = s.joinableInstances
		}
		out = append(out, serverListEntry{
			ListenPort: instancePort,
			Hostname:   s.DisplayHostname,
			MapName:    s.DisplayMap,
			GameDir:    s.DisplayGameDir,
			Users:      s.aggregateUsers,
			MaxUsers:   s.aggregateMaxUsers,
			Instances:  instances,
		})
	}
	return out
}

// IsSlistRequest reports whether payload is a valid CCREQ_SERVER_INFO datagram.
func IsSlistRequest(payload []byte) bool {
	if len(payload) < 4+1 {
		return false
	}
	control := binary.BigEndian.Uint32(payload[0:4])
	if (control&^netFlagLengthMask) != netFlagCtl || int(control&netFlagLengthMask) != len(payload) {
		return false
	}
	if payload[4] != ccreqServerInfo {
		return false
	}
	i := 5
	end := i
	for end < len(payload) && payload[end] != 0 {
		end++
	}
	if end >= len(payload) || string(payload[i:end]) != "QUAKE" {
		return false
	}
	proto := end + 1
	return proto < len(payload) && payload[proto] == netProtocolVersion
}

// BuildSlistResponse builds an aggregated CCREP_SERVER_INFO response datagram
// from the current state of all managed servers.
func (m *ServerManager) BuildSlistResponse() []byte {
	entries := snapshotForSlist(m)
	data, _ := buildCCREPServerList(entries)
	return data
}

const serverInfoPollStep = 500 * time.Millisecond

// serverInfoPoller polls running instances via the dpmaster-style getinfo
// query and forwards infoResponse replies to the [ServerManager] to keep
// game-state metadata current.
type serverInfoPoller struct {
	mgr      *ServerManager
	serverIP net.IP

	pollConn *net.UDPConn
	stopOnce sync.Once
}

// StartInfoPoller starts a server-info poller for mgr and returns a stop
// function. If the UDP socket cannot be bound, it logs a warning and returns
// a no-op stop function. The poller also stops when ctx is cancelled.
func (mgr *ServerManager) StartInfoPoller(ctx context.Context, serverIP net.IP) func() {
	p := &serverInfoPoller{mgr: mgr, serverIP: serverIP}
	if err := p.Start(ctx); err != nil {
		slog.Warn(fmt.Sprintf("Server info poller disabled: %v", err))
		return func() {}
	}
	return p.Stop
}

// Start opens the UDP socket and launches the poll and read goroutines.
// It returns an error if the UDP socket cannot be bound.
// The poller stops when ctx is cancelled or Stop is called.
func (p *serverInfoPoller) Start(ctx context.Context) error {
	udpConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return fmt.Errorf("server info poller: listen udp: %w", err)
	}

	p.pollConn = udpConn

	go p.readLoop(ctx, udpConn)
	go p.pollLoop(ctx, udpConn)

	return nil
}

// Stop closes the UDP socket and terminates both goroutines.
// Safe to call more than once.
func (p *serverInfoPoller) Stop() {
	p.stopOnce.Do(func() {
		if p.pollConn != nil {
			_ = p.pollConn.Close()
		}
	})
}

func fillRunningPortsLocked(mgr *ServerManager, dst []int) []int {
	dst = dst[:0]

	for port, ids := range mgr.instanceIDsByPort {
		if port < 1 || port > 65535 {
			continue
		}
		for _, serverID := range ids {
			rec := mgr.instancesByID[serverID]
			if !mgr.instanceRunningLocked(rec) {
				continue
			}
			dst = append(dst, port)
			break
		}
	}
	return dst
}

func fillRunningPorts(mgr *ServerManager, dst []int) []int {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	return fillRunningPortsLocked(mgr, dst)
}

func snapshotForSlist(mgr *ServerManager) []serverListEntry {
	now := time.Now()

	mgr.mu.Lock()
	out := serverListEntriesLocked(mgr, now, true)
	mgr.mu.Unlock()

	slices.SortFunc(out, func(a, b serverListEntry) int {
		return cmp.Or(
			cmp.Compare(strings.ToLower(a.Hostname), strings.ToLower(b.Hostname)),
			cmp.Compare(a.ListenPort, b.ListenPort),
		)
	})
	return out
}

func (p *serverInfoPoller) pollLoop(ctx context.Context, conn *net.UDPConn) {
	req := buildGetInfoQuery()

	var ports []int

	// Prime quickly at startup.
	ports = fillRunningPorts(p.mgr, ports)
	for _, port := range ports {
		if _, err := conn.WriteToUDP(req, &net.UDPAddr{IP: p.serverIP, Port: port}); err != nil && errors.Is(err, net.ErrClosed) {
			return
		}
	}

	pollOne := func(port int) bool {
		if _, err := conn.WriteToUDP(req, &net.UDPAddr{IP: p.serverIP, Port: port}); err != nil {
			return !errors.Is(err, net.ErrClosed)
		}
		return true
	}

	ticker := time.NewTicker(serverInfoPollStep)
	defer ticker.Stop()

	slog.Debug(fmt.Sprintf("Server info poller: polling %d targets every %s (round-robin step %s)",
		len(ports), time.Duration(len(ports))*serverInfoPollStep, serverInfoPollStep))

	rrNext := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.mgr.reconcileAllServers()
			ports = fillRunningPorts(p.mgr, ports)
			if len(ports) == 0 {
				continue
			}
			if rrNext >= len(ports) {
				rrNext = 0
			}
			port := ports[rrNext]
			rrNext++
			if !pollOne(port) {
				return
			}
		}
	}
}

func (p *serverInfoPoller) readLoop(ctx context.Context, conn *net.UDPConn) {
	buf := make([]byte, 2048)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if ctx.Err() != nil {
				return
			}
			if nerr, ok := err.(net.Error); ok && nerr.Timeout() {
				continue
			}
			continue
		}

		if src.Port < 1 || src.Port > 65535 {
			continue
		}

		info, ok := parseInfoResponse(buf[:n])
		if !ok {
			continue
		}

		players := byte(min(max(info.Players, 0), 0xff))
		maxPlayers := byte(min(max(info.MaxPlayers, 0), 0xff))
		p.mgr.updateGameState(src.Port, info.Hostname, info.MapName, info.GameDir, players, maxPlayers)
	}
}
