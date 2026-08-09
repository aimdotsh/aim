#!/usr/bin/env bash

set -Eeuo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=../aim.sh
source "$ROOT/aim.sh"

tmp="$(mktemp -d)"
trap 'rm -rf -- "$tmp"' EXIT

OS_FAMILY=debian
BASEDIR="$tmp/mysql"
mkdir -p "$BASEDIR/bin" "$tmp/usr/lib/x86_64-linux-gnu"
touch "$BASEDIR/bin/mysqld" "$tmp/usr/lib/x86_64-linux-gnu/libaio.so.1t64"
repaired=0

missing_mysql_runtime_libraries() {
    (( repaired )) || printf 'libaio.so.1\n'
}

trusted_system_library_path() {
    [[ "$1" == "$tmp/usr/lib/x86_64-linux-gnu/libaio.so.1t64" ]]
}

ldconfig() {
    if [[ "${1:-}" == -p ]]; then
        printf 'libaio.so.1t64 (libc6,x86-64) => %s\n' "$tmp/usr/lib/x86_64-linux-gnu/libaio.so.1t64"
    fi
}

readlink() {
    [[ "$1" == -f && "$2" == -- ]]
    printf '%s\n' "$3"
}

ln() {
    [[ "$1" == -s && "$2" == libaio.so.1t64 && "$3" == "$tmp/usr/lib/x86_64-linux-gnu/libaio.so.1" ]]
    repaired=1
}

verify_mysql_runtime_libraries
(( repaired )) || {
    printf 'Ubuntu libaio1t64 compatibility link was not installed\n' >&2
    exit 1
}

OS_FAMILY=rhel
repaired=0
if (verify_mysql_runtime_libraries) >/dev/null 2>&1; then
    printf 'non-Debian host unexpectedly accepted the libaio1t64 repair\n' >&2
    exit 1
fi

printf 'Ubuntu 24.04 libaio1t64 compatibility repair: ok\n'
