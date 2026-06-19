# Nexus Service

Go orchestration service that serves the WASM client, serves game data, tunnels multiplayer traffic over WebSocket or WebTransport, and manages dedicated server processes.

## Package Layout

| Dir | Purpose |
|-----|---------|
| `trunk/` | **Networking relay.** Standalone tunnel↔UDP relay (WebSocket and WebTransport adapters) with deterministic VirtualIP (`127.x.x.x`) allocation and control-frame callback hooks. No HTTP/auth/application imports. |
| `internal/orch/` | **Orchestration.** Dedicated server launch/lifecycle, instance autoscaling/reconcile, server console capture, and `slist` polling/aggregation. No `trunk` or `admin` imports. |
| `internal/access/` | **HTTP access policy.** Caller identity, OIDC login flow + JWT verification, RCON/admin authorization rules, and source-IP blocklist. |
| `internal/clients/` | **Client presence.** Joined runtime view of access identities and active trunk sessions, keyed by VirtualIP for client list/info/ban consumers. |
| `internal/admin/` | **Admin control plane.** `/rcon` JSON-RPC 2.0 dispatch and client/server commands over narrow consumer-side interfaces. |
| `internal/assets/` | **Game data gateway.** Quickstart manifests, VFS manifest construction, PAK indexing/streaming, CD index, and hash-addressed asset serving (`/start`, `/nq/<hash>`). |
| `quake106/` | **Shareware extractor.** Standalone Go package that extracts `pak0.pak` from the Quake 1.06 shareware archive with SHA256 verification. See [quake106/README.md](./quake106/README.md). |

## Dependency Boundaries

```text
trunk          (leaf: stdlib; adapters add gorilla/websocket and quic-go/webtransport-go)
internal/orch  (leaf: stdlib + internal/assets)
internal/access (leaf: stdlib + go-oidc + x/oauth2)
internal/clients -> internal/access + trunk
internal/admin  -> internal/clients + internal/orch (consumer-side interfaces)

package main
  -> trunk + internal/orch + internal/access + internal/clients + internal/admin + internal/assets
```

Rules:

- `trunk` and `internal/orch` do not import `internal/admin`.
- `trunk` has no imports from `internal/*` and no app policy logic.
- Cross-subsystem construction is done in package `main` (`main.go`, `connect.go`, `ws.go`, `wt.go`).

## Entry Files

| File | Purpose |
|------|---------|
| `main.go` | Process lifecycle only: init, runtime wiring, HTTP server start, signal handling, graceful shutdown, and CLI subcommands (`--version`, `--healthcheck`). |
| `connect.go` | HTTP mux and connection boundary: route registration (`/health`, `/connect`, `POST /rcon` JSON-RPC, `GET /rcon` login landing page, `/start`, `/nq/`, `/`), top-level access gate wrapping, and `/rcon` JSON-RPC handler. |
| `env.go` | Startup config (`runtimeConfig`): reads cross-cutting env vars once and exposes `PATH`/env helpers. |
| `log.go` | Logging setup (`LOG_LEVEL`, `CONSOLE_TIMESTAMPS`), nexus.log file sink, and operator console ring buffer. |
| `version.go` | Build metadata (`--version`, `/health`), version ldflags, and CLI sub-command handler. |
| `ws.go` | WebSocket transport setup: mounts `/connect` and upgrades connections to trunk sessions. |
| `wt.go` | WebTransport transport setup: HTTP/3 listener on UDP/`HTTP_PORT`, `/connect` Extended CONNECT route, request-derived URL authority in the `/start` manifest. |
| `cert.go` | Run-path resolution (`EXTERNAL_URL`): shared cert source for the HTTPS and WebTransport listeners — a BYO `cert.pem`+`key.pem` under `CERT_DIR`, else ACME/Let's Encrypt via `autocert`. |

## trunk/

Per-client browser↔UDP tunnel. See [trunk/README.md](./trunk/README.md) for vendoring notes and the public API.

| File | Purpose |
|------|---------|
| `trunk.go` | `Trunk` type, functional options, `SessionInfo`, and session registry (`NewSession`, `SessionByVirtualIP`). |
| `session.go` | `Session` type, `Transport` interface, `ControlHandler`/`PortFilter` callbacks, `SendControl`/`TrySendControl`, `End`, and per-client I/O lifecycle. |
| `relay.go` | Frame encoding (2-byte big-endian port prefix), tunnel read/write loops, and UDP read/write loops. |
| `vip.go` | Deterministic `127.x.x.x` VirtualIP allocation from source keys. |
| `websocket/websocket.go` | `Transport` adapter and `Upgrader` for `gorilla/websocket` (64 KiB inbound read limit); the only file importing it. |
| `webtransport/webtransport.go` | `Transport` adapter for `quic-go/webtransport-go` QUIC datagrams; drops oversized datagrams like UDP loss instead of failing the session. |

## internal/access/

Resolves HTTP callers and centralizes access policy before route-specific code runs. All HTTP handlers, including `/health`, `/start`, `/nq/`, `/connect`, `/rcon`, and the static client file server, sit behind the same source-IP block check. `/rcon` additionally asks for admin capability.

| File | Purpose |
|------|---------|
| `auth.go` | OIDC JWT verification (`Authorization: Bearer` or the `AUTH_JWT_HEADER` front-injected header), `Authorization` scheme parsing, optional matcher allowlist (`AUTH_ADMIN_ID`), and `AUTH_RCON_PASSWORD` policy for admin capability. |
| `gate.go` | Request-level access facade, resolved `access.Client`, top-level HTTP gate, source-IP blocklist, and admin authorization. |
| `identity.go` | Client source IP and identity-key resolution: direct connection address by default, trusted header (`AUTH_CLIENT_IP_HEADER`) behind a front. |

