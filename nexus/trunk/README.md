# Trunk

Per-client browser↔UDP tunnel library. One `Trunk` manages the runtime
state — VirtualIP pool and active session registry — and produces per-client
`Session` instances via `Trunk.NewSession`. A pluggable `Transport`
(e.g. WebSocket, WebTransport) carries binary frames with a 2-byte
big-endian port header, which the session demultiplexes to UDP datagrams
aimed at a localhost backend. Each client is assigned a deterministic
127.x.x.x VirtualIP so the backend sees distinct source addresses.

**Import path:** `github.com/0xBrsm/NexQuake/nexus/trunk`
**Module:** `github.com/0xBrsm/NexQuake/nexus` (`src/nexus/go.mod`)

Full API documentation (wire format, usage example): `go doc ./...` or
[pkg.go.dev](https://pkg.go.dev/github.com/0xBrsm/NexQuake/nexus/trunk).

## Package layout

| File | Responsibility |
|---|---|
| `trunk.go` | `Trunk` type, functional options, `SessionInfo`, session registry |
| `session.go` | `Session` type, `Transport` interface, `ControlHandler`/`PortFilter` callbacks, lifecycle API |
| `relay.go` | I/O engine: frame encoding, tunnel read/write loops, UDP read/write loops |
| `vip.go` | Deterministic 127.x.x.x VirtualIP allocator |

Adapter sub-packages implement `Transport` for specific protocols:

| Package | Transport |
|---|---|
| `trunk/websocket` | WebSocket via `gorilla/websocket` (inbound messages capped at 64 KiB) |
| `trunk/webtransport` | WebTransport (QUIC datagrams) via `quic-go/webtransport-go`; oversized datagrams are dropped like UDP loss, not session failures |

## Vendoring checklist

1. Frame format is fixed: 2-byte big-endian port header + payload. Keep client and relay in sync.
2. Port `0` is the control channel. Define your own payload semantics in `WithControlHandler`.
3. Override `Upgrader.CheckOrigin` for production deployments.
4. Set `WithPortFilter` to restrict clients to specific UDP destinations.
5. Keep `sourceKey` stable across reconnects if deterministic VirtualIP identity matters.
