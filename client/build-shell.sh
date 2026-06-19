#!/usr/bin/env bash
#
# Assemble the NexQuake shell page for the FTE client: concatenate the shell JS
# modules (in load order) into nq-shell.js, fill shell.html's {{{ SCRIPT }}} and
# __NEXQUAKE_*__ placeholders, and copy the static assets.
#
# Usage: build-shell.sh <shell-src-dir> <out-dir>
set -euo pipefail

SRC="${1:?shell src dir}"
OUT="${2:?out dir}"
VERSION="${NQ_VERSION:-fte-dev}"
mkdir -p "$OUT"

# Load order: core globals first, then the FTE bootstrap (Module config + engine
# load), then touch HUD, overlay/UI, and the error handler last.
order=(00-core.js 10-fte-bootstrap.js 20-touch-glyphs.js 21-touch-controls.js \
       50-ui.js 51-ui-cd.js 52-ui-vfs.js 53-ui-upload.js 54-text-entry.js \
       55-rcon.js 59-ui-events.js 60-onerror.js)

: > "$OUT/nq-shell.js"
for f in "${order[@]}"; do
  if [ -f "$SRC/$f" ]; then
    printf '\n//======== %s ========\n' "$f" >> "$OUT/nq-shell.js"
    cat "$SRC/$f" >> "$OUT/nq-shell.js"
  else
    echo "warning: shell module missing: $f" >&2
  fi
done

# {{{ SCRIPT }}} -> runtime config (entrypoint-generated) + the assembled bundle.
SCRIPT_TAGS='<script src="/wtconfig.js"></script>
    <script src="/gameconfig.js"></script>
    <script src="nq-shell.js"></script>'

sed -e "s|__NEXQUAKE_VERSION__|$VERSION|g" \
    -e "s|__NEXQUAKE_GAMENAME__||g" \
    -e "s|__NEXQUAKE_REMOTE_ROOT_BASENAME__||g" \
    "$SRC/shell.html" \
  | awk -v tags="$SCRIPT_TAGS" 'index($0, "{{{ SCRIPT }}}") { print tags; next } { print }' \
  > "$OUT/index.html"

for a in shell-nq.css shell-loader.css shell-ui.css shell-touch.css \
         favicon.svg pwa-icon.svg manifest.webmanifest; do
  [ -f "$SRC/$a" ] && cp "$SRC/$a" "$OUT/"
done

echo "Assembled shell -> $OUT/index.html + nq-shell.js (version $VERSION)"
