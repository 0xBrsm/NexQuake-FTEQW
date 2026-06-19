// Package webtransport adapts a [github.com/quic-go/webtransport-go] *Session
// to the [trunk.Transport] interface. Game frames flow over WT datagrams,
// which are unreliable and unordered — a match for UDP semantics. QUIC
// provides connection-level keepalive, so [trunk.Transport.Ping] is a no-op
// for this adapter.
package webtransport

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/quic-go/quic-go"
	wt "github.com/quic-go/webtransport-go"

	"github.com/0xBrsm/NexQuake/nexus/trunk"
)

// New wraps an already-upgraded *webtransport.Session as a [trunk.Transport].
func New(s *wt.Session) trunk.Transport { return &adapter{session: s} }

type adapter struct {
	session  *wt.Session
	tooLarge uint64
}

func (a *adapter) Name() string { return trunk.TransportWebTransport }

func (a *adapter) ReadFrame() ([]byte, error) {
	return a.session.ReceiveDatagram(a.session.Context())
}

func (a *adapter) WriteFrame(data []byte) error {
	return classifyWriteError(a.session.SendDatagram(data), len(data), &a.tooLarge)
}

// classifyWriteError absorbs non-fatal datagram send errors. An oversized
// datagram is UDP-equivalent loss, not a dead transport: the limit is
// min(peer max_datagram_frame_size, live MTU estimate) and can dip below a
// max-size game frame mid-session. Drop it like the network would —
// propagating it would make the write loop tear down the whole session over
// one fat packet. As UDP-equivalent loss it isn't actionable, so it's logged
// at debug — on the first drop and every 256th thereafter.
func classifyWriteError(err error, size int, tooLarge *uint64) error {
	var oversized *quic.DatagramTooLargeError
	if err == nil || !errors.As(err, &oversized) {
		return err
	}
	*tooLarge++
	if *tooLarge == 1 || *tooLarge%256 == 0 {
		slog.Debug(fmt.Sprintf("webtransport: dropped %dB datagram exceeding QUIC limit (%d dropped total)", size, *tooLarge))
	}
	return nil
}

// Ping is a no-op: QUIC handles keepalive at the connection layer.
func (a *adapter) Ping() error { return nil }

func (a *adapter) Close() error {
	return a.session.CloseWithError(0, "")
}
