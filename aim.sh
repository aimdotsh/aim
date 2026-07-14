#!/usr/bin/env bash
# AIM - install isolated MySQL Community Server instances from generic binaries.

set -Eeuo pipefail
umask 027

readonly AIM_VERSION="2.2.0"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"

VERSION=""
PORT="3306"
PORT_EXPLICIT=0
ROLE="standalone"
CONFIG_FILE="${SCRIPT_DIR}/aim.conf"
BASE_ROOT="/opt/mysql"
DATA_ROOT="/data/mysql"
LOG_ROOT="/var/log/mysql"
TMP_ROOT="/var/tmp/mysql"
MYSQL_USER="mysql"
MYSQL_GROUP="mysql"
BIND_ADDRESS="0.0.0.0"
SERVER_ID=""
ROOT_PASSWORD=""
REPL_USER="aim_repl"
REPL_PASSWORD=""
REPLICA_HOST="%"
SOURCE_HOST=""
SOURCE_PORT="3306"
SOURCE_USER="aim_repl"
SOURCE_PASSWORD=""
GTID=1
DOWNLOAD=1
INSTALL_DEPS=1
DRY_RUN=0
REINITIALIZE=0
ASSUME_YES=0
UNINSTALL=0
MGR_LOCAL_ADDRESS=""
MGR_PORT="33061"
MGR_SEEDS=""
MGR_GROUP_NAME=""
MGR_ALLOWLIST=""
MGR_BOOTSTRAP=0
MGR_RECOVERY_USER="aim_mgr"
MGR_RECOVERY_PASSWORD=""
ARCHIVE=""
DOWNLOAD_URL=""
OS_ID=""
OS_FAMILY=""
ARCH=""
LIBC_VERSION=""
SERIES=""
BASEDIR=""
DATADIR=""
LOGDIR=""
TMPDIR_INSTANCE=""
CNF_FILE=""
SOCKET=""
PID_FILE=""
SERVICE_NAME=""
MYSQL=""
MYSQLADMIN=""

usage() {
    cat <<EOF
AIM ${AIM_VERSION} - MySQL 5.6/5.7/8.0/8.4 lifecycle manager

Usage:
  sudo $0 -v VERSION [-p PORT] [options]
  sudo $0 --uninstall -v VERSION -p PORT [--dry-run|--yes]

Required:
  -v, --version VERSION       Exact MySQL version, e.g. 5.7.44, 8.0.42, 8.4.5

Instance:
  -p, --port PORT             TCP port (default: 3306)
      --role ROLE             standalone, source/master, replica/slave, or mgr
  -g, --gtid                 Enable GTID (default)
      --no-gtid              Disable GTID (replica mode requires GTID)
      --server-id ID          Replication server_id (derived automatically)
      --bind-address ADDRESS  Listen address (default: 0.0.0.0)
      --root-password PASS    Root password (random when omitted)
      --uninstall             Remove one AIM instance; -v and -p are required

Replication:
      --replica-host HOST     Account host used on a source (default: %)
      --repl-user USER        Account created on a source
      --repl-password PASS    Account password (random when omitted)
      --source-host HOST      Source host; required for role=replica
      --source-port PORT      Source port (default: 3306)
      --source-user USER      Replication account on source
      --source-password PASS  Replication password on source

MGR (MySQL 8.0.23+):
      --mgr-local-address IP  This member's address advertised to the group
      --mgr-port PORT         XCom port (default: 33061; not the SQL port)
      --mgr-seeds LIST        Comma-separated XCom addresses for all members
      --mgr-group-name UUID   UUID shared by every member in this group
      --mgr-allowlist LIST    Comma-separated trusted IP/CIDR entries
      --mgr-bootstrap         Bootstrap the group on this member only
      --mgr-recovery-user USER
                              Recovery account (default: aim_mgr)
      --mgr-recovery-password PASS
                              Same recovery password on every member

Paths and package:
  -c, --config FILE           Optional shell config (default: aim.conf)
      --base-root DIR         Software root (default: /opt/mysql)
      --data-root DIR         Data root (default: /data/mysql)
      --log-root DIR          Log root (default: /var/log/mysql)
      --tmp-root DIR          Temporary root (default: /var/tmp/mysql)
      --archive FILE          Use an existing official generic archive
      --download-url URL      Download archive from an explicit URL
      --no-download           Do not download when media/ has no archive
      --skip-deps             Do not install OS packages
      --dry-run               Validate and print actions without changing host
      --reinitialize          Delete and recreate this port's instance data
      --yes                   Confirm destructive reinitialize/uninstall non-interactively
  -h, --help                  Show this help

Examples:
  sudo $0 -v 8.4.5 -p 3306 --role standalone
  sudo $0 -v 8.0.42 -p 3306 --role source --replica-host 10.0.0.12
  sudo $0 -v 8.0.42 -p 3306 --role replica --source-host 10.0.0.11 \\
      --source-password 'secret'
  sudo $0 -v 8.0.46 -p 8046 --role mgr --server-id 101 --mgr-local-address 10.0.0.11 \\
      --mgr-seeds '10.0.0.11:33061,10.0.0.12:33061,10.0.0.13:33061' \\
      --mgr-group-name 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee' --mgr-allowlist '10.0.0.0/24' \\
      --mgr-bootstrap --mgr-recovery-password 'same-secret-on-all-members'
  sudo $0 --uninstall -v 8.0.46 -p 8046 --dry-run
EOF
}

log() { printf '[aim] %s\n' "$*"; }
warn() { printf '[aim] WARNING: %s\n' "$*" >&2; }
die() { printf '[aim] ERROR: %s\n' "$*" >&2; exit 1; }
quote_cmd() { printf ' %q' "$@"; printf '\n'; }
run() {
    if (( DRY_RUN )); then
        printf '[dry-run]'; quote_cmd "$@"
    else
        "$@"
    fi
}

on_error() {
    local rc=$?
    printf '[aim] ERROR: command failed at line %s (exit %s)\n' "${BASH_LINENO[0]}" "$rc" >&2
    exit "$rc"
}
trap on_error ERR

