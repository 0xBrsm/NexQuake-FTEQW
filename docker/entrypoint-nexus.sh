#!/usr/bin/env bash
set -euo pipefail

# Front-door entrypoint: generate the FTE package manifest from whatever paks are
# mounted under CLIENT_DIR/gamedata, then hand off to Nexus (which serves the
# client + game data, runs the trunk /connect relay, and launches fteqw-sv from
# GAME_DIR/servers.ini).

: "${CLIENT_DIR:=/app/client}"
: "${GAME_DIR:=/app/game}"
: "${LOGS_DIR:=/app/logs}"
: "${GAMEDIR:=id1}"

mkdir -p "$LOGS_DIR" "$GAME_DIR" "$CLIENT_DIR/gamedata"

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
