#!/usr/bin/env bash
# AIM - install isolated MySQL Community Server instances from generic binaries.

set -Eeuo pipefail
umask 027

readonly AIM_VERSION="2.0.0"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"

VERSION=""
PORT="3306"
ROLE="standalone"
CONFIG_FILE="${SCRIPT_DIR}/etc/config"
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
AIM ${AIM_VERSION} - MySQL 5.6/5.7/8.0/8.4 installer

Usage:
  sudo $0 -v VERSION [-p PORT] [options]

Required:
  -v, --version VERSION       Exact MySQL version, e.g. 5.7.44, 8.0.42, 8.4.5

Instance:
  -p, --port PORT             TCP port (default: 3306)
      --role ROLE             standalone, source/master, or replica/slave
  -g, --gtid                 Enable GTID (default)
      --no-gtid              Disable GTID (replica mode requires GTID)
      --server-id ID          Replication server_id (derived automatically)
      --bind-address ADDRESS  Listen address (default: 0.0.0.0)
      --root-password PASS    Root password (random when omitted)

Replication:
      --replica-host HOST     Account host used on a source (default: %)
      --repl-user USER        Account created on a source
      --repl-password PASS    Account password (random when omitted)
      --source-host HOST      Source host; required for role=replica
      --source-port PORT      Source port (default: 3306)
      --source-user USER      Replication account on source
      --source-password PASS  Replication password on source

Paths and package:
  -c, --config FILE           Optional shell config (default: etc/config)
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
      --yes                   Confirm destructive --reinitialize non-interactively
  -h, --help                  Show this help

Examples:
  sudo $0 -v 8.4.5 -p 3306 --role standalone
  sudo $0 -v 8.0.42 -p 3306 --role source --replica-host 10.0.0.12
  sudo $0 -v 8.0.42 -p 3306 --role replica --source-host 10.0.0.11 \\
      --source-password 'secret'
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

# Read -v/-p/-c early because legacy config files may derive paths from them.
prescan_args() {
    local index=1 arg
    while (( index <= $# )); do
        arg="${!index}"
        case "$arg" in
            -v|--version) ((index++)); VERSION="${!index:-}" ;;
            --version=*) VERSION="${arg#*=}" ;;
            -p|--port) ((index++)); PORT="${!index:-}" ;;
            --port=*) PORT="${arg#*=}" ;;
            -c|--config) ((index++)); CONFIG_FILE="${!index:-}" ;;
            --config=*) CONFIG_FILE="${arg#*=}" ;;
        esac
        ((index++))
    done
}

load_config() {
    [[ -f "$CONFIG_FILE" ]] || return 0
    # The config is a trusted local shell file, retained for backward compatibility.
    # shellcheck disable=SC1090
    source "$CONFIG_FILE"

    VERSION="${VERSION:-${ver:-}}"
    PORT="${PORT:-3306}"
    BASE_ROOT="${BASE_ROOT:-${PRE_BASEDIR:-/opt/mysql}}"
    DATA_ROOT="${DATA_ROOT:-${PRE_DATADIR:-/data/mysql}}"
    LOG_ROOT="${LOG_ROOT:-${PRE_LOGDIR:-/var/log/mysql}}"
    ROOT_PASSWORD="${ROOT_PASSWORD:-${MySQL_Pass:-}}"
    if [[ "${slave:-0}" == 1 && "$ROLE" == standalone ]]; then ROLE="replica"; fi
    SOURCE_HOST="${SOURCE_HOST:-${masterip:-}}"
    SOURCE_PORT="${SOURCE_PORT:-${masterport:-3306}}"
    GTID="${GTID:-${gtid:-1}}"
}