version_ge() {
    [[ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -n1)" == "$2" ]]
}

sql_escape() {
    local value="$1"
    value="${value//\\/\\\\}"
    value="${value//\'/\'\'}"
    printf '%s' "$value"
}

random_password() {
    local value
    if command -v openssl >/dev/null 2>&1; then
        value="$(openssl rand -hex 16)"
    else
        value="$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"
    fi
    printf '%s' "$value"
}

# Read -c early so the requested trusted config is loaded before normal parsing.
prescan_args() {
    local index=1 arg
    while (( index <= $# )); do
        arg="${!index}"
        case "$arg" in
            -c|--config) ((index++)); CONFIG_FILE="${!index:-}" ;;
            --config=*) CONFIG_FILE="${arg#*=}" ;;
        esac
        ((index++))
    done
}

load_config() {
    [[ -f "$CONFIG_FILE" ]] || return 0
    # The config is a trusted local shell file. Copy config.sample to aim.conf.
    # shellcheck disable=SC1090
    source "$CONFIG_FILE"
}

reset_cli_action_flags() {
    # Destructive actions and confirmations must come from this invocation,
    # never from a sourced configuration file.
    PORT_EXPLICIT=0
    DRY_RUN=0
    REINITIALIZE=0
    ASSUME_YES=0
    UNINSTALL=0
}

apply_secret_environment() {
    [[ -z "${AIM_ROOT_PASSWORD:-}" ]] || ROOT_PASSWORD="$AIM_ROOT_PASSWORD"
    [[ -z "${AIM_REPL_PASSWORD:-}" ]] || REPL_PASSWORD="$AIM_REPL_PASSWORD"
    [[ -z "${AIM_SOURCE_PASSWORD:-}" ]] || SOURCE_PASSWORD="$AIM_SOURCE_PASSWORD"
    [[ -z "${AIM_MGR_RECOVERY_PASSWORD:-}" ]] || MGR_RECOVERY_PASSWORD="$AIM_MGR_RECOVERY_PASSWORD"
}

need_arg() { [[ $# -ge 2 && -n "$2" ]] || die "option $1 requires a value"; }

parse_args() {
    while (( $# )); do
        case "$1" in
            -v|--version) need_arg "$@"; VERSION="$2"; shift 2 ;;
            --version=*) VERSION="${1#*=}"; shift ;;
            -p|--port) need_arg "$@"; PORT="$2"; PORT_EXPLICIT=1; shift 2 ;;
            --port=*) PORT="${1#*=}"; PORT_EXPLICIT=1; shift ;;
            -c|--config) need_arg "$@"; CONFIG_FILE="$2"; shift 2 ;;
            --config=*) CONFIG_FILE="${1#*=}"; shift ;;
            --role) need_arg "$@"; ROLE="$2"; shift 2 ;;
            --role=*) ROLE="${1#*=}"; shift ;;
            -g|--gtid) GTID=1; shift ;;
            --no-gtid) GTID=0; shift ;;
            --server-id) need_arg "$@"; SERVER_ID="$2"; shift 2 ;;
            --bind-address) need_arg "$@"; BIND_ADDRESS="$2"; shift 2 ;;
            --root-password) need_arg "$@"; ROOT_PASSWORD="$2"; shift 2 ;;
            --uninstall) UNINSTALL=1; shift ;;
            --replica-host) need_arg "$@"; REPLICA_HOST="$2"; shift 2 ;;
            --repl-user) need_arg "$@"; REPL_USER="$2"; shift 2 ;;
            --repl-password) need_arg "$@"; REPL_PASSWORD="$2"; shift 2 ;;
            --source-host) need_arg "$@"; SOURCE_HOST="$2"; shift 2 ;;
            --source-port) need_arg "$@"; SOURCE_PORT="$2"; shift 2 ;;
            --source-user) need_arg "$@"; SOURCE_USER="$2"; shift 2 ;;
            --source-password) need_arg "$@"; SOURCE_PASSWORD="$2"; shift 2 ;;
            --mgr-local-address|--mgr-local-ip) need_arg "$@"; MGR_LOCAL_ADDRESS="$2"; shift 2 ;;
            --mgr-port) need_arg "$@"; MGR_PORT="$2"; shift 2 ;;
            --mgr-seeds) need_arg "$@"; MGR_SEEDS="$2"; shift 2 ;;
            --mgr-group-name) need_arg "$@"; MGR_GROUP_NAME="$2"; shift 2 ;;
            --mgr-allowlist) need_arg "$@"; MGR_ALLOWLIST="$2"; shift 2 ;;
            --mgr-bootstrap) MGR_BOOTSTRAP=1; shift ;;
            --mgr-recovery-user) need_arg "$@"; MGR_RECOVERY_USER="$2"; shift 2 ;;
            --mgr-recovery-password) need_arg "$@"; MGR_RECOVERY_PASSWORD="$2"; shift 2 ;;
            --base-root) need_arg "$@"; BASE_ROOT="$2"; shift 2 ;;
            --data-root) need_arg "$@"; DATA_ROOT="$2"; shift 2 ;;
            --log-root) need_arg "$@"; LOG_ROOT="$2"; shift 2 ;;
            --tmp-root) need_arg "$@"; TMP_ROOT="$2"; shift 2 ;;
            --archive) need_arg "$@"; ARCHIVE="$2"; shift 2 ;;
            --download-url) need_arg "$@"; DOWNLOAD_URL="$2"; shift 2 ;;
            --no-download) DOWNLOAD=0; shift ;;
            --skip-deps) INSTALL_DEPS=0; shift ;;
            --dry-run) DRY_RUN=1; shift ;;
            --reinitialize|--reinit) REINITIALIZE=1; shift ;;
            --yes) ASSUME_YES=1; shift ;;
            -h|--help) usage; exit 0 ;;
            --) shift; break ;;
            *) die "unknown option: $1 (use --help)" ;;
        esac
    done
}

