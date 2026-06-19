package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSetupTLS_DefaultPlainHTTP(t *testing.T) {
	t.Setenv("EXTERNAL_URL", "")
	t.Setenv("CERT_DIR", t.TempDir())

	rt, err := setupTLS(context.Background())
	if err != nil {
		t.Fatalf("setupTLS: %v", err)
	}
	if rt.serverTLS != nil {
		t.Fatal("no EXTERNAL_URL must mean no server TLS")
	}
	if rt.getWTCert != nil {
		t.Fatal("no EXTERNAL_URL must not enable WebTransport")
	}
}

func TestSetupTLS_ExternalURLEnablesTLSAndWT(t *testing.T) {
	t.Setenv("EXTERNAL_URL", "https://quake.example.com")
	t.Setenv("CERT_DIR", t.TempDir())

	rt, err := setupTLS(context.Background())
	if err != nil {
		t.Fatalf("setupTLS: %v", err)
	}
	if rt.serverTLS == nil {
		t.Fatal("EXTERNAL_URL must configure server TLS")
	}
	if rt.getWTCert == nil {
		t.Fatal("EXTERNAL_URL must enable WebTransport")
	}
}

func TestSetupTLS_BYOCertServedDirectly(t *testing.T) {
	dir := t.TempDir()
	writeTestCertPair(t, dir, "quake.example.com")

	t.Setenv("EXTERNAL_URL", "https://quake.example.com")
	t.Setenv("CERT_DIR", dir)

	rt, err := setupTLS(context.Background())
	if err != nil {
		t.Fatalf("setupTLS: %v", err)
	}
	if rt.serverTLS == nil || rt.getWTCert == nil {
		t.Fatal("BYO cert must configure both server TLS and WebTransport")
	}
	// The cert source must hand back exactly the on-disk pair (no ACME).
	cert, err := rt.getWTCert(&tls.ClientHelloInfo{ServerName: "quake.example.com"})
	if err != nil {
		t.Fatalf("getWTCert: %v", err)
	}
	if cert == nil || len(cert.Certificate) == 0 {
		t.Fatal("BYO getWTCert returned an empty certificate")
	}
	// A BYO pair is resolved into wtCert immediately (no network), so the WT
	// listener serves it straight away via resolvedCert.
	if rt.certHost != "quake.example.com" {
		t.Fatalf("certHost = %q, want quake.example.com", rt.certHost)
	}
	wtCert, err := rt.getWTCert(&tls.ClientHelloInfo{ServerName: "quake.example.com"})
	if err != nil {
		t.Fatalf("getWTCert: %v", err)
	}
	if leaf := certLeaf(wtCert); leaf == nil || leaf.Subject.CommonName != "quake.example.com" {
		t.Fatalf("getWTCert returned %+v, want leaf for quake.example.com", leaf)
	}
	// resolveCert returns the same on-disk pair without touching the network.
	resolved, err := rt.resolveCert(context.Background())
	if err != nil {
		t.Fatalf("resolveCert: %v", err)
	}
	if certLeaf(resolved).Subject.CommonName != "quake.example.com" {
		t.Fatal("resolveCert returned the wrong certificate")
	}
}

