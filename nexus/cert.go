package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

// EXTERNAL_URL declares the server's public identity and is the single
// switch between the two run paths (docs/ENVIRONMENT.md "Run Paths"):
//
//   - unset (default): plain HTTP, WebSocket only — no TLS and no
//     WebTransport, like NexQuake before 1.11. Right for localhost play, a
//     LAN, or sitting behind any reverse proxy / tunnel that owns public
//     TLS (which may also gate the whole site behind an IdP).
//   - "https://host": Nexus owns the public endpoint at that hostname.
//     The hostname is the certificate identity (automatic Let's Encrypt),
//     HTTPS/WSS serve on the TCP listener, and WebTransport is advertised
//     at the authority each /start request arrives on — nothing separate
//     to configure. Whatever public port serves the page must reach
//     HTTP_PORT over both TCP and UDP.
//
// The certificate comes from one of two sources, both rooted at CERT_DIR
// (default /app/cert):
//
//   - BYO cert: if CERT_DIR holds cert.pem + key.pem, those are loaded and
//     served directly — no ACME. This lets an operator front Nexus with a
//     cert they already manage, and lets the e2e suite serve a self-signed
//     cert that the WebTransport client pins via serverCertificateHashes.
//   - ACME (default): otherwise, certificates are obtained automatically
//     from Let's Encrypt over the TLS-ALPN-01 challenge on the main TLS
//     listener (no plain-HTTP port needed). The autocert account/cert cache
//     lives under CERT_DIR/acme — persist it across restarts to avoid
//     re-issuing.

// tlsRuntime is the resolved TLS wiring shared by the TCP HTTP server and
// the WebTransport (QUIC) listener.
type tlsRuntime struct {
	// serverTLS is plugged into the TCP http.Server; nil means plain HTTP.
	// Under ACME this is autocert's own config so the TCP listener serves the
	// TLS-ALPN-01 challenge (issuance + renewal); under BYO it serves the
	// resolved cert from wtCert.
	serverTLS *tls.Config

	// getWTCert supplies certs to the WebTransport listener's handshakes;
	// nil disables WebTransport (no EXTERNAL_URL). It serves the pre-resolved
	// cert (see resolvedCert) and never calls autocert live — doing so blocks
	// inside the QUIC handshake and black-holes WebTransport.
	getWTCert func(*tls.ClientHelloInfo) (*tls.Certificate, error)

	// certHost is the certificate hostname (EXTERNAL_URL's host); empty in
	// plain-HTTP mode.
	certHost string

	// resolveCert performs the single cert load/resolve: BYO returns the
	// loaded pair; ACME calls autocert (whose TLS-ALPN-01 challenge is served
	// by the TCP listener). resolveTLSCert runs it once at startup, stores the
	// result in wtCert, and re-runs it periodically so renewals propagate.
	// nil in plain-HTTP mode.
	resolveCert func(context.Context) (*tls.Certificate, error)

	// wtCert holds the certificate resolved by resolveTLSCert that the
	// WebTransport listener serves (and the TCP listener under BYO). Read live
	// on every handshake via resolvedCert; updated atomically on renewal.
	wtCert atomic.Pointer[tls.Certificate]
}

// resolvedCert serves the certificate resolved by resolveTLSCert. It never
// calls autocert: that blocks inside the QUIC handshake (autocert may attempt
// an ACME obtain/renew whose TLS-ALPN-01 challenge can't complete on the
// h3-only WebTransport listener), which black-holed every WebTransport session
// on the ACME path. Until the startup resolve lands it returns a fast error, so
// the client stays on WebSocket and adopts WebTransport once the cert is up.
func (rt *tlsRuntime) resolvedCert(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	if c := rt.wtCert.Load(); c != nil {
		return c, nil
	}
	return nil, fmt.Errorf("TLS certificate for %s not resolved yet", rt.certHost)
}

