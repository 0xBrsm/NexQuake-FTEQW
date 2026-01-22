# fteqw-WebQuake

Single Docker image for **NetQuake** that can run:

- `all` (default): NetQuake dedicated server + WebAssembly client (served by nginx)
- `server`: headless dedicated server only
- `client`: WebAssembly client only

The image does **not** ship any game data. Mount your Quake data into the container.

## How it works

- The container serves the WASM client over HTTP on `:26000`.
- nginx reverse-proxies websocket gameplay traffic from `ws(s)://host/nq` to an internal `fteqw-sv` instance (not exposed publicly).
- The web client fetches packages from the container at `/gamedata/...` using a generated `/index.fmf` manifest.
- The in-game server browser is forced to a same-origin server list at `/servers.txt` (so it shows only the backend servers you run).

## Quickstart (docker compose)

1) Put your game data under `./gamedata` (example: `./gamedata/id1/pak0.pak`).
2) Run:

```bash
docker compose up --build
```

- Web client: `http://localhost:26000`
- Gameplay websocket: `ws://localhost:26000/nq`

## Quickstart (docker run)

Server + client (default):

```bash
docker run --rm -it \
  -v /path/to/quake:/gamedata:ro \
  -p 26000:26000 \
  fteqw-webquake:latest
```

Client-only:

```bash
docker run --rm -it \
  -v /path/to/quake:/gamedata:ro \
  -p 26000:26000 \
  fteqw-webquake:latest client
```

## Notes on game data

- Do not commit/share commercial game data (for Quake this includes `pak0.pak`/`pak1.pak`).
- The repo keeps `gamedata/` tracked but empty; mount your own data at runtime.
- The web client fetches packages from the container at `/gamedata/...` when it loads (via the generated `/index.fmf` manifest).

## Build (multi-arch)

```bash
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  -t yourname/fteqw-webquake:latest \
  --push \
  .
```

## Runtime knobs

Common:
- `BASEDIR` (default: `/gamedata`)
- `GAMEDIR` (default: `id1`)
- `BASEGAMES` (default: empty, comma-separated)

Modes:
- `MODE` (`all`|`server`|`client`, default: `all`)

Server (applies to `all` and `server`):
- `NQ_PORT` (default: `27500`) NetQuake websocket server port (internal in `all` mode; publish it only if you run `server` directly)
- `SERVER_PUBLIC` (default: `0`)
- `SERVER_ARGS` extra args appended to the server commandline

Client (applies to `all` and `client`):
- `SERVER_PORT` (default: `26000`) public port used for UI/legacy connection overrides
- `SERVER_HOST` (default: empty ⇒ use `window.location.hostname`)
- `WS_SCHEME` (`auto`|`ws`|`wss`, default: `auto`)
- `CONNECT` (optional full override, e.g. `ws://example.com:26000/nq`)
- `SERVER_LIST_URL` (optional) URL that returns a plaintext server list for the in-game browser; default is same-origin `/servers.txt`
