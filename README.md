# fteqw-WebQuake

Single Docker image for **NetQuake** that runs a dedicated server + a WebAssembly client (served by nginx).

The image does **not** ship any game data. Mount your Quake data into the container.

## How it works

- The container serves the WASM client over HTTP on `:26000`.
- nginx reverse-proxies websocket gameplay traffic from `ws(s)://host/nq` to an internal `fteqw-sv` instance (not exposed publicly).
- The web client fetches packages from the container at `/gamedata/...` using a generated `/index.fmf` manifest.
- The in-game server browser is forced to a same-origin server list at `/servers.txt` (so it shows only the backend server(s) you run).
- A small in-container poller keeps `/servers.txt` up to date with live status (map/players/etc).

## Quickstart (docker compose)

1) Put your game data under `./gamedata` (example: `./gamedata/id1/pak0.pak`).
2) Run:

```bash
docker compose up --build
```

- Web client: `http://localhost:26000`
- Gameplay websocket: `ws://localhost:26000/nq`

## Quickstart (docker run)

```bash
docker run --rm -it \
  -v /path/to/quake:/gamedata:ro \
  -p 26000:26000 \
  fteqw-webquake:latest
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
- `NQ_PORT` (default: `27500`) internal server port (both UDP for polling + TCP for websockets; not published)
- `SERVER_PUBLIC` (default: `0`)
- `SERVER_ARGS` extra args appended to the server commandline
- `SERVER_PORT` (default: `26000`) public port used for UI/legacy connection overrides
- `SERVER_HOST` (default: empty ⇒ use `window.location.hostname`)
- `WS_SCHEME` (`auto`|`ws`|`wss`, default: `auto`)
- `CONNECT` (optional full override, e.g. `ws://example.com:26000/nq`)
- `SERVER_LIST_URL` (optional) URL that returns a plaintext server list for the in-game browser; default is same-origin `/servers.txt`

Server list poller:
- `SERVERLIST_INTERVAL` (default: `5`) seconds between polls
- `SERVERLIST_HOSTNAME` (default: `NetQuake`) label shown in the list
- `SERVERLIST_MAXCLIENTS_DEFAULT` (default: `16`) used only if the server doesn't report a maxclients key

Passing extra server args:
- Any args after the image name are passed through to `fteqw-sv` (after `SERVER_ARGS`).