// setupTLS reads EXTERNAL_URL and CERT_DIR and returns the resolved runtime:
// a BYO cert when CERT_DIR/cert.pem + key.pem exist, otherwise ACME.
func setupTLS(_ context.Context) (*tlsRuntime, error) {
	origin := strings.TrimSpace(os.Getenv("EXTERNAL_URL"))
	if origin == "" {
		return &tlsRuntime{}, nil
	}
	host, err := parseExternalURL(origin)
	if err != nil {
		return nil, err
	}
	certDir := getEnv("CERT_DIR", "/app/cert")

	// BYO cert takes precedence: a complete cert.pem + key.pem pair short-
	// circuits ACME and serves the operator-supplied cert as-is.
	certPath := filepath.Join(certDir, "cert.pem")
	keyPath := filepath.Join(certDir, "key.pem")
	if fileExists(certPath) && fileExists(keyPath) {
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, fmt.Errorf("load BYO cert from %s: %w", certDir, err)
		}
		rt := &tlsRuntime{
			certHost:    host,
			resolveCert: func(context.Context) (*tls.Certificate, error) { return &cert, nil },
		}
		// The pair is in hand now, so both listeners can serve it immediately;
		// resolveTLSCert still logs it (and its expiry) for parity with ACME.
		rt.wtCert.Store(&cert)
		rt.serverTLS = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: rt.resolvedCert}
		rt.getWTCert = rt.resolvedCert
		return rt, nil
	}

	m := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(filepath.Join(certDir, "acme")),
		HostPolicy: autocert.HostWhitelist(host),
	}
	rt := &tlsRuntime{
		certHost: host,
		// autocert's own config drives the TCP listener: it serves the cert for
		// normal SNI and the TLS-ALPN-01 challenge for "acme-tls/1", which is
		// how issuance and renewal happen. The QUIC listener can't do that
		// (h3-only ALPN), so WebTransport serves the pre-resolved cert instead.
		serverTLS: m.TLSConfig(),
		// Resolve via the whitelisted SNI: autocert loads the cached cert or,
		// if absent/expired, obtains one — the TLS-ALPN-01 challenge is served
		// by the TCP listener above. Cheap on a warm cache (no CA contact).
		//
		// Advertise an ECDSA cipher suite: autocert keys its cache by
		// RSA-vs-ECDSA derived from the hello (supportsECDSA), and Let's Encrypt
		// (autocert's default) issues ECDSA. A hello with no ECDSA suite — which
		// is what a bare ClientHelloInfo, and quic-go's handshake, present —
		// makes autocert look up an RSA cert that was never issued, miss the
		// cache, and block on a (rate-limited) obtain. That hang is exactly what
		// black-holed WebTransport before this resolve was hoisted out of the
		// QUIC handshake.
		resolveCert: func(context.Context) (*tls.Certificate, error) {
			return m.GetCertificate(&tls.ClientHelloInfo{
				ServerName:   host,
				CipherSuites: []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256},
			})
		},
	}
	rt.getWTCert = rt.resolvedCert
	return rt, nil
}

// certLeaf returns the parsed leaf of a TLS certificate, using the cached
// Leaf when present and parsing the DER otherwise. Returns nil if it can't be
// recovered (logging-only; never fatal).
func certLeaf(cert *tls.Certificate) *x509.Certificate {
	if cert.Leaf != nil {
		return cert.Leaf
	}
	if len(cert.Certificate) > 0 {
		if leaf, err := x509.ParseCertificate(cert.Certificate[0]); err == nil {
			return leaf
		}
	}
	return nil
}

