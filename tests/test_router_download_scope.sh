#!/usr/bin/env bash

set -Eeuo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
cleanup() { rm -r -- "$TMP"; }
trap cleanup EXIT

function_source="$(awk '
/^download_verified\(\)/ { capture=1 }
capture {
    print
    opens += gsub(/\{/, "{")
    closes += gsub(/\}/, "}")
    if (opens > 0 && opens == closes) exit
}
' "$ROOT/router.sh")"
[[ -n "$function_source" ]] || { printf 'download_verified was not found\n' >&2; exit 1; }

printf 'cached-router-package\n' >"$TMP/mysql-router.tar.xz"
expected="test-checksum"

output="$(bash -Eeuo pipefail -c '
CACHE_ROOT="$1"
EXPECTED="$3"
md5_file() { printf "%s\n" "$EXPECTED"; }
eval "$2"
download_verified mysql-router mysql-router.tar.xz "$3"
' _ "$TMP" "$function_source" "$expected")"

[[ "$output" == "$TMP/mysql-router.tar.xz" ]]
printf 'router download local-variable scope: ok\n'