## internal/orch/

Manages dedicated server processes and their scaled instances. Parses `.bat`-style `servers.ini`, starts processes under PTY for console capture, polls server info for `slist`, and runs server/instance reconcile and autoscale policy.

| File | Purpose |
|------|---------|
| `autoscale.go` | Server-level instance selection (least-loaded, round-robin tie-break), slist-poll demand accounting, headroom calculation, autoscale scale-up/drain/despawn decisions, and reconcile loops (event-driven + heartbeat). |
| `launch_plan.go` | `servers.ini` parser (`@macro` + `%arg` expansion) and launch plan builder. |
| `launcher.go` | Process start/stop wiring under PTY. |
| `manager.go` | `ServerManager` construction, shared logging hooks, and operator console relay formatting. |
| `registry.go` | Server/instance registry model, instance lifecycle state (`warming/active/draining/terminating`), and aggregate server snapshot refresh. |
| `state.go` | In-memory instance state updates (resolved port/search-path + observed server-info), startup-online transitions, and per-update reconcile trigger. |
| `ops.go` | High-level lifecycle operations (start/stop/restart/remove/launch by 1-based server index). Resolves targets and coordinates instance transitions. |
| `console.go` | PTY-based server console I/O. Captures output lines, detects listen port from console, supports filtered reads for rcon command capture. |
| `rcon.go` | Instance command dispatch (`DispatchInstanceCmd`): brackets the user command with random `__NQX_*` sentinels via `echo`, writes the framed line to the instance PTY, and returns exactly the lines the server emits between BEGIN and END. A safety timeout bounds hung-server cases; there is no idle-wait heuristic. |
| `slist.go` | Server-info poller. Sends `CCREQ_SERVER_INFO` in round-robin, updates cache for WebSocket `slist`, and drives periodic reconcile heartbeat. |

## internal/admin/

Handles JSON-RPC 2.0 commands received on authorized `POST /rcon` requests. Command handlers call small consumer-side interfaces for source blocking, client presence, and server orchestration. The package does not resolve HTTP callers, track active tunnel connections, or make top-level access decisions.

| File | Purpose |
|------|---------|
| `rpc.go` | JSON-RPC 2.0 machinery: `Request`/`Response` envelopes, `ParseRequest`, `Dispatch`, `MethodError`, and per-RPC audit logging. |
| `cmds.go` | Command registry and shared handlers: `rcon.help`, `rpc.discover`, and `logs.tail`. |
| `server.go` | Server commands and orch dispatch (`server.list`, `server.instances`, lifecycle, launch, and instance console commands). |
| `client.go` | Joined active-client commands and helpers (`client.list`, `client.info`, `client.ban`). |

## internal/clients/

Tracks live client presence by joining access identity metadata recorded at `/connect` with active trunk sessions. The registry is keyed by trunk VirtualIP (`nqip`) so multiple sessions from the same source IP remain distinct.

| File | Purpose |
|------|---------|
| `clients.go` | Active client registry, stable connection list, lookup by NQIP, and best-effort disconnect helper. |

## internal/assets/

| File | Purpose |
|------|---------|
| `clientfs.go` | VFS manifest builder: scans `${GAME_DIR}/<mod>/{common,client}` and builds JSON manifests (Quake precedence: loose > PAK, higher PAK wins); also builds CD index from `${CD_DIR}`. |
| `clientfs_pak.go` | PAK parser: indexes PAK headers and exposes file offsets/sizes for stream extraction. |
| `serverfs.go` | Runtime overlay basedir: creates an ephemeral directory of symlinks into `${GAME_DIR}` for the dedicated server's VFS, reconciled per fsnotify event. |
| `gamedir.go` | Lists valid mod directories under `${GAME_DIR}`. |
| `manifest.go` | Runtime asset gateway: serves `/start` quickstart metadata and `/nq/<hash>` hash-addressed asset reads. |
| `manifest_asset.go` | Hashed asset construction and content-type resolution for hash-addressed responses. |
| `quickstart.go` | Quickstart orchestration: seeds `servers.ini` and plans mod installs from `CFG_DIR/game.json` based on `QUICKSTART` and `servers.ini -game` entries. |
| `quickstart_extract.go` | Archive and PAK extraction for quickstart mod layer installs. |
| `quickstart_install.go` | Mod layer install coordinator: fetches, extracts, and writes mod layers to `${GAME_DIR}`. |

## quake106/

Standalone Go package that extracts `pak0.pak` from the Quake 1.06 shareware archive. See [quake106/README.md](./quake106/README.md).

| File | Purpose |
|------|---------|
| `quake106.go` | LZH decompression + PAK extraction with SHA256 verification. |

## Building

```bash
cd src/nexus
go build -o nexus .
```

Static binary (Docker):

```bash
CGO_ENABLED=0 go build -o nexus .
```

## Dependencies

Go 1.24+. Primary dependencies:

- `github.com/gorilla/websocket` (WebSocket tunnel)
- `github.com/quic-go/webtransport-go` + `github.com/quic-go/quic-go` (WebTransport tunnel: QUIC datagrams, HTTP/3 listener)
- `github.com/coreos/go-oidc/v3` (OIDC JWT verification)
- `golang.org/x/crypto/acme/autocert` (Let's Encrypt certificates when `EXTERNAL_URL` is set)
- `github.com/creack/pty` (server console PTY)
- `github.com/google/shlex` (command argument splitting)