// resolveTLSCert is the single cert load/resolve step. It runs rt.resolveCert
// once at startup and stores the result in rt.wtCert so the WebTransport (and,
// under BYO, the TCP) listener serves it via resolvedCert — never calling
// autocert live in a handshake. It then refreshes periodically so an autocert
// renewal propagates to the served cert. No-op in plain-HTTP mode.
//
// Resolving also surfaces a failure at startup instead of silently on the first
// client handshake (autocert issues on demand, so an unconfigured or
// rate-limited host would otherwise produce no output until a real client with
// the right SNI connects).
//
// Run it in a goroutine *after* the TCP TLS server is accepting: ACME issuance
// can take seconds (and must not block startup), and its TLS-ALPN-01 challenge
// arrives back on the TCP listener, so resolution can only complete once that
// listener is serving. The initial attempt retries with a conservative,
// minutes-scale backoff — failed ACME validations are themselves rate-limited,
// so retrying sparsely avoids burning the limit.
// activateTLSCert resolves the cert once at startup so a TLS failure is fatal
// before Nexus serves anything else. Synchronous: it returns when the cert is
// in hand — cached or freshly issued, the TLS-ALPN-01 challenge answered by the
// already-serving TCP listener — or an error after a short bounded retry. BYO
// certs resolve instantly. No-op (nil) when TLS is off. On success the cert is
// stored for the listeners; the caller's "listening ... with TLS" line is the
// single success announcement, so this logs only the pending retries.
func activateTLSCert(ctx context.Context, rt *tlsRuntime) error {
	if rt == nil || rt.resolveCert == nil {
		return nil
	}
	// Short, seconds-scale retry: absorbs a transient LE/network blip at boot
	// without crash-looping, but a genuine misconfig fails fast (not the
	// minutes-scale renewal backoff). A persisted acme cache means a healthy
	// restart loads the cached cert here and never re-issues.
	backoffs := []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second}
	for attempt := 0; ; attempt++ {
		cert, err := rt.resolveCert(ctx)
		if err == nil {
			rt.wtCert.Store(cert)
			return nil
		}
		if attempt >= len(backoffs) {
			return fmt.Errorf("%s: %w", rt.certHost, err)
		}
		delay := backoffs[attempt]
		slog.Warn(fmt.Sprintf("TLS cert pending: %s, retry in %s: %v", rt.certHost, delay, err))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
}

// refreshTLSCert re-resolves periodically so an autocert renewal propagates to
// the served cert. autocert renews lazily when GetCertificate is called near
// expiry, so this re-call both triggers and picks up the new cert. BYO certs
// don't change, so this is a cheap no-op. Run as a goroutine after
// activateTLSCert has succeeded.
func refreshTLSCert(ctx context.Context, rt *tlsRuntime) {
	if rt == nil || rt.resolveCert == nil {
		return
	}
	ticker := time.NewTicker(12 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cert, err := rt.resolveCert(ctx)
			if err != nil {
				slog.Warn(fmt.Sprintf("TLS cert refresh failed: %s: %v", rt.certHost, err))
				continue
			}
			prev := rt.wtCert.Load()
			rt.wtCert.Store(cert)
			if leaf := certLeaf(cert); leaf != nil && certNewer(leaf, prev) {
				slog.Info(fmt.Sprintf("TLS cert renewed: %s (expires %s)", rt.certHost, leaf.NotAfter.Format(time.DateOnly)))
			}
		}
	}
}

// certNewer reports whether leaf expires later than prev's leaf — i.e. a
// renewal landed rather than the same cert being re-served.
func certNewer(leaf *x509.Certificate, prev *tls.Certificate) bool {
	if prev == nil {
		return true
	}
	pl := certLeaf(prev)
	return pl == nil || leaf.NotAfter.After(pl.NotAfter)
}

// fileExists reports whether path names an existing regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// parseExternalURL validates EXTERNAL_URL and returns the certificate
// hostname. Only "https://host" is accepted: the hostname is the entire
// public identity. There is no port — clients are routed by the authority
// each request actually arrives on, so the public port is wherever the
// page is served from.
func parseExternalURL(raw string) (string, error) {
	u, perr := url.Parse(strings.TrimSuffix(raw, "/"))
	switch {
	case perr != nil:
		return "", fmt.Errorf("invalid EXTERNAL_URL %q: %v", raw, perr)
	case u.Scheme != "https":
		return "", fmt.Errorf("invalid EXTERNAL_URL %q: must be an https:// URL", raw)
	case u.Hostname() == "" || u.Port() != "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil:
		return "", fmt.Errorf("invalid EXTERNAL_URL %q: expected https://host with nothing else (no port — the public port is wherever the page is reached)", raw)
	}
	return u.Hostname(), nil
}
