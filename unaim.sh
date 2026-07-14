#!/usr/bin/env bash
# Remove one AIM 2.x MySQL instance. The shared versioned binary tree is kept.

set -Eeuo pipefail
umask 027

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
CONFIG_FILE="${SCRIPT_DIR}/etc/config"
VERSION=""
PORT=""
DATA_ROOT="/data/mysql"
LOG_ROOT="/var/log/mysql"
TMP_ROOT="/var/tmp/mysql"
BASE_ROOT="/opt/mysql"
ROOT_PASSWORD="${AIM_ROOT_PASSWORD:-}"
ASSUME_YES=0
DRY_RUN=0

usage() {
    cat <<EOF
Usage: sudo $0 -v VERSION -p PORT [options]

  -v, --version VERSION       Installed MySQL version (binary tree is retained)
  -p, --port PORT             Instance port to remove
  -c, --config FILE           Config used during installation
      --root-password PASS    Password used for graceful non-systemd shutdown
      --yes                   Do not ask for interactive confirmation
      --dry-run               Print paths without stopping or deleting anything
  -h, --help                  Show this help

The shared ${BASE_ROOT}/<version> directory and downloaded media are not removed.
EOF
}

die() { printf '[unaim] ERROR: %s\n' "$*" >&2; exit 1; }

prescan_config() {
    local previous=""
    for argument in "$@"; do
        if [[ "$previous" == config ]]; then CONFIG_FILE="$argument"; previous=""; continue; fi
        case "$argument" in
            -c|--config) previous="config" ;;
            --config=*) CONFIG_FILE="${argument#*=}" ;;
        esac
    done
}

load_config() {
    [[ -f "$CONFIG_FILE" ]] || return 0
    # shellcheck disable=SC1090
    source "$CONFIG_FILE"
}

need_arg() { [[ $# -ge 2 && -n "$2" ]] || die "option $1 requires a value"; }

parse_args() {
    while (( $# )); do
        case "$1" in
            -v|--version) need_arg "$@"; VERSION="$2"; shift 2 ;;
            --version=*) VERSION="${1#*=}"; shift ;;
            -p|--port) need_arg "$@"; PORT="$2"; shift 2 ;;
            --port=*) PORT="${1#*=}"; shift ;;
            -c|--config) need_arg "$@"; CONFIG_FILE="$2"; shift 2 ;;
            --config=*) CONFIG_FILE="${1#*=}"; shift ;;
            --root-password) need_arg "$@"; ROOT_PASSWORD="$2"; shift 2 ;;
            --yes) ASSUME_YES=1; shift ;;
            --dry-run) DRY_RUN=1; shift ;;
            -h|--help) usage; exit 0 ;;
            *) die "unknown option: $1" ;;
        esac
    done
}

safe_instance_path() {
    local path="$1" root="$2"
    [[ -n "$path" && -n "$root" && "$path" != / && "$root" != / && "$path" == "$root/"* ]] ||
        die "refusing unsafe path: $path (expected a child of $root)"
}

main() {
    prescan_config "$@"
    load_config
    ROOT_PASSWORD="${AIM_ROOT_PASSWORD:-$ROOT_PASSWORD}"
    parse_args "$@"

    [[ "$VERSION" =~ ^(5\.6|5\.7|8\.0|8\.4)\.[0-9]+$ ]] || die "an exact supported version is required"
    [[ "$PORT" =~ ^[0-9]+$ ]] || die "a numeric port is required"
    PORT=$((10#$PORT))
    (( PORT >= 1 && PORT <= 65535 )) || die "invalid port: $PORT"
    (( DRY_RUN )) || [[ $EUID -eq 0 ]] || die "run as root"

    local instance_root="${DATA_ROOT}/${PORT}"
    local cnf="${instance_root}/my.cnf"
    local socket="${instance_root}/mysql.sock"
    local logdir="${LOG_ROOT}/${PORT}"
    local tmpdir="${TMP_ROOT}/${PORT}"
    local basedir="${BASE_ROOT}/${VERSION}"
    local service="aim-mysql-${PORT}"
    local start_script="${BASE_ROOT}/start-${PORT}.sh"
    local stop_script="${BASE_ROOT}/stop-${PORT}.sh"

    safe_instance_path "$instance_root" "$DATA_ROOT"
    safe_instance_path "$logdir" "$LOG_ROOT"
    safe_instance_path "$tmpdir" "$TMP_ROOT"

    printf '[unaim] instance: MySQL %s on port %s\n' "$VERSION" "$PORT"
    printf '[unaim] remove:   %s %s %s %s %s\n' "$instance_root" "$logdir" "$tmpdir" "$start_script" "$stop_script"
    if (( DRY_RUN )); then printf '[unaim] dry-run completed\n'; return; fi
    if (( ! ASSUME_YES )); then
        [[ -t 0 ]] || die "confirmation requires a terminal; pass --yes after reviewing --dry-run"
        read -r -p "Type the port number (${PORT}) to confirm: " answer
        [[ "$answer" == "$PORT" ]] || die "confirmation did not match"
    fi

    if command -v systemctl >/dev/null 2>&1 && systemctl list-unit-files "${service}.service" --no-legend 2>/dev/null | grep "$service" >/dev/null; then
        systemctl disable --now "$service"
        rm -f -- "/etc/systemd/system/${service}.service"
        systemctl daemon-reload
    elif [[ -S "$socket" ]]; then
        [[ -n "$ROOT_PASSWORD" ]] || die "instance is running; set AIM_ROOT_PASSWORD for graceful shutdown"
        MYSQL_PWD="$ROOT_PASSWORD" "$basedir/bin/mysqladmin" --protocol=socket --socket="$socket" --user=root shutdown
    fi

    if command -v ss >/dev/null 2>&1 && ss -ltnH 2>/dev/null | awk '{print $4}' | grep -E "(^|:)$PORT$" >/dev/null; then
        die "port $PORT is still listening after shutdown; refusing deletion"
    fi
    if [[ -f "$cnf" ]] && [[ -f "${instance_root}/mysql.pid" ]]; then
        die "mysqld PID file still exists after shutdown; refusing deletion"
    fi
    rm -rf -- "$instance_root" "$logdir" "$tmpdir"
    rm -f -- "$start_script" "$stop_script"
    printf '[unaim] instance removed; shared binaries retained at %s\n' "$basedir"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
    main "$@"
fi