validate_inputs() {
    local root_path seed seed_port
    local -a seed_entries
    [[ "$VERSION" =~ ^(5\.6|5\.7|8\.0|8\.4)\.[0-9]+$ ]] ||
        die "unsupported version '$VERSION'; expected an exact 5.6.x, 5.7.x, 8.0.x, or 8.4.x version"
    SERIES="${BASH_REMATCH[1]}"
    [[ "$PORT" =~ ^[0-9]+$ ]] || die "invalid port: $PORT"
    [[ "$SOURCE_PORT" =~ ^[0-9]+$ ]] || die "invalid source port: $SOURCE_PORT"
    [[ "$MGR_PORT" =~ ^[0-9]+$ ]] || die "invalid MGR port: $MGR_PORT"
    PORT=$((10#$PORT))
    SOURCE_PORT=$((10#$SOURCE_PORT))
    MGR_PORT=$((10#$MGR_PORT))
    (( PORT >= 1 && PORT <= 65535 )) || die "invalid port: $PORT"
    (( SOURCE_PORT >= 1 && SOURCE_PORT <= 65535 )) || die "invalid source port: $SOURCE_PORT"
    (( MGR_PORT >= 1 && MGR_PORT <= 65535 )) || die "invalid MGR port: $MGR_PORT"
    [[ "$ROLE" == master ]] && ROLE="source"
    [[ "$ROLE" == slave ]] && ROLE="replica"
    [[ "$MGR_BOOTSTRAP" =~ ^[01]$ ]] || die "MGR_BOOTSTRAP must be 0 or 1"
    [[ "$ROLE" =~ ^(standalone|source|replica|mgr)$ ]] ||
        die "role must be standalone, source/master, replica/slave, or mgr"
    if (( MGR_BOOTSTRAP )) && [[ "$ROLE" != mgr ]]; then
        die "--mgr-bootstrap is valid only with --role mgr"
    fi
    if [[ "$ROLE" == replica ]]; then
        [[ -n "$SOURCE_HOST" ]] || die "--source-host is required for role=replica"
        [[ -n "$SOURCE_PASSWORD" ]] || die "--source-password is required for role=replica"
        (( GTID )) || die "role=replica currently requires GTID for a consistent, position-free setup"
    fi
    if [[ "$ROLE" == mgr ]]; then
        if [[ "$SERIES" != 8.0 ]] || ! version_ge "$VERSION" 8.0.23; then
            die "role=mgr requires MySQL 8.0.23 or newer in the 8.0 series"
        fi
        (( GTID )) || die "role=mgr requires GTID"
        [[ -n "$SERVER_ID" ]] || die "--server-id is required for role=mgr and must be unique in the group"
        [[ "$MGR_LOCAL_ADDRESS" =~ ^[A-Za-z0-9._-]+$ ]] || die "invalid or missing --mgr-local-address"
        [[ "$MGR_SEEDS" =~ ^[A-Za-z0-9._-]+:[0-9]+(,[A-Za-z0-9._-]+:[0-9]+)*$ ]] ||
            die "invalid or missing --mgr-seeds; expected host:port,host:port"
        IFS=',' read -r -a seed_entries <<<"$MGR_SEEDS"
        for seed in "${seed_entries[@]}"; do
            seed_port="${seed##*:}"
            seed_port=$((10#$seed_port))
            (( seed_port >= 1 && seed_port <= 65535 )) || die "invalid MGR seed port in: $seed"
        done
        [[ ",$MGR_SEEDS," == *",${MGR_LOCAL_ADDRESS}:${MGR_PORT},"* ]] ||
            die "--mgr-seeds must include this member's ${MGR_LOCAL_ADDRESS}:${MGR_PORT} address"
        [[ "$MGR_GROUP_NAME" =~ ^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$ ]] ||
            die "invalid or missing --mgr-group-name UUID"
        [[ "$MGR_ALLOWLIST" =~ ^[A-Za-z0-9._:/,-]+$ ]] || die "invalid or missing --mgr-allowlist"
        [[ "$MGR_RECOVERY_USER" =~ ^[A-Za-z0-9_]+$ ]] || die "invalid MGR recovery user"
        [[ -n "$MGR_RECOVERY_PASSWORD" ]] ||
            die "--mgr-recovery-password or AIM_MGR_RECOVERY_PASSWORD is required for role=mgr"
        (( MGR_PORT != PORT )) || die "MGR XCom port must be different from the MySQL SQL port"
    fi
    [[ "$REPL_USER" =~ ^[A-Za-z0-9_]+$ ]] || die "invalid replication user"
    [[ "$SOURCE_USER" =~ ^[A-Za-z0-9_]+$ ]] || die "invalid source user"
    [[ -z "$SERVER_ID" || "$SERVER_ID" =~ ^[0-9]+$ ]] || die "server-id must be numeric"
    if [[ -n "$SERVER_ID" ]]; then
        SERVER_ID=$((10#$SERVER_ID))
        (( SERVER_ID >= 1 && SERVER_ID <= 4294967295 )) || die "server-id must be between 1 and 4294967295"
    fi
    for root_path in "$BASE_ROOT" "$DATA_ROOT" "$LOG_ROOT" "$TMP_ROOT"; do
        [[ "$root_path" == /* && "$root_path" != / ]] || die "installation roots must be absolute non-root paths: $root_path"
        [[ "/$root_path/" != *"/../"* && "/$root_path/" != *"/./"* ]] ||
            die "installation roots must not contain . or .. path segments: $root_path"
    done

    BASEDIR="${BASEDIR:-${BASE_ROOT}/${VERSION}}"
    DATADIR="${DATADIR:-${DATA_ROOT}/${PORT}/data}"
    LOGDIR="${LOGDIR:-${LOG_ROOT}/${PORT}}"
    TMPDIR_INSTANCE="${TMPDIR_INSTANCE:-${TMP_ROOT}/${PORT}}"
    CNF_FILE="${CNF_FILE:-${DATA_ROOT}/${PORT}/my.cnf}"
    SOCKET="${SOCKET:-${DATA_ROOT}/${PORT}/mysql.sock}"
    PID_FILE="${PID_FILE:-${DATA_ROOT}/${PORT}/mysql.pid}"
    SERVICE_NAME="aim-mysql-${PORT}"
    MYSQL="${BASEDIR}/bin/mysql"
    MYSQLADMIN="${BASEDIR}/bin/mysqladmin"
    [[ -n "$SERVER_ID" ]] || SERVER_ID="$(( (PORT * 1009 + 17) % 4294967294 + 1 ))"
}

detect_platform() {
    local os_like os_pretty ldd_output
    [[ "$(uname -s)" == Linux ]] || die "only Linux is supported by Oracle's generic MySQL server binaries"
    [[ -r /etc/os-release ]] || die "/etc/os-release is required"
    # Read in subshells: os-release defines VERSION, which must not overwrite
    # the requested MySQL VERSION in this process.
    # shellcheck disable=SC1091
    OS_ID="$(. /etc/os-release; printf '%s' "${ID:-}")"
    # shellcheck disable=SC1091
    os_like="$(. /etc/os-release; printf '%s' "${ID_LIKE:-}")"
    # shellcheck disable=SC1091
    os_pretty="$(. /etc/os-release; printf '%s' "${PRETTY_NAME:-}")"
    OS_ID="${OS_ID,,}"
    case "${os_like} ${OS_ID}" in
        *rhel*|*fedora*|*centos*|*rocky*|*almalinux*|*ol*) OS_FAMILY="rhel" ;;
        *debian*|*ubuntu*) OS_FAMILY="debian" ;;
        *suse*|*sles*) OS_FAMILY="suse" ;;
        *) die "unsupported distribution: ${os_pretty:-$OS_ID}; supported families: RHEL, Debian/Ubuntu, SUSE" ;;
    esac

    case "$(uname -m)" in
        x86_64|amd64) ARCH="x86_64" ;;
        aarch64|arm64) ARCH="aarch64" ;;
        i386|i486|i586|i686) ARCH="i686" ;;
        *) die "unsupported architecture: $(uname -m)" ;;
    esac
    if [[ "$ARCH" != x86_64 && "$SERIES" =~ ^5\. ]]; then
        die "MySQL $SERIES is supported by AIM only on x86_64; use a compatible custom package or a 64-bit host"
    fi
    command -v ldd >/dev/null 2>&1 || die "ldd is required"
    ldd_output="$(ldd --version 2>&1)"
    grep -qiE 'glibc|gnu libc|ubuntu|debian' <<<"$ldd_output" ||
        die "musl/unknown libc is unsupported; Oracle generic builds require glibc"
    LIBC_VERSION="$(getconf GNU_LIBC_VERSION 2>/dev/null | awk '{print $2}' || true)"
    local required_libc="2.5"
    if [[ "$SERIES" == 5.7 ]] || { [[ "$SERIES" == 5.6 ]] && version_ge "$VERSION" 5.6.37; }; then
        required_libc="2.12"
    fi
    [[ "$SERIES" =~ ^8\. ]] && required_libc="2.17"
    [[ -z "$LIBC_VERSION" ]] || version_ge "$LIBC_VERSION" "$required_libc" ||
        die "MySQL $SERIES generic binaries require glibc >= $required_libc (found $LIBC_VERSION)"
    log "platform: ${OS_ID} (${OS_FAMILY}), ${ARCH}, glibc ${LIBC_VERSION:-unknown}"
}

require_root() {
    (( DRY_RUN )) || [[ $EUID -eq 0 ]] || die "run as root (or use --dry-run to validate)"
}

tcp_port_is_listening() {
    local checked_port="$1"
    command -v ss >/dev/null 2>&1 &&
        ss -ltnH 2>/dev/null | awk '{print $4}' | grep -E "(^|:)${checked_port}$" >/dev/null
}

port_is_listening() { tcp_port_is_listening "$PORT"; }

check_port_and_paths() {
    if port_is_listening; then
        die "TCP port $PORT is already listening"
    fi
    if [[ "$ROLE" == mgr ]] && tcp_port_is_listening "$MGR_PORT"; then
        die "MGR XCom TCP port $MGR_PORT is already listening"
    fi
    if (( REINITIALIZE && DRY_RUN )); then
        return 0
    fi
    if [[ -d "$DATADIR" ]] && find "$DATADIR" -mindepth 1 -maxdepth 1 -print -quit | grep -q .; then
        die "data directory is not empty: $DATADIR"
    fi
    [[ ! -e "$CNF_FILE" ]] || die "configuration already exists: $CNF_FILE"
}

canonicalize_path() {
    local path="$1" suffix="" parent
    while [[ ! -e "$path" && ! -L "$path" && "$path" != / ]]; do
        suffix="/$(basename -- "$path")${suffix}"
        path="$(dirname -- "$path")"
    done
    if [[ -d "$path" ]]; then
        path="$(cd -P -- "$path" && pwd -P)"
    else
        parent="$(cd -P -- "$(dirname -- "$path")" && pwd -P)"
        path="${parent}/$(basename -- "$path")"
    fi
    printf '%s%s' "$path" "$suffix"
}

assert_safe_child_path() {
    local path="$1" root="$2" canonical_path canonical_root
    canonical_path="$(canonicalize_path "$path")"
    canonical_root="$(canonicalize_path "$root")"
    [[ -n "$canonical_path" && -n "$canonical_root" && "$canonical_path" != / && "$canonical_root" != / &&
        "$canonical_path" == "$canonical_root/"* ]] ||
        die "refusing unsafe instance path: $path (expected a child of $root)"
}

validate_uninstall_inputs() {
    local root_path
    [[ "$VERSION" =~ ^(5\.6|5\.7|8\.0|8\.4)\.[0-9]+$ ]] ||
        die "--uninstall requires an exact supported version"
    (( PORT_EXPLICIT )) || die "--uninstall requires an explicit -p/--port"
    [[ "$PORT" =~ ^[0-9]+$ ]] || die "invalid port: $PORT"
    PORT=$((10#$PORT))
    (( PORT >= 1 && PORT <= 65535 )) || die "invalid port: $PORT"
    (( ! REINITIALIZE )) || die "--uninstall cannot be combined with --reinitialize"
    for root_path in "$BASE_ROOT" "$DATA_ROOT" "$LOG_ROOT" "$TMP_ROOT"; do
        [[ "$root_path" == /* && "$root_path" != / ]] ||
            die "installation roots must be absolute non-root paths: $root_path"
        [[ "/$root_path/" != *"/../"* && "/$root_path/" != *"/./"* ]] ||
            die "installation roots must not contain . or .. path segments: $root_path"
    done
}

uninstall_instance() {
    validate_uninstall_inputs
    require_root

    local instance_root="${DATA_ROOT}/${PORT}"
    local socket="${instance_root}/mysql.sock"
    local pid_file="${instance_root}/mysql.pid"
    local logdir="${LOG_ROOT}/${PORT}"
    local tmpdir="${TMP_ROOT}/${PORT}"
    local basedir="${BASE_ROOT}/${VERSION}"
    local service="aim-mysql-${PORT}"
    local unit="/etc/systemd/system/${service}.service"
    local start_script="${BASE_ROOT}/start-${PORT}.sh"
    local stop_script="${BASE_ROOT}/stop-${PORT}.sh"
    local answer path pid="" has_artifacts=0

    assert_safe_child_path "$instance_root" "$DATA_ROOT"
    assert_safe_child_path "$logdir" "$LOG_ROOT"
    assert_safe_child_path "$tmpdir" "$TMP_ROOT"
    assert_safe_child_path "$start_script" "$BASE_ROOT"
    assert_safe_child_path "$stop_script" "$BASE_ROOT"
    for path in "$instance_root" "$logdir" "$tmpdir" "$unit" "$start_script" "$stop_script"; do
        [[ ! -e "$path" && ! -L "$path" ]] || has_artifacts=1
    done
    (( has_artifacts )) || die "no AIM instance artifacts found for port $PORT"

    warn "UNINSTALL WILL PERMANENTLY DELETE the MySQL instance on port $PORT"
    printf '[aim] remove: %s %s %s %s %s %s\n' \
        "$instance_root" "$logdir" "$tmpdir" "$unit" "$start_script" "$stop_script"
    if (( DRY_RUN )); then
        log "dry-run: uninstall preview completed; nothing was stopped or deleted"
        return
    fi
    if (( ! ASSUME_YES )); then
        [[ -t 0 ]] || die "uninstall confirmation requires a terminal; review --dry-run, then pass --yes"
        read -r -p "Type the port number (${PORT}) to permanently uninstall this instance: " answer
        [[ "$answer" == "$PORT" ]] || die "confirmation did not match; instance was not changed"
    fi

    if command -v systemctl >/dev/null 2>&1 && [[ -d /run/systemd/system ]] &&
        { [[ -e "$unit" ]] || systemctl list-unit-files "${service}.service" --no-legend 2>/dev/null | grep -q "$service"; }; then
        systemctl disable --now "$service"
    fi
    if [[ -S "$socket" ]] && port_is_listening; then
        [[ -n "$ROOT_PASSWORD" ]] || die "instance is still running; set AIM_ROOT_PASSWORD for graceful shutdown"
        MYSQL_PWD="$ROOT_PASSWORD" "$basedir/bin/mysqladmin" \
            --protocol=socket --socket="$socket" --user=root shutdown
    fi
    port_is_listening && die "port $PORT is still listening after shutdown; refusing deletion"
    if [[ -r "$pid_file" ]]; then
        read -r pid <"$pid_file" || true
        if [[ "$pid" =~ ^[0-9]+$ ]] && kill -0 "$pid" 2>/dev/null; then
            die "mysqld process $pid is still running; refusing deletion"
        fi
    fi

    rm -rf -- "$instance_root" "$logdir" "$tmpdir"
    rm -f -- "$unit" "$start_script" "$stop_script"
    if command -v systemctl >/dev/null 2>&1 && [[ -d /run/systemd/system ]]; then
        systemctl daemon-reload
    fi
    log "instance removed; shared binaries, media, and aim.conf were retained"
}

reinitialize_instance() {
    (( REINITIALIZE )) || return 0
    local instance_root unit start_script stop_script answer path pid="" has_artifacts=0
    instance_root="$(dirname -- "$CNF_FILE")"
    unit="/etc/systemd/system/${SERVICE_NAME}.service"
    start_script="${BASE_ROOT}/start-${PORT}.sh"
    stop_script="${BASE_ROOT}/stop-${PORT}.sh"

    assert_safe_child_path "$instance_root" "$DATA_ROOT"
    assert_safe_child_path "$LOGDIR" "$LOG_ROOT"
    assert_safe_child_path "$TMPDIR_INSTANCE" "$TMP_ROOT"
    assert_safe_child_path "$start_script" "$BASE_ROOT"
    assert_safe_child_path "$stop_script" "$BASE_ROOT"
    for path in "$instance_root" "$LOGDIR" "$TMPDIR_INSTANCE" "$unit" "$start_script" "$stop_script"; do
        [[ ! -e "$path" && ! -L "$path" ]] || has_artifacts=1
    done
    (( has_artifacts )) || { log "no existing instance artifacts found for port $PORT; normal initialization will continue"; return; }

    [[ ! -S "$SOCKET" ]] || die "instance socket still exists at $SOCKET; stop MySQL before reinitializing"
    if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
        die "systemd service $SERVICE_NAME is active; stop it before reinitializing"
    fi
    if [[ -r "$PID_FILE" ]]; then
        read -r pid <"$PID_FILE" || true
        if [[ "$pid" =~ ^[0-9]+$ ]] && kill -0 "$pid" 2>/dev/null; then
            die "mysqld process $pid is still running; stop it before reinitializing"
        fi
    fi
    port_is_listening && die "TCP port $PORT is still listening; stop the service before reinitializing"

    warn "REINITIALIZE WILL PERMANENTLY DELETE the MySQL instance on port $PORT"
    printf '[aim] remove: %s %s %s %s %s %s\n' \
        "$instance_root" "$LOGDIR" "$TMPDIR_INSTANCE" "$unit" "$start_script" "$stop_script"
    if (( DRY_RUN )); then
        log "dry-run: reinitialize preview completed; nothing was deleted"
        return
    fi
    if (( ! ASSUME_YES )); then
        [[ -t 0 ]] || die "reinitialize confirmation requires a terminal; review --dry-run, then pass --yes"
        read -r -p "Type the port number (${PORT}) to permanently delete this instance: " answer
        [[ "$answer" == "$PORT" ]] || die "confirmation did not match; instance was not changed"
    fi

    rm -rf -- "$instance_root" "$LOGDIR" "$TMPDIR_INSTANCE"
    rm -f -- "$unit" "$start_script" "$stop_script"
    if command -v systemctl >/dev/null 2>&1 && [[ -d /run/systemd/system ]]; then
        systemctl daemon-reload
    fi
    log "instance artifacts removed; shared binaries and media were retained"
}

create_directory_0755() {
    local directory="$1" parent
    [[ -d "$directory" ]] && return 0
    [[ ! -e "$directory" ]] || die "installation path is not a directory: $directory"
    parent="$(dirname -- "$directory")"
    if [[ "$parent" != "$directory" ]]; then
        create_directory_0755 "$parent"
    fi
    run mkdir "$directory"
    (( DRY_RUN )) || chmod 0755 "$directory"
}

prepare_install_roots() {
    local root
    for root in "$BASE_ROOT" "$DATA_ROOT" "$LOG_ROOT" "$TMP_ROOT"; do
        create_directory_0755 "$root"
    done
    if (( DRY_RUN )); then
        log "would create missing installation roots with mode 0755"
    fi
}

install_dependencies() {
    local manager aio_package
    (( INSTALL_DEPS )) || { log "dependency installation skipped"; return; }
    case "$OS_FAMILY" in
        rhel)
            if command -v dnf >/dev/null 2>&1; then
                manager=dnf
            else
                manager=yum
            fi
            run "$manager" install -y libaio numactl-libs perl tar xz curl openssl
            if [[ "$SERIES" =~ ^5\. ]]; then
                "$manager" -q list ncurses-compat-libs >/dev/null 2>&1 && run "$manager" install -y ncurses-compat-libs
                "$manager" -q list libxcrypt-compat >/dev/null 2>&1 && run "$manager" install -y libxcrypt-compat
            fi
            if command -v getenforce >/dev/null 2>&1 && [[ "$(getenforce)" != Disabled ]] && ! command -v semanage >/dev/null 2>&1; then
                if "$manager" -q list policycoreutils-python-utils >/dev/null 2>&1; then
                    run "$manager" install -y policycoreutils-python-utils
                elif "$manager" -q list policycoreutils-python >/dev/null 2>&1; then
                    run "$manager" install -y policycoreutils-python
                fi
            fi
            ;;
        debian)
            run env DEBIAN_FRONTEND=noninteractive apt-get update
            aio_package="libaio1"
            apt-cache show libaio1t64 >/dev/null 2>&1 && aio_package="libaio1t64"
            run env DEBIAN_FRONTEND=noninteractive apt-get install -y "$aio_package" libnuma1 perl tar xz-utils curl openssl
            if [[ "$SERIES" =~ ^5\. ]] && apt-cache show libncurses5 >/dev/null 2>&1; then
                run env DEBIAN_FRONTEND=noninteractive apt-get install -y libncurses5
            fi
            ;;
        suse)
            run zypper --non-interactive install libaio1 libnuma1 perl tar xz curl openssl
            if [[ "$SERIES" =~ ^5\. ]] && zypper --non-interactive search --match-exact libncurses5 >/dev/null 2>&1; then
                run zypper --non-interactive install libncurses5
            fi
            ;;
    esac
}

archive_candidates() {
    local baseline
    case "${SERIES}:${ARCH}" in
        5.6:x86_64)
            if version_ge "$VERSION" 5.6.37; then
                printf '%s\n' "mysql-${VERSION}-linux-glibc2.12-x86_64.tar.gz"
            else
                printf '%s\n' "mysql-${VERSION}-linux-glibc2.5-x86_64.tar.gz"
            fi
            ;;
        5.7:x86_64)
            printf '%s\n' "mysql-${VERSION}-linux-glibc2.12-x86_64.tar.gz"
            printf '%s\n' "mysql-${VERSION}-linux-glibc2.5-x86_64.tar.gz"
            ;;
        8.*:*)
            # Oracle publishes multiple generic builds for newer releases.
            # Prefer the newest ABI baseline supported by this host, then
            # fall back to older compatible builds. Prefer compressed media.
            for baseline in 2.28 2.17; do
                if [[ "$SERIES" == 8.0 && "$ARCH" == aarch64 && "$baseline" == 2.17 ]] &&
                    version_ge "$VERSION" 8.0.46; then
                    continue
                fi
                if { [[ -n "$LIBC_VERSION" ]] && version_ge "$LIBC_VERSION" "$baseline"; } ||
                    { [[ -z "$LIBC_VERSION" ]] && [[ "$baseline" == 2.17 ]]; }; then
                    printf '%s\n' "mysql-${VERSION}-linux-glibc${baseline}-${ARCH}.tar.xz"
                    printf '%s\n' "mysql-${VERSION}-linux-glibc${baseline}-${ARCH}.tar"
                    if [[ "$baseline" == 2.17 && "$ARCH" == x86_64 ]]; then
                        printf '%s\n' "mysql-${VERSION}-linux-glibc${baseline}-${ARCH}-minimal.tar.xz"
                        printf '%s\n' "mysql-${VERSION}-linux-glibc${baseline}-${ARCH}-minimal.tar"
                    fi
                fi
            done
            ;;
    esac
}

validate_archive_compatibility() {
    local filename required_libc package_arch
    filename="$(basename -- "$ARCHIVE")"
    [[ "$filename" != mysql-test-* ]] ||
        die "archive $filename is a MySQL test suite, not an installable server package"
    if [[ "$filename" =~ glibc([0-9]+\.[0-9]+) ]]; then
        required_libc="${BASH_REMATCH[1]}"
        if [[ -n "$LIBC_VERSION" ]] && ! version_ge "$LIBC_VERSION" "$required_libc"; then
            die "archive $filename requires glibc >= $required_libc (host: $LIBC_VERSION)"
        fi
    fi
    case "$filename" in
        *-x86_64.tar*|*-x86_64-minimal.tar*) package_arch="x86_64" ;;
        *-aarch64.tar*) package_arch="aarch64" ;;
        *-i686.tar*) package_arch="i686" ;;
        *) package_arch="" ;;
    esac
    if [[ -n "$package_arch" && "$package_arch" != "$ARCH" ]]; then
        die "archive architecture is $package_arch, but this host is $ARCH"
    fi
}

obtain_archive() {
    local candidate url destination base_url
    if [[ -n "$ARCHIVE" ]]; then
        ARCHIVE="$(cd -- "$(dirname -- "$ARCHIVE")" && pwd -P)/$(basename -- "$ARCHIVE")"
        [[ -r "$ARCHIVE" ]] || die "archive is not readable: $ARCHIVE"
        return
    fi
    if [[ -n "$DOWNLOAD_URL" ]]; then
        candidate="$(basename -- "${DOWNLOAD_URL%%\?*}")"
        destination="${SCRIPT_DIR}/media/${candidate}"
        run mkdir -p "${SCRIPT_DIR}/media"
        (( DRY_RUN )) || curl --fail --location --retry 3 --continue-at - --output "$destination" "$DOWNLOAD_URL"
        ARCHIVE="$destination"
        return
    fi
    while IFS= read -r candidate; do
        [[ -n "$candidate" ]] || continue
        if [[ -r "${SCRIPT_DIR}/media/${candidate}" ]]; then
            ARCHIVE="${SCRIPT_DIR}/media/${candidate}"
            log "using local archive: $ARCHIVE"
            return
        fi
    done < <(archive_candidates)
    (( DOWNLOAD )) || die "archive not found in media/ and downloads are disabled"

    run mkdir -p "${SCRIPT_DIR}/media"
    while IFS= read -r candidate; do
        [[ -n "$candidate" ]] || continue
        destination="${SCRIPT_DIR}/media/${candidate}"
        for base_url in \
            "https://cdn.mysql.com/Downloads/MySQL-${SERIES}" \
            "https://downloads.mysql.com/archives/get/p/23/file"; do
            url="${base_url}/${candidate}"
            log "downloading $url"
            if (( DRY_RUN )); then
                ARCHIVE="$destination"
                return
            fi
            if curl --fail --location --retry 2 --continue-at - --output "$destination" "$url"; then
                ARCHIVE="$destination"
                return
            fi
            rm -f -- "$destination"
        done
    done < <(archive_candidates)
    die "no official archive found; download it into media/ or use --archive/--download-url"
}

ensure_mysql_user() {
    if ! getent group "$MYSQL_GROUP" >/dev/null 2>&1; then run groupadd --system "$MYSQL_GROUP"; fi
    if ! id "$MYSQL_USER" >/dev/null 2>&1; then
        run useradd --system --gid "$MYSQL_GROUP" --home-dir /nonexistent --shell /usr/sbin/nologin "$MYSQL_USER"
    fi
}

extract_mysql() {
    local actual missing
    if [[ -x "$BASEDIR/bin/mysqld" ]]; then
        missing="$(ldd "$BASEDIR/bin/mysqld" 2>/dev/null | awk '/not found/ {print $1}' | paste -sd, -)"
        [[ -z "$missing" ]] || die "MySQL runtime libraries are missing: $missing (install the distribution compatibility packages)"
        actual="$("$BASEDIR/bin/mysqld" --no-defaults --version 2>/dev/null | awk '{print $3}')"
        [[ "$actual" == "$VERSION" ]] || die "existing $BASEDIR contains MySQL $actual, expected $VERSION"
        log "reusing existing MySQL binaries in $BASEDIR"
        return
    fi
    [[ ! -e "$BASEDIR" ]] || die "base directory exists but is not a valid MySQL installation: $BASEDIR"
    run mkdir -p "$BASEDIR"
    if (( DRY_RUN )); then
        log "would extract $ARCHIVE to $BASEDIR"
        return
    fi
    case "$ARCHIVE" in
        *.tar.gz|*.tgz) tar -xzf "$ARCHIVE" --strip-components=1 -C "$BASEDIR" ;;
        *.tar.xz) tar -xJf "$ARCHIVE" --strip-components=1 -C "$BASEDIR" ;;
        *.tar) tar -xf "$ARCHIVE" --strip-components=1 -C "$BASEDIR" ;;
        *) die "unsupported archive format: $ARCHIVE" ;;
    esac
    [[ -x "$BASEDIR/bin/mysqld" ]] || die "archive does not contain bin/mysqld"
    missing="$(ldd "$BASEDIR/bin/mysqld" 2>/dev/null | awk '/not found/ {print $1}' | paste -sd, -)"
    [[ -z "$missing" ]] || die "MySQL runtime libraries are missing: $missing (install the distribution compatibility packages)"
    actual="$("$BASEDIR/bin/mysqld" --no-defaults --version 2>/dev/null | awk '{print $3}')"
    [[ "$actual" == "$VERSION" ]] || die "archive version is $actual, expected $VERSION"
    run chown -R root:"$MYSQL_GROUP" "$BASEDIR"
}

memory_megabytes() {
    awk '/MemTotal/ {printf "%d", $2 / 1024}' /proc/meminfo
}

write_config() {
    local memory buffer_pool expire_config role_config gtid_config binlog_format_config updates_option
    local mgr_config mgr_applier_config
    memory="$(memory_megabytes)"
    buffer_pool=$(( memory * 60 / 100 ))
    (( buffer_pool < 128 )) && buffer_pool=128
    (( buffer_pool > 131072 )) && buffer_pool=131072
    if [[ "$SERIES" =~ ^8\. ]]; then expire_config="binlog_expire_logs_seconds = 604800"; else expire_config="expire_logs_days = 7"; fi
    role_config=""
    if [[ "$ROLE" == replica ]]; then
        role_config=$'read_only = ON'
        if [[ "$SERIES" =~ ^8\. ]] || { [[ "$SERIES" == 5.7 ]] && version_ge "$VERSION" 5.7.8; }; then
            role_config+=$'\nsuper_read_only = ON'
        fi
    fi
    binlog_format_config="binlog_format = ROW"
    if [[ "$SERIES" =~ ^8\. ]] && version_ge "$VERSION" 8.0.34; then
        binlog_format_config=""
    fi
    updates_option="log_slave_updates"
    if [[ "$SERIES" =~ ^8\. ]] && version_ge "$VERSION" 8.0.26; then
        updates_option="log_replica_updates"
    fi
    gtid_config=""
    if (( GTID )); then
        gtid_config="gtid_mode = ON
enforce_gtid_consistency = ON
${updates_option} = ON"
    fi
    mgr_config=""
    if [[ "$ROLE" == mgr ]]; then
        if version_ge "$VERSION" 8.0.27; then
            mgr_applier_config="replica_parallel_workers = 4
replica_preserve_commit_order = ON"
        elif version_ge "$VERSION" 8.0.26; then
            mgr_applier_config="replica_parallel_type = LOGICAL_CLOCK
replica_parallel_workers = 4
replica_preserve_commit_order = ON"
        else
            mgr_applier_config="slave_parallel_type = LOGICAL_CLOCK
slave_parallel_workers = 4
slave_preserve_commit_order = ON"
        fi
        mgr_config="plugin_load_add = group_replication.so
report_host = ${MGR_LOCAL_ADDRESS}
loose-group_replication_group_name = ${MGR_GROUP_NAME}
loose-group_replication_start_on_boot = OFF
loose-group_replication_local_address = ${MGR_LOCAL_ADDRESS}:${MGR_PORT}
loose-group_replication_group_seeds = ${MGR_SEEDS}
loose-group_replication_bootstrap_group = OFF
loose-group_replication_single_primary_mode = ON
loose-group_replication_enforce_update_everywhere_checks = OFF
loose-group_replication_ip_allowlist = ${MGR_ALLOWLIST}
loose-group_replication_recovery_get_public_key = ON
loose-group_replication_exit_state_action = READ_ONLY
loose-group_replication_autorejoin_tries = 3
loose-group_replication_consistency = BEFORE_ON_PRIMARY_FAILOVER
${mgr_applier_config}"
    fi

    run mkdir -p "$(dirname -- "$CNF_FILE")" "$DATADIR" "$LOGDIR/binlog" "$LOGDIR/relaylog" "$TMPDIR_INSTANCE"
    if (( DRY_RUN )); then
        log "would write version-aware config: $CNF_FILE (buffer pool ${buffer_pool}M)"
        return
    fi
    cat >"$CNF_FILE" <<EOF
# Generated by AIM ${AIM_VERSION}; instance ${PORT}, MySQL ${VERSION}.
[client]
port = ${PORT}
socket = ${SOCKET}

[mysql]
no-auto-rehash
default-character-set = utf8mb4

[mysqld]
user = ${MYSQL_USER}
port = ${PORT}
bind_address = ${BIND_ADDRESS}
server_id = ${SERVER_ID}
basedir = ${BASEDIR}
datadir = ${DATADIR}
socket = ${SOCKET}
pid_file = ${PID_FILE}
tmpdir = ${TMPDIR_INSTANCE}
log_error = ${LOGDIR}/error.log
slow_query_log = ON
slow_query_log_file = ${LOGDIR}/slow.log
long_query_time = 1
skip_name_resolve = ON
character_set_server = utf8mb4
collation_server = utf8mb4_unicode_ci
max_connections = 1000
max_allowed_packet = 64M
open_files_limit = 65535
default_storage_engine = InnoDB
innodb_buffer_pool_size = ${buffer_pool}M
innodb_file_per_table = ON
innodb_flush_log_at_trx_commit = 1
sync_binlog = 1
log_bin = ${LOGDIR}/binlog/mysql-bin
relay_log = ${LOGDIR}/relaylog/relay-bin
${binlog_format_config}
${expire_config}
${gtid_config}
${role_config}
${mgr_config}
EOF
    chmod 640 "$CNF_FILE"
    chown root:"$MYSQL_GROUP" "$CNF_FILE"
    chown -R "$MYSQL_USER":"$MYSQL_GROUP" "$(dirname -- "$CNF_FILE")" "$DATADIR" "$LOGDIR" "$TMPDIR_INSTANCE"
}

initialize_database() {
    if (( DRY_RUN )); then log "would initialize MySQL $VERSION in $DATADIR"; return; fi
    if [[ "$SERIES" == 5.6 ]] || { [[ "$SERIES" == 5.7 ]] && ! version_ge "$VERSION" 5.7.6; }; then
        "$BASEDIR/scripts/mysql_install_db" --defaults-file="$CNF_FILE" --user="$MYSQL_USER" --basedir="$BASEDIR" --datadir="$DATADIR"
    else
        "$BASEDIR/bin/mysqld" --defaults-file="$CNF_FILE" --initialize-insecure --user="$MYSQL_USER"
    fi
}

write_control_scripts() {
    local start_file="${BASE_ROOT}/start-${PORT}.sh" stop_file="${BASE_ROOT}/stop-${PORT}.sh"
    run mkdir -p "$BASE_ROOT"
    if (( DRY_RUN )); then log "would write $start_file and $stop_file"; return; fi
    cat >"$start_file" <<EOF
#!/usr/bin/env bash
set -euo pipefail
exec "${BASEDIR}/bin/mysqld_safe" --defaults-file="${CNF_FILE}"
EOF
    cat >"$stop_file" <<EOF
#!/usr/bin/env bash
set -euo pipefail
MYSQL_PWD="\${MYSQL_ROOT_PASSWORD:?export MYSQL_ROOT_PASSWORD first}" \\
  exec "${MYSQLADMIN}" --protocol=socket --socket="${SOCKET}" --user=root shutdown
EOF
    chmod 750 "$start_file" "$stop_file"
}

install_limits() {
    local limits=/etc/security/limits.d/90-aim-mysql.conf
    if (( DRY_RUN )); then log "would write $limits"; return; fi
    cat >"$limits" <<EOF
# Managed by AIM. Applies to all AIM MySQL instances.
${MYSQL_USER} soft nofile 65535
${MYSQL_USER} hard nofile 65535
${MYSQL_USER} soft nproc  65535
${MYSQL_USER} hard nproc  65535
EOF
}

configure_selinux() {
    [[ "$OS_FAMILY" == rhel ]] || return 0
    command -v getenforce >/dev/null 2>&1 || return 0
    [[ "$(getenforce)" != Disabled ]] || return 0
    if (( DRY_RUN )); then
        log "would label custom paths and TCP port for SELinux"
        return
    fi
    if ! command -v semanage >/dev/null 2>&1 || ! command -v restorecon >/dev/null 2>&1; then
        die "SELinux is enabled but semanage/restorecon is unavailable; install policycoreutils tools"
    fi

    semanage fcontext -a -t mysqld_db_t "${DATA_ROOT}(/.*)?" 2>/dev/null ||
        semanage fcontext -m -t mysqld_db_t "${DATA_ROOT}(/.*)?"
    semanage fcontext -a -t mysqld_log_t "${LOG_ROOT}(/.*)?" 2>/dev/null ||
        semanage fcontext -m -t mysqld_log_t "${LOG_ROOT}(/.*)?"
    semanage fcontext -a -t mysqld_db_t "${TMP_ROOT}(/.*)?" 2>/dev/null ||
        semanage fcontext -m -t mysqld_db_t "${TMP_ROOT}(/.*)?"
    semanage port -a -t mysqld_port_t -p tcp "$PORT" 2>/dev/null ||
        semanage port -m -t mysqld_port_t -p tcp "$PORT"
    if [[ "$ROLE" == mgr ]]; then
        semanage port -a -t mysqld_port_t -p tcp "$MGR_PORT" 2>/dev/null ||
            semanage port -m -t mysqld_port_t -p tcp "$MGR_PORT"
    fi
    restorecon -RF "$DATA_ROOT" "$LOG_ROOT" "$TMP_ROOT"
}

verify_instance_permissions() {
    (( DRY_RUN )) && { log "would verify mysql can write data, log, and temporary directories"; return; }
    local directory probe
    for directory in "$DATADIR" "$LOGDIR" "$TMPDIR_INSTANCE"; do
        probe="${directory}/.aim-write-test-$$"
        if ! runuser -u "$MYSQL_USER" -- touch "$probe" 2>/dev/null; then
            warn "mysql cannot write to $directory"
            command -v namei >/dev/null 2>&1 && namei -l "$directory" >&2 || true
            ls -ldZ "$directory" "$(dirname -- "$directory")" 2>/dev/null >&2 || true
            die "directory permissions or SELinux context prevent mysql access; fix the path shown above"
        fi
        rm -f -- "$probe"
    done
}

install_systemd_unit() {
    command -v systemctl >/dev/null 2>&1 && [[ -d /run/systemd/system ]] || return 1
    local unit="/etc/systemd/system/${SERVICE_NAME}.service"
    if (( DRY_RUN )); then log "would install systemd unit: $unit"; return 0; fi
    cat >"$unit" <<EOF
[Unit]
Description=AIM MySQL ${VERSION} instance on port ${PORT}
After=network.target

[Service]
Type=simple
User=${MYSQL_USER}
Group=${MYSQL_GROUP}
ExecStart=${BASEDIR}/bin/mysqld --defaults-file=${CNF_FILE}
LimitNOFILE=65535
TimeoutStartSec=300
TimeoutStopSec=300
Restart=on-failure
PrivateTmp=false

[Install]
WantedBy=multi-user.target
EOF
    systemctl daemon-reload
    systemctl enable --now "$SERVICE_NAME"
}

start_database() {
    if install_systemd_unit; then return; fi
    if (( DRY_RUN )); then log "would start mysqld_safe without systemd"; return; fi
    runuser -u "$MYSQL_USER" -- "$BASEDIR/bin/mysqld_safe" --defaults-file="$CNF_FILE" >/dev/null 2>&1 &
}

wait_for_mysql() {
    (( DRY_RUN )) && return
    local _attempt
    for _attempt in {1..60}; do
        if "$MYSQLADMIN" --protocol=socket --socket="$SOCKET" --user=root ping >/dev/null 2>&1; then return; fi
        sleep 2
    done
    tail -n 80 "$LOGDIR/error.log" >&2 || true
    die "MySQL did not become ready within 120 seconds"
}

mysql_root() {
    MYSQL_PWD="$ROOT_PASSWORD" "$MYSQL" --protocol=socket --socket="$SOCKET" --user=root --batch --skip-column-names "$@"
}

secure_root() {
    [[ -n "$ROOT_PASSWORD" ]] || ROOT_PASSWORD="$(random_password)"
    (( DRY_RUN )) && { log "would set the local root password"; return; }
    local escaped
    escaped="$(sql_escape "$ROOT_PASSWORD")"
    if [[ "$SERIES" == 5.6 ]] || { [[ "$SERIES" == 5.7 ]] && ! version_ge "$VERSION" 5.7.6; }; then
        "$MYSQL" --protocol=socket --socket="$SOCKET" --user=root -e \
            "UPDATE mysql.user SET Password=PASSWORD('${escaped}') WHERE User='root'; DELETE FROM mysql.user WHERE User=''; DROP DATABASE IF EXISTS test; FLUSH PRIVILEGES;"
    else
        "$MYSQL" --protocol=socket --socket="$SOCKET" --user=root -e \
            "ALTER USER 'root'@'localhost' IDENTIFIED BY '${escaped}'; DROP DATABASE IF EXISTS test;"
    fi
}

configure_source() {
    [[ "$ROLE" == source ]] || return 0
    [[ -n "$REPL_PASSWORD" ]] || REPL_PASSWORD="$(random_password)"
    (( DRY_RUN )) && { log "would create replication account ${REPL_USER}@${REPLICA_HOST}"; return; }
    local user password host
    user="$(sql_escape "$REPL_USER")"
    password="$(sql_escape "$REPL_PASSWORD")"
    host="$(sql_escape "$REPLICA_HOST")"
    if [[ "$SERIES" == 5.6 ]]; then
        mysql_root -e "GRANT REPLICATION SLAVE, REPLICATION CLIENT ON *.* TO '${user}'@'${host}' IDENTIFIED BY '${password}'; FLUSH PRIVILEGES;"
    else
        mysql_root -e "CREATE USER '${user}'@'${host}' IDENTIFIED BY '${password}'; GRANT REPLICATION SLAVE, REPLICATION CLIENT ON *.* TO '${user}'@'${host}';"
    fi
}

configure_replica() {
    [[ "$ROLE" == replica ]] || return 0
    (( DRY_RUN )) && { log "would configure GTID replication from ${SOURCE_HOST}:${SOURCE_PORT}"; return; }
    local host user password sql
    host="$(sql_escape "$SOURCE_HOST")"
    user="$(sql_escape "$SOURCE_USER")"
    password="$(sql_escape "$SOURCE_PASSWORD")"
    if version_ge "$VERSION" 8.0.23; then
        sql="CHANGE REPLICATION SOURCE TO SOURCE_HOST='${host}', SOURCE_PORT=${SOURCE_PORT}, SOURCE_USER='${user}', SOURCE_PASSWORD='${password}', SOURCE_AUTO_POSITION=1, GET_SOURCE_PUBLIC_KEY=1; START REPLICA;"
    elif [[ "$SERIES" == 8.0 ]]; then
        sql="CHANGE MASTER TO MASTER_HOST='${host}', MASTER_PORT=${SOURCE_PORT}, MASTER_USER='${user}', MASTER_PASSWORD='${password}', MASTER_AUTO_POSITION=1, GET_MASTER_PUBLIC_KEY=1; START SLAVE;"
    else
        sql="CHANGE MASTER TO MASTER_HOST='${host}', MASTER_PORT=${SOURCE_PORT}, MASTER_USER='${user}', MASTER_PASSWORD='${password}', MASTER_AUTO_POSITION=1; START SLAVE;"
    fi
    mysql_root -e "$sql"
}

configure_mgr() {
    [[ "$ROLE" == mgr ]] || return 0
    if (( DRY_RUN )); then
        if (( MGR_BOOTSTRAP )); then
            log "would bootstrap MGR group $MGR_GROUP_NAME at ${MGR_LOCAL_ADDRESS}:${MGR_PORT}"
        else
            log "would join MGR group $MGR_GROUP_NAME at ${MGR_LOCAL_ADDRESS}:${MGR_PORT}"
        fi
        return
    fi

    local business_tables recovery_user recovery_password member_state="" _attempt
    business_tables="$(mysql_root -e "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema NOT IN ('mysql','information_schema','performance_schema','sys');")"
    [[ "$business_tables" == 0 ]] ||
        die "role=mgr refuses an instance containing business tables; provision a validated consistent snapshot manually"

    recovery_user="$(sql_escape "$MGR_RECOVERY_USER")"
    recovery_password="$(sql_escape "$MGR_RECOVERY_PASSWORD")"
    mysql_root <<SQL
SET SQL_LOG_BIN=0;
CREATE USER IF NOT EXISTS '${recovery_user}'@'%' IDENTIFIED BY '${recovery_password}';
GRANT REPLICATION SLAVE, CONNECTION_ADMIN, BACKUP_ADMIN ON *.* TO '${recovery_user}'@'%';
SET SQL_LOG_BIN=1;
RESET MASTER;
CHANGE REPLICATION SOURCE TO SOURCE_USER='${recovery_user}', SOURCE_PASSWORD='${recovery_password}' FOR CHANNEL 'group_replication_recovery';
SQL
    warn "MGR recovery credentials are stored by MySQL in replication metadata; protect the host and MySQL data directory"

    if (( MGR_BOOTSTRAP )); then
        mysql_root -e "SET GLOBAL group_replication_bootstrap_group=ON;"
        if ! mysql_root -e "START GROUP_REPLICATION;"; then
            mysql_root -e "SET GLOBAL group_replication_bootstrap_group=OFF;" || true
            die "failed to bootstrap MGR; bootstrap mode was disabled again"
        fi
        mysql_root -e "SET GLOBAL group_replication_bootstrap_group=OFF;"
    else
        mysql_root -e "START GROUP_REPLICATION;"
    fi

    for _attempt in {1..60}; do
        member_state="$(mysql_root -e "SELECT MEMBER_STATE FROM performance_schema.replication_group_members WHERE MEMBER_ID=@@server_uuid;" || true)"
        [[ "$member_state" == ONLINE ]] && break
        sleep 2
    done
    [[ "$member_state" == ONLINE ]] || die "MGR member did not become ONLINE within 120 seconds (state: ${member_state:-missing})"
    mysql_root -e "SET PERSIST group_replication_start_on_boot=ON;"
    mysql_root -e "SELECT MEMBER_HOST, MEMBER_PORT, MEMBER_STATE, MEMBER_ROLE, MEMBER_VERSION FROM performance_schema.replication_group_members ORDER BY MEMBER_HOST;"
}

verify_installation() {
    (( DRY_RUN )) && { log "validation completed; no host changes were made"; return; }
    local actual
    actual="$(mysql_root -e 'SELECT VERSION();')"
    [[ "$actual" == "$VERSION"* ]] || die "running server version mismatch: $actual"
    if [[ "$ROLE" == replica ]]; then
        if version_ge "$VERSION" 8.0.23; then mysql_root -e 'SHOW REPLICA STATUS\G'; else mysql_root -e 'SHOW SLAVE STATUS\G'; fi
    fi
    if [[ "$ROLE" == mgr ]]; then
        [[ "$(mysql_root -e "SELECT MEMBER_STATE FROM performance_schema.replication_group_members WHERE MEMBER_ID=@@server_uuid;")" == ONLINE ]] ||
            die "MGR verification failed: local member is not ONLINE"
    fi
    log "MySQL $actual is running on port $PORT"
}

summary() {
    cat <<EOF

Installation summary
  version:       ${VERSION}
  role:          ${ROLE}
  service:       ${SERVICE_NAME}
  config:        ${CNF_FILE}
  basedir:       ${BASEDIR}
  datadir:       ${DATADIR}
  socket:        ${SOCKET}
EOF
    if (( ! DRY_RUN )); then
        printf '  root password: %s\n' "$ROOT_PASSWORD"
        [[ "$ROLE" == source ]] && printf '  replication:  %s / %s\n' "$REPL_USER" "$REPL_PASSWORD"
        if [[ "$ROLE" == mgr ]]; then
            printf '  MGR group:     %s\n' "$MGR_GROUP_NAME"
            printf '  MGR address:   %s:%s\n' "$MGR_LOCAL_ADDRESS" "$MGR_PORT"
            printf '  MGR mode:      %s\n' "$([[ "$MGR_BOOTSTRAP" == 1 ]] && printf bootstrap || printf join)"
        fi
        if [[ "$ROLE" == mgr ]]; then
            warn "store the root password now; MySQL persists the MGR recovery credential in replication metadata"
        else
            warn "store the displayed credentials now; AIM does not write them to disk"
        fi
    fi
}

main() {
    prescan_args "$@"
    load_config
    reset_cli_action_flags
    apply_secret_environment
    parse_args "$@"
    if (( UNINSTALL )); then
        uninstall_instance
        return
    fi
    validate_inputs
    detect_platform
    require_root
    reinitialize_instance
    check_port_and_paths
    prepare_install_roots
    log "installing MySQL $VERSION as $ROLE on port $PORT"
    install_dependencies
    if [[ -x "$BASEDIR/bin/mysqld" ]]; then
        log "an installed binary tree is available; package acquisition is skipped"
    else
        obtain_archive
        validate_archive_compatibility
    fi
    ensure_mysql_user
    extract_mysql
    write_config
    install_limits
    configure_selinux
    verify_instance_permissions
    initialize_database
    write_control_scripts
    start_database
    wait_for_mysql
    secure_root
    configure_source
    configure_replica
    configure_mgr
    verify_installation
    summary
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
    main "$@"
fi
