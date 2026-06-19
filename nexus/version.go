package main

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

// These are set at build time via -ldflags.
// Example:
//
//	-X github.com/0xBrsm/NexQuake/nexus.gitSHA=$GITHUB_SHA -X github.com/0xBrsm/NexQuake/nexus.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)
var (
	gitSHA    = "dev"
	buildTime = ""
)

// versionInfo carries build-time metadata returned by the /health endpoint
// and the --version CLI command.
type versionInfo struct {
	GitSHA    string `json:"git_sha"`
	BuildTime string `json:"build_time,omitempty"`
	GoVersion string `json:"go_version"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
}

// currentVersionInfo returns build metadata. When buildTime is not set via
// -ldflags it defaults to the current UTC time (dev builds only).
func currentVersionInfo() versionInfo {
	bt := buildTime
	if bt == "" {
		bt = time.Now().UTC().Format(time.RFC3339)
	}
	return versionInfo{
		GitSHA:    gitSHA,
		BuildTime: bt,
		GoVersion: runtime.Version(),
		GOOS:      runtime.GOOS,
		GOARCH:    runtime.GOARCH,
	}
}

// handleCLI processes CLI-only sub-commands (--version, --healthcheck).
// It returns (true, exitCode) when a sub-command was matched and the process
// should exit; (false, 0) means normal server startup should proceed.
func handleCLI(args []string) (handled bool, exitCode int) {
	if len(args) == 0 {
		return false, 0
	}

	switch args[0] {
	case "--version", "version":
		// Keep this simple so it's usable inside minimal runtime images.
		v := currentVersionInfo()
		fmt.Printf("nexquake-nexus git_sha=%s build_time=%s go=%s %s/%s\n",
			v.GitSHA,
			v.BuildTime,
			v.GoVersion,
			v.GOOS,
			v.GOARCH,
		)
		return true, 0
	case "--healthcheck", "healthcheck":
		// Used by Docker/compose healthchecks. Do not require curl/wget/bash in the image.
		httpPort := getEnv("HTTP_PORT", "1337")
		if err := runHealthcheck(httpPort); err != nil {
			fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
			return true, 1
		}
		return true, 0
	default:
		return false, 0
	}
}

// runHealthcheck performs an HTTP GET against the local /health endpoint.
// Designed for Docker/compose healthchecks — avoids needing curl or bash.
func runHealthcheck(httpPort string) error {
	scheme := "http"
	var transport http.RoundTripper
	if external := strings.TrimSpace(os.Getenv("EXTERNAL_URL")); external != "" {
		// The listener serves TLS for EXTERNAL_URL's hostname; this is a local
		// liveness probe against 127.0.0.1, so certificate identity can't match
		// and isn't what's being checked — hence InsecureSkipVerify.
		//
		// But the ClientHello's SNI still matters: under ACME, autocert's
		// HostPolicy whitelists only EXTERNAL_URL's host and aborts the
		// handshake for any other (or missing) server name — which is what a
		// bare 127.0.0.1 dial sends. That handshake failure made the container
		// read unhealthy even while it served real traffic fine. Set ServerName
		// to the whitelisted host so the SNI matches and the cert is served;
		// verification is still skipped, so the 127.0.0.1 dial target is moot.
		scheme = "https"
		tlsCfg := &tls.Config{InsecureSkipVerify: true}
		if host, err := parseExternalURL(external); err == nil {
			tlsCfg.ServerName = host
		}
		transport = &http.Transport{TLSClientConfig: tlsCfg}
	}
	url := fmt.Sprintf("%s://127.0.0.1:%s/health", scheme, httpPort)

	client := http.Client{Timeout: 2 * time.Second, Transport: transport}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", resp.Status)
	}

	// Assert cert health explicitly. A cert that can't be issued at all already
	// aborts the handshake above, but InsecureSkipVerify accepts an expired one
	// — so an expiry that slipped past renewal would otherwise read healthy.
	// Identity is intentionally not checked (the dial targets 127.0.0.1).
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		if leaf := resp.TLS.PeerCertificates[0]; time.Now().After(leaf.NotAfter) {
			return fmt.Errorf("TLS certificate expired %s", leaf.NotAfter.Format(time.RFC3339))
		}
	}

	return nil
}
