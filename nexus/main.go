package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/0xBrsm/NexQuake/nexus/internal/access"
	"github.com/0xBrsm/NexQuake/nexus/internal/admin"
	"github.com/0xBrsm/NexQuake/nexus/internal/assets"
	"github.com/0xBrsm/NexQuake/nexus/internal/clients"
	"github.com/0xBrsm/NexQuake/nexus/internal/orch"
	"github.com/0xBrsm/NexQuake/nexus/trunk"
)

// nexusApp is the central application object. It wires together the
// networking layer (IP allocator, session registry), game server orchestration
// (server manager, info poller), and the admin subsystem into a single
// coherent unit that drives the HTTP server lifecycle. Transport modules
// attach themselves via their respective setup functions.
type nexusApp struct {
	cfg         runtimeConfig
	access      *access.Gate
	clients     *clients.Registry
	admin       *admin.Admin
	trunk       *trunk.Trunk
	serverMgr   *orch.ServerManager
	assetServer *assets.HashedAssetServer

	bootstrapClientFields []func(r *http.Request) map[string]any
}

func main() {
	initLogging()
	auditLogger := initSlog()

	if handled, exitCode := handleCLI(os.Args[1:]); handled {
		os.Exit(exitCode)
	}

	if err := configureNexusLogFile(getEnv("LOGS_DIR", "/app/logs")); err != nil {
		fatalf("Failed to initialize Nexus log file: %v", err)
	}
	defer closeNexusLogFile()
	slog.Info("== Welcome to NexQuake! ==")

	cfg := loadRuntimeConfig()

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()

	// Bind and TLS are hard preconditions — nothing else starts until both
	// pass. Resolve the TLS config (reads env) and bind the listener; a port
	// conflict fails here. The cert itself is resolved further down, once the
	// server is accepting (ACME's TLS-ALPN-01 challenge lands on this listener,
	// so it must be live to issue), and a failure there is fatal too. Only
	// after that gate does Nexus announce "listening … with TLS" and launch the
	// game servers. TLS and WebTransport are both gated on EXTERNAL_URL, so
	// "with TLS" implies HTTPS/WSS + WebTransport (vs plain HTTP + WebSocket).
	tlsRT, err := setupTLS(runCtx)
	if err != nil {
		fatalf("TLS setup: %v", err)
	}
	listener, err := net.Listen("tcp", ":"+cfg.httpPort)
	if err != nil {
		fatalf("Failed to listen on port %s: %v", cfg.httpPort, err)
	}

	// Initialize access management. HTTP endpoints resolve callers once per
	// request; /rcon is just one route that asks for admin capability.
	authCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	jwt, err := access.InitJWT(authCtx)
	cancel()
	if err != nil {
		fatalf("Failed to initialize OIDC: %v", err)
	}
	auth, err := access.InitAuth()
	if err != nil {
		fatalf("%v", err)
	}
	blocklist := access.NewBlocklist()

	nqServerIP := net.ParseIP("127.0.0.1").To4()
	id := access.NewIdentity()
	accessGate := access.NewGate(jwt, id, auth, blocklist)

	for _, dir := range []string{cfg.binDir, cfg.serverBinDir} {
		if err := prependPath(dir); err != nil {
			fatalf("Failed to configure PATH: %v", err)
		}
	}

	// Validate runtime artifacts early; avoids confusing partial startup.
	indexPath := filepath.Join(cfg.clientDir, "index.html")
	if st, err := os.Stat(indexPath); err != nil || st.IsDir() {
		fatalf("WASM client not found: %s", cfg.clientDir)
	}

	// Bootstraps servers.ini (only if missing) and seeds missing mod data based on
	// servers.ini -game entries and CFG_DIR/game.json. `base` catalog entries are
	// always included in the install set, and QUICKSTART defaults to `ffa`.
	if err := assets.QuickstartGame(runCtx, cfg.gameDir, cfg.cfgDir); err != nil {
		fatalf("Quickstart failed: %v", err)
	}

	// Build the server manager now (the mux/admin reference it), but defer the
	// actual launch until after the TLS gate below — nothing starts until TLS
	// is confirmed.
	serverMgr := orch.NewServerManager(
		cfg.gameDir,
		cfg.logsDir,
		infofNoTail,
		formatTimestampedLogText,
	)
	serverMgr.SetServerMaxInstances(cfg.serverMaxInstances)

	app := &nexusApp{
		cfg:         cfg,
		access:      accessGate,
		serverMgr:   serverMgr,
		assetServer: assets.NewHashedAssetServer(cfg.gameDir, cfg.cdDir, assets.NewPakIndexCache()),
	}
	app.trunk = trunk.New(
		trunk.WithServerIP(nqServerIP),
		trunk.WithControlHandler(app.handleControlFrame),
	)
	app.clients = clients.NewRegistry(app.trunk)
	app.admin = admin.New(app.clients, serverMgr, auditLogger, tailNexusLogLines, accessGate)

	// Surface the public OIDC parameters to the WASM client so the in-game
	// `rcon login` can run a client-side Authorization Code + PKCE flow when
	// Nexus is exposed directly (no fronting access gate to inject a JWT).
	// Absent OIDC config this stays nil and the client keeps its
	// password / edge-gated login paths.
	if oidc := accessGate.OIDCBrowserConfig(); oidc != nil {
		app.AddBootstrapClientFields(func(*http.Request) map[string]any {
			return map[string]any{"oidc": oidc}
		})
	}

	// Build the shared HTTP mux and let each transport module attach.
	mux := app.newMux()
	setupWebSocket(app, mux)

	wtServer, err := setupWebTransport(app, tlsRT)
	if err != nil {
		fatalf("WebTransport setup: %v", err)
	}

	server := &http.Server{
		Addr:              ":" + cfg.httpPort,
		Handler:           accessGate.HTTPGate(mux),
		TLSConfig:         tlsRT.serverTLS,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		// Route the server's internal errors through slog and demote benign
		// per-connection TLS handshake noise (SNI-less scanners, etc.) to debug.
		ErrorLog: newServerErrorLog(),
	}

	// Start serving so the listener can answer ACME's TLS-ALPN-01 challenge,
	// then block on the cert — a TLS failure is fatal here, before anything
	// else starts. BYO certs resolve instantly; plain HTTP skips the gate.
	go func() {
		var err error
		if tlsRT.serverTLS != nil {
			// Certs come from TLSConfig.GetCertificate; no key pair files.
			err = server.ServeTLS(listener, "", "")
		} else {
			err = server.Serve(listener)
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatalf("Server error: %v", err)
		}
	}()

	listenMsg := fmt.Sprintf("Nexus listening on port %s", cfg.httpPort)
	if tlsRT.serverTLS != nil {
		if err := activateTLSCert(runCtx, tlsRT); err != nil {
			fatalf("TLS activation failed: %v", err)
		}
		listenMsg += " with TLS"
	}
	slog.Info(listenMsg)

	// Past the gate: report admin access, start the WebTransport listener (now
	// serving the resolved cert), launch the game servers, and begin the info
	// poller. A background goroutine refreshes the cert on autocert renewal.
	var methods []string
	if auth.HasRconPassword() {
		methods = append(methods, "password")
	}
	if jwt != nil && auth.AllowsJWTAdmin() {
		methods = append(methods, "SSO")
	}
	if len(methods) == 0 {
		slog.Info("Admin access disabled")
	} else {
		slog.Info(fmt.Sprintf("Admin access by %s", strings.Join(methods, ", ")))
	}

	if wtServer != nil {
		go func() {
			// webtransport-go's Close() cancels the context its accept loop
			// waits on, so ListenAndServe returns context.Canceled on a normal
			// shutdown (not http.ErrServerClosed). Treat both as expected.
			if err := wtServer.ListenAndServe(); err != nil &&
				!errors.Is(err, http.ErrServerClosed) &&
				!errors.Is(err, context.Canceled) {
				slog.Error(fmt.Sprintf("%s server error: %v", trunk.TransportWebTransport, err))
			}
		}()
	}

	if err := serverMgr.StartAll(); err != nil {
		fatalf("Failed to start servers: %v", err)
	}

	// Server info poller (used for Quake's `slist`).
	stopInfoPoller := serverMgr.StartInfoPoller(runCtx, nqServerIP)

	// Refresh the served cert on autocert renewal. No-op for BYO / plain HTTP.
	go refreshTLSCert(runCtx, tlsRT)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	slog.Info("Shutting down gracefully...")
	runCancel()
	stopInfoPoller()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		fatalf("Server shutdown failed: %v", err)
	}
	if wtServer != nil {
		_ = wtServer.Close()
	}

	_ = serverMgr.StopAll(ctx, 2*time.Second)
	slog.Info("Nexus stopped")
}
