package main

import "testing"

func TestIsBenignHandshakeNoise(t *testing.T) {
	// Real scanner/probe noise observed on a public 443 — all demoted to debug.
	benign := []string{
		"http: TLS handshake error from 1.2.3.4:5: acme/autocert: missing server name",
		// Probe sending an SNI for a host we don't serve — autocert's HostWhitelist
		// rejects it. Names someone else's host, so it's a scanner, not our problem.
		"http: TLS handshake error from 1.2.3.4:5: acme/autocert: host \"scanner-target.example\" not configured in HostWhitelist",
		"http: TLS handshake error from 1.2.3.4:5: tls: client offered only unsupported versions: [302]",
		"http: TLS handshake error from 1.2.3.4:5: tls: unsupported SSLv2 handshake received",
		"http: TLS handshake error from 1.2.3.4:5: tls: no cipher suite supported by both client and server; client offered: [16 33]",
		"http: TLS handshake error from 1.2.3.4:5: read tcp 10.0.0.1:1337->1.2.3.4:5: read: connection reset by peer",
		"http: TLS handshake error from 1.2.3.4:5: EOF",
	}
	for _, line := range benign {
		if !isBenignHandshakeNoise(line) {
			t.Errorf("expected benign (debug): %q", line)
		}
	}

	// Must stay visible at warn: a genuine cert-issuance failure (client did
	// send a valid SNI) and any non-handshake server error.
	loud := []string{
		"http: TLS handshake error from 1.2.3.4:5: acme/autocert: rate limited by ACME CA",
		"http: TLS handshake error from 1.2.3.4:5: acme/autocert: dev.quake.nexus: unable to authorize",
		// Raw ACME protocol error from Let's Encrypt (no "acme/autocert:" prefix)
		// — the duplicate-certificate rate limit, exactly as observed on the pi.
		"http: TLS handshake error from 1.2.3.4:5: 429 urn:ietf:params:acme:error:rateLimited: too many certificates (5) already issued for this exact set of identifiers in the last 168h0m0s, retry after 2026-06-15 13:00:41 UTC",
		"http: Accept error: too many open files; retrying",
		"http2: server: error reading preface from client: timeout",
	}
	for _, line := range loud {
		if isBenignHandshakeNoise(line) {
			t.Errorf("expected loud (warn), got benign: %q", line)
		}
	}
}
