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

# One configurable game per deployment (the "play Q1, Q2, or Q3" switch). GAMEDIR
# selects the engine game profile and a default starting map; SV_MAP overrides.
# EXTRA holds per-game server cvars.
# GAME_MODE is FTE's game-selection switch (-quake/-quake2/-quake3). It is what
# actually puts the engine into Q1/Q2/Q3 mode: protocol version, scheme, native
# gamecode, and the base gamedir. Passing only "-game baseq2" leaves the server
# in Quake1 mode, so a Quake2 client is rejected with "Game mismatch". The mode
# flag also sets the base gamedir (baseq2/baseq3), so a separate -game is not
# needed for the base games. Each mode's cfg pins that game's default sv_port,
# but the trunk requires a uniform 27500; the command-line "+set sv_port 27500"
# below runs (with +map) after the mode cfg, so it wins at socket-bind time.
EXTRA=""
case "$GAMEDIR" in
  id1)    FTE_GAME="quake";  DEF_MAP="start"; GAME_MODE="-quake" ;;
  baseq2) FTE_GAME="quake2"; DEF_MAP="base1"; GAME_MODE="-quake2"
          # FTE has no built-in Q2 gamecode; it loads a native baseq2/game.so.
          # Provision the bundled yamagi build if the gamedir lacks one. (-quake2
          # already implies com_gamedirnativecode 1; keep EXTRA for clarity.)
          if [ -d "$CLIENT_DIR/gamedata/baseq2" ] && [ ! -f "$CLIENT_DIR/gamedata/baseq2/game.so" ] && [ -f /app/q2game/game.so ]; then
            cp /app/q2game/game.so "$CLIENT_DIR/gamedata/baseq2/game.so" && echo "Provisioned baseq2/game.so (yamagi)"
          fi
          EXTRA="+set com_gamedirnativecode 1" ;;
  baseq3) FTE_GAME="quake3"; DEF_MAP="q3dm1"; GAME_MODE="-quake3" ;;
  *)      FTE_GAME="quake";  DEF_MAP="start"; GAME_MODE="-quake" ;;
esac
MAP="${SV_MAP:-$DEF_MAP}"

# Launch plan: a single fteqw-sv for the selected game on the trunk's UDP port
# (27500, distinct from Nexus's 26000 HTTP/3 port). Regenerated each start so the
# game/map track GAMEDIR.
cat > "$GAME_DIR/servers.ini" <<INI
# Generated from GAMEDIR=$GAMEDIR. Trunk delivers plain UDP to fteqw-sv.
fteqw-sv -dedicated $GAME_MODE -basedir $CLIENT_DIR/gamedata +set sv_port 27500 +set sv_public 0 +set hostname "NexQuake-FTE ($GAMEDIR)" $EXTRA +map $MAP
INI

# FTE package manifest for the client (game profile + the paks/pk3s present).
out="$CLIENT_DIR/index.fmf"
{
  echo "FTEMANIFEST 1"
  echo "GAME $FTE_GAME"
  echo "NAME \"NexQuake-FTE ($GAMEDIR)\""
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

# Tell the client which game to launch (read by index.html).
printf 'window.NQ_GAMEDIR=%s;\n' "\"$GAMEDIR\"" > "$CLIENT_DIR/gameconfig.js"

echo "Game: $GAMEDIR (FTE profile $FTE_GAME, map $MAP)"
echo "Generated manifest:"; cat "$out"

exec /app/bin/nexus
