# syntax=docker/dockerfile:1

ARG DEBIAN_VERSION=bookworm
ARG EMSDK_IMAGE=emscripten/emsdk:latest
ARG GO_IMAGE=golang:1.24

ARG FTEQW_REPO=https://github.com/fte-team/fteqw.git
ARG FTEQW_REF=

FROM debian:${DEBIAN_VERSION} AS fteqw-source
ARG FTEQW_REPO
ARG FTEQW_REF
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates git \
  && rm -rf /var/lib/apt/lists/*
WORKDIR /build
RUN if [ -n "${FTEQW_REF}" ]; then \
      git clone --depth 1 --branch "${FTEQW_REF}" "${FTEQW_REPO}" fteqw; \
    else \
      git clone --depth 1 "${FTEQW_REPO}" fteqw; \
    fi
COPY patches/ /tmp/patches/
WORKDIR /build/fteqw
RUN if ls /tmp/patches/*.patch >/dev/null 2>&1; then git apply /tmp/patches/*.patch; fi

FROM debian:${DEBIAN_VERSION} AS server-builder
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates git build-essential pkg-config zlib1g-dev \
  && rm -rf /var/lib/apt/lists/*
WORKDIR /build
COPY --from=fteqw-source /build/fteqw /build/fteqw
WORKDIR /build/fteqw/engine
RUN make sv-rel

FROM ${EMSDK_IMAGE} AS web-builder
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates git gzip make tar wget xz-utils \
  && rm -rf /var/lib/apt/lists/*
WORKDIR /build
COPY --from=fteqw-source /build/fteqw /build/fteqw
WORKDIR /build/fteqw/engine
RUN make web-rel

FROM ${GO_IMAGE} AS nexus-builder
WORKDIR /src/nexus
COPY nexus/go.mod nexus/go.sum ./
RUN go mod download
COPY nexus/ ./
RUN CGO_ENABLED=0 go build -trimpath -o /out/nexus .

# Quake II gamecode: FTE has no built-in Q2 game logic and loads a native
# baseq2/game.so. Retail/GOG data ships only the Windows DLL, so build the
# GPL yamagi-quake2 baseq2 game library (classic Q2 game API v3) for this arch.
FROM debian:${DEBIAN_VERSION} AS q2game-builder
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates git build-essential \
  && rm -rf /var/lib/apt/lists/*
RUN git clone --depth 1 https://github.com/yquake2/yquake2.git /src/yquake2
WORKDIR /src/yquake2
RUN make game   # -> release/baseq2/game.so

FROM debian:${DEBIAN_VERSION}-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends \
    bash ca-certificates zlib1g openssl \
  && rm -rf /var/lib/apt/lists/*

RUN mkdir -p /app/bin /app/client/gamedata /app/etc /app/game /app/logs /app/cd /app/cert /app/q2game

# Server + relay binaries (on PATH via BIN_DIR; servers.ini calls bare "fteqw-sv").
COPY --from=server-builder /build/fteqw/engine/release/fteqw-sv /app/bin/fteqw-sv
COPY --from=nexus-builder  /out/nexus                            /app/bin/nexus

# Bundled Q2 gamecode; the entrypoint provisions it into baseq2/ when serving Q2.
COPY --from=q2game-builder /src/yquake2/release/baseq2/game.so   /app/q2game/game.so

# FTE WASM client + the trunk boot page.
COPY --from=web-builder /build/fteqw/engine/release/ftewebgl.js      /app/client/ftewebgl.js
COPY --from=web-builder /build/fteqw/engine/release/ftewebgl.wasm    /app/client/ftewebgl.wasm
COPY --from=web-builder /build/fteqw/engine/release/ftewebgl.js.gz   /app/client/ftewebgl.js.gz
COPY --from=web-builder /build/fteqw/engine/release/ftewebgl.wasm.gz /app/client/ftewebgl.wasm.gz
# Assemble the NexQuake shell page (loader/PWA/overlay/touch/rcon) around the FTE
# client: concatenate shell/*.js -> nq-shell.js and template shell.html -> index.html.
COPY client/shell /tmp/shell
COPY client/build-shell.sh /tmp/build-shell.sh
RUN bash /tmp/build-shell.sh /tmp/shell /app/client && rm -rf /tmp/shell /tmp/build-shell.sh

# Orchestration config: servers.ini (launch plan) + game.json (asset catalog).
COPY etc/servers.ini /app/game/servers.ini
COPY etc/game.json   /app/etc/game.json

COPY docker/entrypoint-nexus.sh /app/entrypoint.sh
RUN chmod +x /app/bin/fteqw-sv /app/bin/nexus /app/entrypoint.sh

ENV HTTP_PORT=26000 \
    GAME_DIR=/app/game \
    CFG_DIR=/app/etc \
    CLIENT_DIR=/app/client \
    LOGS_DIR=/app/logs \
    BIN_DIR=/app/bin \
    SERVER_DIR=/app/bin \
    CD_DIR=/app/cd \
    GAME_SV_ADDR=127.0.0.1:27500 \
    GAMEDIR=id1

WORKDIR /app
EXPOSE 26000/tcp

ENTRYPOINT ["/app/entrypoint.sh"]
CMD []