apply_secret_environment() {
    [[ -z "${AIM_ROOT_PASSWORD:-}" ]] || ROOT_PASSWORD="$AIM_ROOT_PASSWORD"
    [[ -z "${AIM_REPL_PASSWORD:-}" ]] || REPL_PASSWORD="$AIM_REPL_PASSWORD"
    [[ -z "${AIM_SOURCE_PASSWORD:-}" ]] || SOURCE_PASSWORD="$AIM_SOURCE_PASSWORD"
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
            --role) need_arg "$@"; ROLE="$2"; shift 2 ;;
            --role=*) ROLE="${1#*=}"; shift ;;
            -g|--gtid) GTID=1; shift ;;
            --no-gtid) GTID=0; shift ;;
            --server-id) need_arg "$@"; SERVER_ID="$2"; shift 2 ;;
            --bind-address) need_arg "$@"; BIND_ADDRESS="$2"; shift 2 ;;
            --root-password) need_arg "$@"; ROOT_PASSWORD="$2"; shift 2 ;;
            --replica-host) need_arg "$@"; REPLICA_HOST="$2"; shift 2 ;;
            --repl-user) need_arg "$@"; REPL_USER="$2"; shift 2 ;;
            --repl-password) need_arg "$@"; REPL_PASSWORD="$2"; shift 2 ;;
            --source-host) need_arg "$@"; SOURCE_HOST="$2"; shift 2 ;;
            --source-port) need_arg "$@"; SOURCE_PORT="$2"; shift 2 ;;
            --source-user) need_arg "$@"; SOURCE_USER="$2"; shift 2 ;;
            --source-password) need_arg "$@"; SOURCE_PASSWORD="$2"; shift 2 ;;
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
    local root_path
    [[ "$VERSION" =~ ^(5\.6|5\.7|8\.0|8\.4)\.[0-9]+$ ]] ||
        die "unsupported version '$VERSION'; expected an exact 5.6.x, 5.7.x, 8.0.x, or 8.4.x version"
    SERIES="${BASH_REMATCH[1]}"
    [[ "$PORT" =~ ^[0-9]+$ ]] || die "invalid port: $PORT"
    [[ "$SOURCE_PORT" =~ ^[0-9]+$ ]] || die "invalid source port: $SOURCE_PORT"
    PORT=$((10#$PORT))
    SOURCE_PORT=$((10#$SOURCE_PORT))
    (( PORT >= 1 && PORT <= 65535 )) || die "invalid port: $PORT"
    (( SOURCE_PORT >= 1 && SOURCE_PORT <= 65535 )) || die "invalid source port: $SOURCE_PORT"
    [[ "$ROLE" == master ]] && ROLE="source"
    [[ "$ROLE" == slave ]] && ROLE="replica"
    [[ "$ROLE" =~ ^(standalone|source|replica)$ ]] || die "role must be standalone, source/master, or replica/slave"
    if [[ "$ROLE" == replica ]]; then
        [[ -n "$SOURCE_HOST" ]] || die "--source-host is required for role=replica"
        [[ -n "$SOURCE_PASSWORD" ]] || die "--source-password is required for role=replica"
        (( GTID )) || die "role=replica currently requires GTID for a consistent, position-free setup"
    fi
    [[ "$REPL_USER" =~ ^[A-Za-z0-9_]+$ ]] || die "invalid replication user"
    [[ "$SOURCE_USER" =~ ^[A-Za-z0-9_]+$ ]] || die "invalid source user"
    [[ -z "$SERVER_ID" || "$SERVER_ID" =~ ^[0-9]+$ ]] || die "server-id must be numeric"
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

port_is_listening() {
    command -v ss >/dev/null 2>&1 &&
        ss -ltnH 2>/dev/null | awk '{print $4}' | grep -E "(^|:)$PORT$" >/dev/null
}

check_port_and_paths() {
    if port_is_listening; then
        die "TCP port $PORT is already listening"
    fi
    if (( REINITIALIZE && DRY_RUN )); then
        return 0
    fi
    if [[ -d "$DATADIR" ]] && find "$DATADIR" -mindepth 1 -maxdepth 1 -print -quit | grep -q .; then
        die "data directory is not empty: $DATADIR"
    fi
    [[ ! -e "$CNF_FILE" ]] || die "configuration already exists: $CNF_FILE"
}

assert_safe_child_path() {
    local path="$1" root="$2"
    [[ -n "$path" && -n "$root" && "$path" != / && "$root" != / && "$path" == "$root/"* ]] ||
        die "refusing unsafe reinitialize path: $path (expected a child of $root)"
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

verify_installation() {
    (( DRY_RUN )) && { log "validation completed; no host changes were made"; return; }
    local actual
    actual="$(mysql_root -e 'SELECT VERSION();')"
    [[ "$actual" == "$VERSION"* ]] || die "running server version mismatch: $actual"
    if [[ "$ROLE" == replica ]]; then
        if version_ge "$VERSION" 8.0.23; then mysql_root -e 'SHOW REPLICA STATUS\G'; else mysql_root -e 'SHOW SLAVE STATUS\G'; fi
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
        warn "store the displayed credentials now; AIM does not write them to disk"
    fi
}

main() {
    prescan_args "$@"
    load_config
    apply_secret_environment
    parse_args "$@"
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
    verify_installation
    summary
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
    main "$@"
fi
