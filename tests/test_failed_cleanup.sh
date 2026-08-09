#!/usr/bin/env bash

set -Eeuo pipefail

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
# shellcheck disable=SC1091
source "${REPO_ROOT}/aim.sh"

sandbox="$(mktemp -d /tmp/aim-failed-cleanup-test.XXXXXX)"
cleanup() { find "$sandbox" -depth -delete 2>/dev/null || true; }
trap cleanup EXIT

VERSION="8.0.46"
PORT=8046
PORT_EXPLICIT=1
BASE_ROOT="${sandbox}/opt/mysql"
DATA_ROOT="${sandbox}/data/mysql"
LOG_ROOT="${sandbox}/log/mysql"
TMP_ROOT="${sandbox}/tmp/mysql"
ROOT_PASSWORD="test-only"
REINITIALIZE=0
DRY_RUN=0
ASSUME_YES=1

instance_root="${DATA_ROOT}/${PORT}"
other_root="${DATA_ROOT}/8047"
basedir="${BASE_ROOT}/${VERSION}"
mkdir -p "$instance_root/data" "$other_root/data" "$LOG_ROOT/$PORT" "$TMP_ROOT/$PORT" "$basedir/bin"
printf '[mysqld]\nbasedir = %s\n' "$basedir" >"$instance_root/my.cnf"
printf '[mysqld]\nbasedir = %s\n' "$basedir" >"$other_root/my.cnf"
touch "$basedir/bin/mysqld" "$BASE_ROOT/start-$PORT.sh" "$BASE_ROOT/stop-$PORT.sh"

require_root() { :; }
port_is_listening() { return 1; }
systemctl() { return 1; }

cleanup_failed_install >/dev/null
[[ ! -e "$instance_root" ]]
[[ -e "$other_root/my.cnf" ]]
[[ -e "$basedir/bin/mysqld" ]]

PORT=8047
PORT_EXPLICIT=1
cleanup_failed_install >/dev/null
[[ ! -e "$other_root" ]]
[[ ! -e "$basedir" ]]

dry_run_output="$(bash "${REPO_ROOT}/aim.sh" --cleanup-failed -v "$VERSION" -p 8048 \
    --base-root "$BASE_ROOT" --data-root "$DATA_ROOT" \
    --log-root "$LOG_ROOT" --tmp-root "$TMP_ROOT" --dry-run 2>&1)"
[[ "$dry_run_output" == *"no failed-install artifacts remain"* ]]

printf 'failed-install cleanup scope and shared binary protection: ok\n'
