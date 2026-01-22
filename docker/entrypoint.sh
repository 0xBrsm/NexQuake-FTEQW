#!/usr/bin/env bash
set -euo pipefail

MODE="${MODE:-}"
if [ $# -ge 1 ]; then
  case "$1" in
    server|client)
      MODE="$1"
      shift
      ;;
  esac
fi

if [ -z "$MODE" ]; then
  MODE="client"
fi

write_client_config() {
  : "${SERVER_HOST:=}"
  : "${SERVER_PORT:=27500}"
  : "${WS_SCHEME:=auto}"
  : "${CONNECT:=}"

  js_quote() {
    # Minimal JS string escaping for env-provided values.
    local s
    s="$(printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g')"
    printf '"%s"' "$s"
  }

  cat > /opt/fteqw/web/config.js <<EOF
// Generated at container start.
window.WEBQUAKE = {
  serverHost: $(js_quote "$SERVER_HOST"),
  serverPort: $(js_quote "${SERVER_PORT:-27500}"),
  wsScheme: $(js_quote "${WS_SCHEME:-auto}"),
  connectOverride: $(js_quote "$CONNECT"),
  manifestUrl: "/index.fmf"
};
EOF
}

validate_dir_name() {
  # allow simple gamedir names only (avoid path traversal)
  case "$1" in
    ""|*/*|*..*|*\\*|*:*|*"$"*|*"'"*|*"\""*|*" "*)
      return 1
      ;;
  esac
  return 0
}

write_manifest() {
  : "${BASEDIR:=/gamedata}"
  : "${GAMEDIR:=id1}"
  : "${BASEGAMES:=}"

  if ! validate_dir_name "$GAMEDIR"; then
    echo "Invalid GAMEDIR: $GAMEDIR" >&2
    exit 2
  fi

  out="/opt/fteqw/web/index.fmf"

  {
    echo "FTEMANIFEST 1"
    echo "GAME quake"
    echo "NAME \"fteqw-WebQuake\""

    oldifs=$IFS
    IFS=,
    for bg in $BASEGAMES; do
      bg=$(printf '%s' "$bg" | tr -d '[:space:]')
      [ -z "$bg" ] && continue
      if ! validate_dir_name "$bg"; then
        echo "Invalid basegame in BASEGAMES: $bg" >&2
        exit 2
      fi
      echo "BASEGAME $bg"
    done
    IFS=$oldifs

    echo "GAMEDIR $GAMEDIR"
  } > "$out"

  add_dir_packages() {
    dir="$1"
    pkgdir="${BASEDIR%/}/$dir"
    [ -d "$pkgdir" ] || return 0

    while IFS= read -r -d '' file; do
      base=$(basename "$file")
      logical="$dir/$base"
      url="${BASEDIR%/}/$logical"
      # Manifest v1 uses key/value properties; non-schemed mirrors must be prefixed with "mirror".
      # Omit crc entirely to avoid TOC hash enforcement.
      printf 'PACKAGE "%s" mirror "%s"\n' "$logical" "$url" >> "$out"
    done < <(
      find "$pkgdir" -maxdepth 1 -type f \( -iname '*.pak' -o -iname '*.pk3' -o -iname '*.pk4' \) -print0 | sort -z
    )
  }

  add_dir_packages "$GAMEDIR"

  oldifs=$IFS
  IFS=,
  for bg in $BASEGAMES; do
    bg=$(printf '%s' "$bg" | tr -d '[:space:]')
    [ -z "$bg" ] && continue
    add_dir_packages "$bg"
  done
  IFS=$oldifs

  # Convenience: if someone hits /index.html directly, emscripten will default to /index.html.fmf.
  cp "$out" /opt/fteqw/web/index.html.fmf
}

case "$MODE" in
  server)
    : "${BASEDIR:=/gamedata}"
    : "${GAMEDIR:=id1}"
    : "${SERVER_PORT:=27500}"
    : "${SERVER_PUBLIC:=0}"
    : "${SERVER_ARGS:=}"

    exec /opt/fteqw/bin/fteqw-sv \
      -basedir "$BASEDIR" \
      -game "$GAMEDIR" \
      +set sv_port_tcp "$SERVER_PORT" \
      +set sv_public "$SERVER_PUBLIC" \     
      $SERVER_ARGS \
      "$@"
    ;;
  client)
    write_client_config
    write_manifest
    exec nginx -g 'daemon off;'
    ;;
  *)
    echo "Usage: $0 [server|client] [args...]" >&2
    exit 2
    ;;
esac
