# syntax=docker/dockerfile:1

ARG DEBIAN_VERSION=bookworm
ARG EMSDK_IMAGE=emscripten/emsdk:latest

ARG FTEQW_REPO=https://github.com/fte-team/fteqw.git
ARG FTEQW_REF=

FROM debian:${DEBIAN_VERSION} AS server-builder
ARG FTEQW_REPO
ARG FTEQW_REF
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates git build-essential pkg-config zlib1g-dev \
  && rm -rf /var/lib/apt/lists/*
WORKDIR /build
RUN if [ -n "${FTEQW_REF}" ]; then \
      git clone --depth 1 --branch "${FTEQW_REF}" "${FTEQW_REPO}" fteqw; \
    else \
      git clone --depth 1 "${FTEQW_REPO}" fteqw; \
    fi
WORKDIR /build/fteqw/engine
RUN make sv-rel

FROM ${EMSDK_IMAGE} AS web-builder
ARG FTEQW_REPO
ARG FTEQW_REF
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates git gzip make tar wget xz-utils \
  && rm -rf /var/lib/apt/lists/*
WORKDIR /build
RUN if [ -n "${FTEQW_REF}" ]; then \
      git clone --depth 1 --branch "${FTEQW_REF}" "${FTEQW_REPO}" fteqw; \
    else \
      git clone --depth 1 "${FTEQW_REPO}" fteqw; \
    fi
WORKDIR /build/fteqw/engine
RUN make web-rel

FROM debian:${DEBIAN_VERSION}-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends \
    bash ca-certificates nginx zlib1g \
  && rm -rf /var/lib/apt/lists/*

RUN useradd --create-home --shell /usr/sbin/nologin --uid 10001 fteqw

RUN mkdir -p /opt/fteqw/bin /opt/fteqw/web /gamedata /tmp/nginx \
  && chown -R fteqw:fteqw /opt/fteqw /gamedata /tmp/nginx

COPY --from=server-builder /build/fteqw/engine/release/fteqw-sv /opt/fteqw/bin/fteqw-sv

COPY --from=web-builder /build/fteqw/engine/release/ftewebgl.js /opt/fteqw/web/ftewebgl.js
COPY --from=web-builder /build/fteqw/engine/release/ftewebgl.wasm /opt/fteqw/web/ftewebgl.wasm
COPY --from=web-builder /build/fteqw/engine/release/ftewebgl.js.gz /opt/fteqw/web/ftewebgl.js.gz
COPY --from=web-builder /build/fteqw/engine/release/ftewebgl.wasm.gz /opt/fteqw/web/ftewebgl.wasm.gz

COPY web/index.html /opt/fteqw/web/index.html
COPY docker/nginx.conf /etc/nginx/nginx.conf
COPY docker/entrypoint.sh /opt/fteqw/entrypoint.sh

RUN chmod +x /opt/fteqw/bin/fteqw-sv /opt/fteqw/entrypoint.sh \
  && chown -R fteqw:fteqw /opt/fteqw

USER fteqw
WORKDIR /opt/fteqw

EXPOSE 26000/tcp

ENTRYPOINT ["/opt/fteqw/entrypoint.sh"]
CMD ["all"]
