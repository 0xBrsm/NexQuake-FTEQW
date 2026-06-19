#!/usr/bin/env bash
set -euo pipefail

# Front-door entrypoint: generate the FTE package manifest from whatever paks are
# mounted under CLIENT_DIR/gamedata, then hand off to Nexus (which serves the
# client + game data, runs the trunk /connect relay, and launches fteqw-sv from
# GAME_DIR/servers.ini).

: "${CLIENT_DIR:=/app/client}"
: "${GAME_DIR:=/app/game}"
: "${LOGS_DIR:=/app/logs}"
: "${CERT_DIR:=/app/cert}"
: "${GAMEDIR:=id1}"

mkdir -p "$LOGS_DIR" "$GAME_DIR" "$CLIENT_DIR/gamedata"

# WebTransport: when EXTERNAL_URL is set, Nexus serves HTTPS + WebTransport. Mint
# a self-signed cert (ECDSA P-256, 13-day validity — the serverCertificateHashes
# constraints) if one isn't supplied, and publish its SHA-256 to the client so the
# WT session can pin it (no public CA needed for local play). Plain-HTTP mode
# writes an empty hash list so the WebSocket path is used.
if [ -n "${EXTERNAL_URL:-}" ]; then
  mkdir -p "$CERT_DIR"
  if [ ! -s "$CERT_DIR/cert.pem" ] || [ ! -s "$CERT_DIR/key.pem" ]; then
    cn="${WT_CERT_HOST:-localhost}"
    echo "Minting self-signed WebTransport cert for $cn (ECDSA P-256, 13-day)"
    openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 \
      -keyout "$CERT_DIR/key.pem" -out "$CERT_DIR/cert.pem" \
      -days 13 -nodes -subj "/CN=$cn" \
      -addext "subjectAltName=DNS:$cn,DNS:localhost,IP:127.0.0.1,IP:::1" >/dev/null 2>&1
  fi
  hash="$(openssl x509 -in "$CERT_DIR/cert.pem" -outform der | openssl dgst -sha256 -binary | base64 -w0)"
  printf 'window.NQWT_HASHES=["%s"];\n' "$hash" > "$CLIENT_DIR/wtconfig.js"
  echo "WebTransport enabled; cert SHA-256 (base64): $hash"
else
  printf 'window.NQWT_HASHES=[];\n' > "$CLIENT_DIR/wtconfig.js"
fi

out="$CLIENT_DIR/index.fmf"
{
  echo "FTEMANIFEST 1"
  echo "GAME quake"
  echo "NAME \"NexQuake-FTE\""
  echo "GAMEDIR $GAMEDIR"
} > "$out"

pkgdir="$CLIENT_DIR/gamedata/$GAMEDIR"
if [ -d "$pkgdir" ]; then
  for f in "$pkgdir"/*.pak "$pkgdir"/*.pk3 "$pkgdir"/*.pk4; do
    [ -e "$f" ] || continue
    base="$(basename "$f")"
    printf 'PACKAGE "%s/%s" mirror "/gamedata/%s/%s"\n' "$GAMEDIR" "$base" "$GAMEDIR" "$base" >> "$out"
  done
fi
# Emscripten falls back to <page>.fmf when -manifest is absent; keep them in sync.
cp "$out" "$CLIENT_DIR/index.html.fmf"

echo "Generated manifest:"; cat "$out"

exec /app/bin/nexus
