package main

import (
	"crypto/tls"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/0xBrsm/NexQuake/nexus/trunk"
	"github.com/0xBrsm/NexQuake/nexus/trunk/webtransport"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	qwt "github.com/quic-go/webtransport-go"
)

// WebTransport exists only when EXTERNAL_URL is set, same-origin with the
// page. The cert comes from the shared TLS runtime (cert.go) — an
// ACME-issued or BYO cert that chains to a public CA, which clients validate
// normally. (The e2e suite instead serves a self-signed cert the client pins
// by serverCertificateHashes; that path lives in the test harness, not here.)
// The WT listener binds UDP/HTTP_PORT — TCP and UDP socket spaces don't
// collide, so the WS and WT listeners coexist on the same port.
//
// The advertised WT URL authority is each /start request's own Host header
// — exactly the public host[:port] the browser already reached over TCP —
// so the one deployment rule is that whatever public port serves the page
// also reaches HTTP_PORT over UDP.

// setupWebTransport configures the WebTransport listener when the TLS
// runtime supplies a QUIC cert source (EXTERNAL_URL set). Returns the
// configured *qwt.Server (caller drives ListenAndServe and Close) or nil
// when WT is disabled.
func setupWebTransport(app *nexusApp, rt *tlsRuntime) (*qwt.Server, error) {
	if rt.getWTCert == nil {
		return nil, nil
	}

	// quic-go logs a one-time "failed to sufficiently increase receive buffer
	// size" line to stderr (via the stdlib logger) when the kernel UDP receive
	// buffer is below its 7 MiB target. It's benign for our datagram volume and
	// only tunable host-side (sysctl net.core.rmem_max), so silence the noisy
	// startup print and re-surface the same pointer at debug for anyone chasing
	// UDP throughput. The env var is quic-go's only knob; set it before the QUIC
	// listener starts (it reads the var when wrapping the conn).
	os.Setenv("QUIC_GO_DISABLE_RECEIVE_BUFFER_WARNING", "true")
	slog.Debug("WebTransport: quic-go UDP receive-buffer warning suppressed; raise host sysctl net.core.rmem_max (target 7 MiB) if QUIC throughput matters. See https://github.com/quic-go/quic-go/wiki/UDP-Buffer-Sizes")

	// Register the WT manifest field. The provider is invoked per /start
	// request so the URL authority is the one the requesting client actually
	// used to reach the page.
	app.AddBootstrapClientFields(func(r *http.Request) map[string]any {
		return map[string]any{
			"transports": map[string]any{
				"webtransport": map[string]any{
					"url": "https://" + r.Host + "/connect",
				},
			},
		}
	})

	// HTTP/3 needs its own listener (UDP/QUIC); it cannot share Nexus's TCP
	// HTTP server. Both listeners bind to the same port number — TCP and UDP
	// socket spaces don't collide. The cert is delivered via GetCertificate
	// so rotation/renewal hot-swaps the active cert at handshake time.
	wt := &qwt.Server{
		H3: &http3.Server{
			Addr: ":" + app.cfg.httpPort,
			TLSConfig: &tls.Config{
				MinVersion:     tls.VersionTLS13,
				GetCertificate: rt.getWTCert,
			},
			QUICConfig: &quic.Config{
				MaxIdleTimeout:  120 * time.Second,
				KeepAlivePeriod: 15 * time.Second,
			},
		},
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	wt.H3.TLSConfig = http3.ConfigureTLSConfig(wt.H3.TLSConfig)
	qwt.ConfigureHTTP3Server(wt.H3)

	h3mux := http.NewServeMux()
	h3mux.HandleFunc("/connect", app.handleWebTransport(wt))
	wt.H3.Handler = app.access.HTTPGate(h3mux)
	return wt, nil
}

func (app *nexusApp) handleWebTransport(wt *qwt.Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		app.trunkSession(w, r, trunk.TransportWebTransport, func() (trunk.Transport, error) {
			sess, err := wt.Upgrade(w, r)
			if err != nil {
				return nil, err
			}
			return webtransport.New(sess), nil
		})
	}
}
