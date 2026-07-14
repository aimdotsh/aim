#!/usr/bin/env bash

set -Eeuo pipefail

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
# shellcheck disable=SC1091
source "${REPO_ROOT}/aim.sh"

UNINSTALL=1
PORT_EXPLICIT=1
REINITIALIZE=1
ASSUME_YES=1
reset_cli_action_flags
[[ "$UNINSTALL" == 0 && "$PORT_EXPLICIT" == 0 && "$REINITIALIZE" == 0 && "$ASSUME_YES" == 0 ]]

if (PORT_EXPLICIT=0; VERSION="8.0.46"; validate_uninstall_inputs) >/dev/null 2>&1; then
    printf 'uninstall accepted an implicit default port\n' >&2
    exit 1
fi

sandbox="$(mktemp -d /tmp/aim-uninstall-test.XXXXXX)"
cleanup() { find "$sandbox" -depth -delete 2>/dev/null || true; }
trap cleanup EXIT

ln -s / "${sandbox}/escape-root"
if (assert_safe_child_path "${sandbox}/escape-root/8046" "${sandbox}/escape-root") >/dev/null 2>&1; then
    printf 'destructive path validation followed a root symlink escape\n' >&2
    exit 1
fi

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
logdir="${LOG_ROOT}/${PORT}"
tmpdir="${TMP_ROOT}/${PORT}"
basedir="${BASE_ROOT}/${VERSION}"
start_script="${BASE_ROOT}/start-${PORT}.sh"
stop_script="${BASE_ROOT}/stop-${PORT}.sh"

mkdir -p "$instance_root/data" "$logdir" "$tmpdir" "$basedir/bin"
touch "$instance_root/my.cnf" "$start_script" "$stop_script" "$basedir/bin/mysqld"

dry_run_output="$(bash "${REPO_ROOT}/aim.sh" --uninstall -v "$VERSION" -p "$PORT" \
    --base-root "$BASE_ROOT" --data-root "$DATA_ROOT" \
    --log-root "$LOG_ROOT" --tmp-root "$TMP_ROOT" --dry-run 2>&1)"
[[ "$dry_run_output" == *"uninstall preview completed"* ]]
[[ -e "$instance_root/my.cnf" ]]

require_root() { :; }
port_is_listening() { return 1; }
systemctl() { return 1; }

uninstall_instance >/dev/null

[[ ! -e "$instance_root" ]]
[[ ! -e "$logdir" ]]
[[ ! -e "$tmpdir" ]]
[[ ! -e "$start_script" ]]
[[ ! -e "$stop_script" ]]
[[ -e "$basedir/bin/mysqld" ]]

printf 'uninstall scope and shared binary retention: ok\n'
