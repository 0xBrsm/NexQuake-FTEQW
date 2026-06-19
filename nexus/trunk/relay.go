package trunk

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"syscall"
	"time"
)

// controlPort is the reserved tunnel port for control-channel frames.
const controlPort = 0

// buildFrame builds a trunk tunnel frame: 2-byte port header + payload.
func buildFrame(port int, payload []byte) []byte {
	if port < controlPort || port > 65535 {
		return nil
	}
	frame := make([]byte, 2+len(payload))
	binary.BigEndian.PutUint16(frame[:2], uint16(port))
	copy(frame[2:], payload)
	return frame
}

// decodeFrame extracts the port header and payload from a tunnel frame.
// Frames shorter than the 2-byte header are rejected.
func decodeFrame(packet []byte) (dstPort int, payload []byte, ok bool) {
	if len(packet) < 2 {
		return 0, nil, false
	}
	dstPort = int(binary.BigEndian.Uint16(packet[:2]))
	return dstPort, packet[2:], true
}

func isValidServerPort(port int) bool {
	return port >= 1 && port <= 65535
}

func (s *Session) readLoop() {
	for {
		data, err := s.xport.ReadFrame()
		if err != nil {
			if s.ctx.Err() == nil {
				slog.Debug(fmt.Sprintf("tunnel read error: %v", err))
			}
			s.End()
			return
		}
		s.handleFrame(data)
	}
}

func (s *Session) writeLoop() {
	defer close(s.writeLoopDone)
	ping := time.NewTicker(keepalivePingInterval)
	defer ping.Stop()

	for {
		select {
		case <-s.ctx.Done():
			s.drainTx()
			return
		case <-ping.C:
			if err := s.xport.Ping(); err != nil {
				if s.ctx.Err() == nil {
					slog.Debug(fmt.Sprintf("tunnel ping error: %v", err))
				}
				s.cancel()
				_ = s.xport.Close() // unblock readLoop; End handles the rest
				return
			}
		case packet := <-s.tx:
			// Don't gate on ctx.Err() — if End cancelled mid-select
			// the frame is still part of the drain we promised to deliver.
			if len(packet) == 0 {
				continue
			}
			if err := s.xport.WriteFrame(packet); err != nil {
				if s.ctx.Err() == nil {
					slog.Debug(fmt.Sprintf("tunnel write error: %v", err))
				}
				s.cancel()
				_ = s.xport.Close() // unblock readLoop; End handles the rest
				return
			}
		}
	}
}

// drainTx flushes queued outbound frames after ctx is cancelled, bounded by
// sessionDrainDeadline. Best-effort: any write error stops the drain (the
// transport is presumably going away).
func (s *Session) drainTx() {
	deadline := time.Now().Add(sessionDrainDeadline)
	for time.Now().Before(deadline) {
		select {
		case packet := <-s.tx:
			if len(packet) == 0 {
				continue
			}
			if err := s.xport.WriteFrame(packet); err != nil {
				return
			}
		default:
			return
		}
	}
}

// sendFrame enqueues a frame for the tunnel write loop. If drop is true,
// the frame is silently dropped when the channel is full.
func (s *Session) sendFrame(frame []byte, drop bool) {
	if len(frame) == 0 || s.ctx.Err() != nil {
		return
	}

	if !drop {
		select {
		case <-s.ctx.Done():
		case s.tx <- frame:
		}
		return
	}

	select {
	case <-s.ctx.Done():
	case s.tx <- frame:
	default:
		// Drop silently and count; End() logs one summary per session.
		s.txDropped.Add(1)
	}
}

func (s *Session) handleFrame(packet []byte) {
	dstPort, payload, ok := decodeFrame(packet)
	if !ok {
		return
	}

	if dstPort == controlPort {
		// Control-channel payload ownership stays outside trunk; the handler
		// may call s.SendControl to reply or push at will.
		if s.trunk.onCtrlFrame != nil {
			s.trunk.onCtrlFrame(s, payload)
		}
		return
	}

	if s.trunk.allowPort != nil && !s.trunk.allowPort(dstPort) {
		slog.Debug(fmt.Sprintf("dropping frame to disallowed port %d", dstPort))
		return
	}

	s.noteServerRoutePort(dstPort)
	s.udpWrite(dstPort, payload)
}

// listenAddrForClient returns a UDP listen address bound to the client's
// VirtualIP (any port).
func listenAddrForClient(ip4 [4]byte) *net.UDPAddr {
	return &net.UDPAddr{
		IP:   net.IPv4(ip4[0], ip4[1], ip4[2], ip4[3]).To4(),
		Port: 0,
	}
}

// serverSourcePortFromAddr extracts the source port from a UDP address
// returned by ReadFrom. Returns ok=false for non-UDP addresses and port 0.
func serverSourcePortFromAddr(addr net.Addr) (srcPort int, ok bool) {
	udpAddr, isUDP := addr.(*net.UDPAddr)
	if !isUDP || !isValidServerPort(udpAddr.Port) {
		return 0, false
	}
	return udpAddr.Port, true
}

func (s *Session) udpReadLoop() {
	buffer := make([]byte, 65536)
	for {
		if s.ctx.Err() != nil {
			return
		}

		n, remoteSrcAddr, err := s.udpConn.ReadFrom(buffer)
		if err != nil {
			if errors.Is(err, syscall.ECONNREFUSED) {
				continue
			}
			if s.ctx.Err() == nil {
				slog.Warn(fmt.Sprintf("UDP read error: %v", err))
			}
			s.End()
			return
		}

		packet := buffer[:n]

		srcPort, ok := serverSourcePortFromAddr(remoteSrcAddr)
		if !ok {
			continue
		}
		if s.trunk.debugRelay {
			slog.Debug(fmt.Sprintf("DEBUG_RELAY\tudp<-server\tsrc=%s\tport=%d\tlen=%d\tbytes=% x",
				remoteSrcAddr.String(), srcPort, len(packet), packet[:min(len(packet), 24)]))
		}

		s.sendFrame(buildFrame(srcPort, packet), true)
	}
}

func (s *Session) udpWrite(dstPort int, payload []byte) {
	if s.ctx.Err() != nil || len(payload) == 0 {
		return
	}

	addr := netip.AddrPortFrom(s.trunk.allocator.serverAddr, uint16(dstPort))
	if s.trunk.debugRelay {
		slog.Debug(fmt.Sprintf("DEBUG_RELAY\tudp->server\tdst=%s\tlen=%d\tbytes=% x",
			addr.String(), len(payload), payload[:min(len(payload), 24)]))
	}

	_, err := s.udpConn.WriteToUDPAddrPort(payload, addr)
	if err == nil || s.ctx.Err() != nil {
		return
	}
	// ECONNREFUSED on a UDP write is usually stale: Linux queues an ICMP
	// "Port Unreachable" from a previous packet and surfaces it on the next
	// write to the same addr, even if that write actually made it onto the
	// wire. Real connectivity failures manifest elsewhere (slist timeouts,
	// stalled gameplay), so this errno is just noise — keep it at debug.
	if errors.Is(err, syscall.ECONNREFUSED) {
		slog.Debug(fmt.Sprintf("UDP write: stale ECONNREFUSED dst=%s", addr.String()))
		return
	}
	slog.Warn(fmt.Sprintf("UDP write error: %v", err))
}
