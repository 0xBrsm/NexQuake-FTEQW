# fteqw-WebQuake

Single Docker image that can run:

- `server`: headless FTEQW dedicated server (native Linux, `amd64`/`arm64`)
- `client`: WebAssembly client served by nginx (game data served from a bind mount)

The image does **not** ship any game data. Mount your Quake data into both containers.

## Quickstart (docker compose)

1) Put your game data under `./gamedata` (example: `./gamedata/id1/pak0.pak`).
2) Run:

```bash
docker compose up --build
```

- Web client: `http://localhost:8080`
- Server port: `27500/tcp` (web clients use `ws://`)

## Quickstart (docker run)

Server:

```bash
docker run --rm -it \
  -v /path/to/quake:/gamedata:ro \
  -p 27500:27500 \
  fteqw-webquake:latest server
```

Client:

```bash
docker run --rm -it \
  -v /path/to/quake:/gamedata:ro \
  -p 8080:8080 \
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

Server mode:
- `SERVER_PORT` (default: `27500`) TCP listen port (websocket clients)
- `SERVER_PUBLIC` (default: `0`)
- `SERVER_ARGS` extra args appended to the server commandline

Client mode:
- `SERVER_HOST` (default: empty ⇒ use `window.location.hostname`)
- `SERVER_PORT` (default: `27500`)
- `WS_SCHEME` (`auto`|`ws`|`wss`, default: `auto`)
- `CONNECT` (optional full override, e.g. `ws://example.com:27500/`)
