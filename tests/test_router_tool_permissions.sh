#!/usr/bin/env bash

set -Eeuo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
cleanup() { rm -r -- "$TMP"; }
trap cleanup EXIT

function_source="$(awk '
/^normalize_tool_permissions\(\)/ { capture=1 }
capture {
    print
    if (/^extract_tool\(\)/) in_extract=1
    opens += gsub(/\{/, "{")
    closes += gsub(/\}/, "}")
    if (in_extract && opens > 0 && opens == closes) exit
}
' "$ROOT/router.sh")"
[[ -n "$function_source" ]] || { printf 'Router tool permission functions were not found\n' >&2; exit 1; }

mkdir -p "$TMP/bin" "$TMP/tool/bin"
cat >"$TMP/bin/chown" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$AIM_ROUTER_CHOWN_LOG"
EOF
chmod 0755 "$TMP/bin/chown"
printf '#!/usr/bin/env bash\n' >"$TMP/tool/bin/mysqlrouter"
chmod 0750 "$TMP/tool" "$TMP/tool/bin"
chmod 0700 "$TMP/tool/bin/mysqlrouter"

PATH="$TMP/bin:$PATH" AIM_ROUTER_CHOWN_LOG="$TMP/chown.log" bash -Eeuo pipefail -c '
die() { printf "%s\n" "$*" >&2; exit 1; }
eval "$1"
extract_tool /unused/archive "$2" bin/mysqlrouter
' _ "$function_source" "$TMP/tool"

mode() {
    if stat -c '%a' "$1" >/dev/null 2>&1; then
        stat -c '%a' "$1"
    else
        stat -f '%Lp' "$1"
    fi
}

[[ "$(mode "$TMP/tool")" == 755 ]]
[[ "$(mode "$TMP/tool/bin")" == 755 ]]
[[ "$(mode "$TMP/tool/bin/mysqlrouter")" == 755 ]]
grep -q -- '-R root:root' "$TMP/chown.log"

printf 'router tool permissions: ok\n'
