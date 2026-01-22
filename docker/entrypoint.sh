#!/usr/bin/env bash
set -euo pipefail

MODE="${MODE:-}"
if [ $# -ge 1 ]; then
  case "$1" in
    all|server|client)
      MODE="$1"
      shift
      ;;
  esac
fi

if [ -z "$MODE" ]; then
  MODE="all"
fi

write_client_config() {
  : "${SERVER_HOST:=}"          # optional explicit host override for UI/legacy uses
  : "${SERVER_PORT:=26000}"     # public port (nginx) seen by browsers
  : "${WS_SCHEME:=auto}"
  : "${CONNECT:=}"
  : "${SERVER_LIST_URL:=}"      # preferred name

  js_quote() {
    # Minimal JS string escaping for env-provided values.
    local s
    s="$(printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g')"
    printf '"%s"' "$s"
  }

  local server_list_url
  server_list_url="${SERVER_LIST_URL}"

  cat > /opt/fteqw/web/config.js <<EOF
// Generated at container start.
window.WEBQUAKE = {
  serverHost: $(js_quote "$SERVER_HOST"),
  serverPort: $(js_quote "${SERVER_PORT:-26000}"),
  wsScheme: $(js_quote "${WS_SCHEME:-auto}"),
  connectOverride: $(js_quote "$CONNECT"),
  serverListUrl: $(js_quote "$server_list_url"),
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
  all)
    : "${BASEDIR:=/gamedata}"
    : "${GAMEDIR:=id1}"
    : "${NQ_PORT:=27500}"        # internal server port (not published)
    : "${SERVER_PUBLIC:=0}"
    : "${SERVER_ARGS:=}"

    write_client_config
    write_manifest

    /opt/fteqw/bin/fteqw-sv \
      -basedir "$BASEDIR" \
      -game "$GAMEDIR" \
      +set sv_serverip 127.0.0.1 \
      +set net_enable_websockets 1 \
      +set sv_port_tcp "$NQ_PORT" \
      +set sv_public "$SERVER_PUBLIC" \
      $SERVER_ARGS \
      "$@" &
    server_pid=$!

    nginx -g 'daemon off;' &
    nginx_pid=$!

    term() {
      kill "$nginx_pid" "$server_pid" 2>/dev/null || true
    }
    trap 'term; exit 0' INT TERM

    wait "$nginx_pid"
    term
    wait "$server_pid" || true
    ;;
  server)
    : "${BASEDIR:=/gamedata}"
    : "${GAMEDIR:=id1}"
    : "${NQ_PORT:=27500}"
    : "${SERVER_PUBLIC:=0}"
    : "${SERVER_ARGS:=}"

    exec /opt/fteqw/bin/fteqw-sv \
      -basedir "$BASEDIR" \
      -game "$GAMEDIR" \
      +set net_enable_websockets 1 \
      +set sv_port_tcp "$NQ_PORT" \
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
    echo "Usage: $0 [all|server|client] [args...]" >&2
    exit 2
    ;;
esac
