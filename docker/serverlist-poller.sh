#!/usr/bin/env bash
set -euo pipefail

# Generates `/opt/fteqw/web/servers.info` for nginx SSI to include in `/servers.txt`.
# Uses a UDP getinfo query against the local server to obtain current serverinfo.

: "${NQ_PORT:=27500}"
: "${SERVERLIST_INTERVAL:=5}"
: "${SERVERLIST_HOSTNAME:=NetQuake}"
: "${SERVERLIST_MAXCLIENTS_DEFAULT:=16}"

out_dir="/opt/fteqw/web"
out_file="${out_dir}/servers.info"
tmp_file="${out_file}.tmp"

get_info_value() {
	# $1 = infostring, $2 = key
	# infostring format: \key\val\key\val...
	awk -v key="$2" -F'\\' '{
		for (i = 2; i < NF; i += 2) {
			if ($i == key) {
				print $(i + 1);
				exit;
			}
		}
	}' <<<"$1"
}

normalize_infostring() {
	local info="$1"

	# Ensure we always have a hostname (prefer runtime override, otherwise whatever server returned).
	if [ -n "${SERVERLIST_HOSTNAME}" ]; then
		# Remove existing hostname if present, then append our hostname.
		info="$(awk -F'\\' -v host="$SERVERLIST_HOSTNAME" 'BEGIN{out="";seen=0}
			{
				for(i=2;i<NF;i+=2){
					k=$i; v=$(i+1);
					if(k=="hostname"){seen=1; next}
					out=out "\\" k "\\" v
				}
				out=out "\\hostname\\" host
				print out
			}' <<<"$info")"
	fi

	# Translate sv_maxclients -> maxclients for the web server browser.
	local maxclients
	maxclients="$(get_info_value "$info" "maxclients")"
	if [ -z "$maxclients" ]; then
		local sv_maxclients
		sv_maxclients="$(get_info_value "$info" "sv_maxclients")"
		if [ -n "$sv_maxclients" ]; then
			info="${info}\\maxclients\\${sv_maxclients}"
		elif [ -n "${SERVERLIST_MAXCLIENTS_DEFAULT}" ]; then
			info="${info}\\maxclients\\${SERVERLIST_MAXCLIENTS_DEFAULT}"
		fi
	fi

	# Ensure trailing backslash (common convention; some parsers are picky).
	case "$info" in
		*\\) ;;
		*) info="${info}\\" ;;
	esac

	printf '%s' "$info"
}

mkdir -p "$out_dir"

while true; do
	# Quake3/dpmaster-style query supported by FTEQW: response is `infoResponse\n\\key\\val...`
	resp="$(
		printf '\xFF\xFF\xFF\xFFgetinfo poll\n' | nc -u -n -w 1 127.0.0.1 "$NQ_PORT" 2>/dev/null || true
	)"

	infostring="$(
		printf '%s' "$resp" \
			| tr -d '\r' \
			| awk 'substr($0,1,1)=="\\" {print; exit}'
	)"

	if [ -n "$infostring" ]; then
		infostring="$(normalize_infostring "$infostring")"
		{
			printf '%s\n' "$infostring"
		} >"$tmp_file"
		mv -f "$tmp_file" "$out_file"
	fi

	sleep "$SERVERLIST_INTERVAL"
done