func TestSetupTLS_PartialBYOCertFallsBackToACME(t *testing.T) {
	dir := t.TempDir()
	// Only cert.pem, no key.pem: an incomplete pair must not be treated as BYO.
	if err := os.WriteFile(filepath.Join(dir, "cert.pem"), []byte("not-a-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EXTERNAL_URL", "https://quake.example.com")
	t.Setenv("CERT_DIR", dir)

	rt, err := setupTLS(context.Background())
	if err != nil {
		t.Fatalf("setupTLS: %v", err)
	}
	if rt.serverTLS == nil || rt.getWTCert == nil {
		t.Fatal("incomplete BYO pair must fall back to the ACME path")
	}
}

// writeTestCertPair mints a throwaway self-signed cert for host and writes it
// as cert.pem + key.pem under dir, mirroring a BYO deployment.
func writeTestCertPair(t *testing.T, dir, host string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{host},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(filepath.Join(dir, "cert.pem"), certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "key.pem"), keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
}

// makeTestCert mints a throwaway self-signed ECDSA cert as an in-memory
// *tls.Certificate (with Leaf set), for exercising the cert holder directly.
func makeTestCert(t *testing.T, cn string) *tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     []string{cn},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

// TestWTCertNeverResolvesLive is the regression for the WebTransport black-hole:
// WT used to serve its cert via autocert's GetCertificate, which blocks inside
// the QUIC handshake (its TLS-ALPN-01 challenge can't complete on the h3-only
// listener) and silently kills every WT session. resolvedCert must serve only
// the pre-resolved cert and never invoke resolveCert — so even a hung resolve
// can't stall a handshake.
func TestWTCertNeverResolvesLive(t *testing.T) {
	rt := &tlsRuntime{certHost: "quake.example.com"}
	rt.getWTCert = rt.resolvedCert
	rt.resolveCert = func(ctx context.Context) (*tls.Certificate, error) {
		<-ctx.Done() // stand in for autocert hanging in the handshake
		return nil, ctx.Err()
	}

	// Before the startup resolve lands: a fast error, not a hang.
	done := make(chan error, 1)
	go func() {
		_, err := rt.getWTCert(&tls.ClientHelloInfo{ServerName: "quake.example.com"})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error before the cert is resolved")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("getWTCert blocked: it must serve the pre-resolved cert, never call resolveCert live")
	}

	// Once resolution lands, serve it — still without invoking resolveCert.
	want := makeTestCert(t, "quake.example.com")
	rt.wtCert.Store(want)
	got, err := rt.getWTCert(&tls.ClientHelloInfo{ServerName: "quake.example.com"})
	if err != nil {
		t.Fatalf("getWTCert after resolve: %v", err)
	}
	if got != want {
		t.Fatal("getWTCert did not return the resolved cert from the holder")
	}
}

// TestResolveTLSCertPopulatesHolder checks the single startup step stores the
// resolved cert where the WT listener reads it.
func TestActivateTLSCertPopulatesHolder(t *testing.T) {
	want := makeTestCert(t, "quake.example.com")
	rt := &tlsRuntime{
		certHost:    "quake.example.com",
		resolveCert: func(context.Context) (*tls.Certificate, error) { return want, nil },
	}
	rt.getWTCert = rt.resolvedCert

	// Synchronous gate: returns once the cert is in hand, no polling.
	if err := activateTLSCert(context.Background(), rt); err != nil {
		t.Fatalf("activateTLSCert: %v", err)
	}
	if rt.wtCert.Load() == nil {
		t.Fatal("activateTLSCert did not populate wtCert")
	}
	got, err := rt.getWTCert(&tls.ClientHelloInfo{ServerName: "quake.example.com"})
	if err != nil || got != want {
		t.Fatalf("getWTCert = (%v, %v), want the resolved cert", got, err)
	}
}

func TestActivateTLSCertNoTLSIsNoop(t *testing.T) {
	if err := activateTLSCert(context.Background(), &tlsRuntime{}); err != nil {
		t.Fatalf("activateTLSCert with TLS off: %v", err)
	}
}

func TestParseExternalURL(t *testing.T) {
	cases := []struct {
		in, host string
	}{
		{"https://quake.example.com", "quake.example.com"},
		{"https://quake.example.com/", "quake.example.com"},
		{"https://localhost", "localhost"},
	}
	for _, c := range cases {
		host, err := parseExternalURL(c.in)
		if err != nil {
			t.Fatalf("parseExternalURL(%q): %v", c.in, err)
		}
		if host != c.host {
			t.Fatalf("parseExternalURL(%q) = %q, want %q", c.in, host, c.host)
		}
	}

	for _, bad := range []string{
		"http://quake.example.com",
		"quake.example.com",
		"https://",
		"https://quake.example.com:443",
		"https://quake.example.com:8443",
		"https://quake.example.com/play",
		"https://quake.example.com?x=1",
		"https://user@quake.example.com",
	} {
		if _, err := parseExternalURL(bad); err == nil {
			t.Fatalf("parseExternalURL(%q) must fail", bad)
		}
	}
}
