#!/usr/bin/env bash
# AIM restricted MySQL Router installer and InnoDB Cluster adoption helper.

set -Eeuo pipefail
umask 027

readonly TOOL_VERSION="8.0.46"
MYSQL_VERSION=""
MYSQL_PORT=""
CLUSTER_NAME=""
BIND_ADDRESS=""
RW_PORT=""
ADMIN_USER=""
ADMIN_HOSTS=""
BASE_ROOT="/opt/mysql"
DATA_ROOT="/data/mysql"
ADOPT=0

log() { printf '[aim-router] %s\n' "$*"; }
die() { printf '[aim-router] ERROR: %s\n' "$*" >&2; exit 1; }
need_arg() { [[ $# -ge 2 && -n "$2" ]] || die "option $1 requires a value"; }

while (( $# )); do
    case "$1" in
        --mysql-version) need_arg "$@"; MYSQL_VERSION="$2"; shift 2 ;;
        --mysql-port) need_arg "$@"; MYSQL_PORT="$2"; shift 2 ;;
        --cluster-name) need_arg "$@"; CLUSTER_NAME="$2"; shift 2 ;;
        --bind-address) need_arg "$@"; BIND_ADDRESS="$2"; shift 2 ;;
        --rw-port) need_arg "$@"; RW_PORT="$2"; shift 2 ;;
        --admin-user) need_arg "$@"; ADMIN_USER="$2"; shift 2 ;;
        --admin-hosts) need_arg "$@"; ADMIN_HOSTS="$2"; shift 2 ;;
        --base-root) need_arg "$@"; BASE_ROOT="$2"; shift 2 ;;
        --data-root) need_arg "$@"; DATA_ROOT="$2"; shift 2 ;;
        --adopt) ADOPT=1; shift ;;
        *) die "unsupported option: $1" ;;
    esac
done

[[ $EUID -eq 0 ]] || die "restricted executor must run this helper as root"
[[ "$MYSQL_VERSION" =~ ^8\.0\.[0-9]+$ ]] || die "Router requires an exact MySQL 8.0 version"
[[ "$MYSQL_PORT" =~ ^[0-9]+$ && "$RW_PORT" =~ ^[0-9]+$ ]] || die "ports must be numeric"
MYSQL_PORT=$((10#$MYSQL_PORT))
RW_PORT=$((10#$RW_PORT))
(( MYSQL_PORT >= 1 && MYSQL_PORT <= 65535 && RW_PORT >= 1 && RW_PORT <= 65532 )) || die "port is out of range"
[[ "$CLUSTER_NAME" =~ ^[A-Za-z][A-Za-z0-9_-]{0,62}$ ]] || die "invalid cluster name"
[[ "$BIND_ADDRESS" =~ ^[0-9A-Fa-f:.]+$ ]] || die "invalid bind address"
[[ "$ADMIN_USER" =~ ^[A-Za-z0-9_]{1,32}$ ]] || die "invalid administrator user"
[[ "$ADMIN_HOSTS" =~ ^[0-9A-Fa-f:.,]+$ ]] || die "invalid administrator host list"
[[ -n "${AIM_MGR_ADMIN_PASSWORD:-}" ]] || die "AIM_MGR_ADMIN_PASSWORD is required"
[[ "$BASE_ROOT" == /* && "$BASE_ROOT" != / && "$DATA_ROOT" == /* && "$DATA_ROOT" != / ]] || die "invalid MySQL roots"

IFS=',' read -r -a ADMIN_HOST_LIST <<<"$ADMIN_HOSTS"
(( ${#ADMIN_HOST_LIST[@]} == 3 )) || die "exactly three administrator source IPs are required"
METADATA_HOST="${ADMIN_HOST_LIST[0]}"
RO_PORT=$((RW_PORT + 1))
X_RW_PORT=$((RW_PORT + 2))
X_RO_PORT=$((RW_PORT + 3))

MYSQL="${BASE_ROOT}/${MYSQL_VERSION}/bin/mysql"
[[ -x "$MYSQL" ]] || die "managed MySQL client is missing: $MYSQL"
SOCKET="${DATA_ROOT}/${MYSQL_PORT}/mysql.sock"
[[ -S "$SOCKET" ]] || die "managed MySQL socket is missing: $SOCKET"

case "$(uname -m)" in
    x86_64|amd64)
        ROUTER_ARCHIVE="mysql-router-${TOOL_VERSION}-linux-glibc2.17-x86_64-minimal.tar.xz"
        ROUTER_MD5="b0599a14342a2a48df0de7050faed13f"
        SHELL_ARCHIVE="mysql-shell-${TOOL_VERSION}-linux-glibc2.17-x86-64bit.tar.gz"
        SHELL_MD5="04cb490e587f66c861a98d190dc420c2"
        ;;
    aarch64|arm64)
        ROUTER_ARCHIVE="mysql-router-${TOOL_VERSION}-linux-glibc2.28-aarch64.tar.xz"
        ROUTER_MD5="4d09c107104cb544b3b1020e1e6d424b"
        SHELL_ARCHIVE="mysql-shell-${TOOL_VERSION}-linux-glibc2.28-arm-64bit.tar.gz"
        SHELL_MD5="785517641a41795bce6df66f7b8ab1af"
        ;;
    *) die "unsupported Router architecture: $(uname -m)" ;;
esac

CACHE_ROOT="/var/cache/aim"
TOOLS_ROOT="/opt/aim-tools"
ROUTER_HOME="${TOOLS_ROOT}/mysql-router-${TOOL_VERSION}"
SHELL_HOME="${TOOLS_ROOT}/mysql-shell-${TOOL_VERSION}"
INSTANCE_ROOT="/var/lib/aim-mysqlrouter/${RW_PORT}"
SERVICE_NAME="aim-mysqlrouter-${RW_PORT}"

md5_file() {
    if command -v md5sum >/dev/null 2>&1; then
        md5sum "$1" | awk '{print $1}'
    elif command -v md5 >/dev/null 2>&1; then
        md5 -q "$1"
    else
        die "md5sum is required to verify Oracle packages"
    fi
}

download_verified() {
    local product="$1" filename="$2" expected="$3" destination
    destination="${CACHE_ROOT}/${filename}"
    if [[ -f "$destination" && "$(md5_file "$destination")" == "$expected" ]]; then
        printf '%s' "$destination"
        return
    fi
    command -v curl >/dev/null 2>&1 || die "curl is required to download Oracle packages"
    rm -f -- "$destination.part"
    log "downloading official ${filename}" >&2
    curl --fail --location --retry 3 --connect-timeout 20 \
        --output "$destination.part" \
        "https://cdn.mysql.com/archives/${product}/${filename}"
    [[ "$(md5_file "$destination.part")" == "$expected" ]] || {
        rm -f -- "$destination.part"
        die "Oracle package checksum mismatch: $filename"
    }
    mv -- "$destination.part" "$destination"
    printf '%s' "$destination"
}

normalize_tool_permissions() {
    local destination="$1"
    [[ -d "$destination" ]] || die "tool directory is missing: $destination"
    chown -R root:root "$destination"
    chmod -R u=rwX,go=rX "$destination"
}

extract_tool() {
    local archive="$1" destination="$2" expected_binary="$3" temp root entry
    if [[ -x "$destination/$expected_binary" ]]; then
        normalize_tool_permissions "$destination"
        return
    fi
    temp="$(mktemp -d /var/tmp/aim-tool.XXXXXX)"
    while IFS= read -r entry; do
        [[ "$entry" != /* && "/$entry/" != *"/../"* ]] || { rm -rf -- "$temp"; die "unsafe path in $(basename -- "$archive")"; }
    done < <(tar -tf "$archive")
    tar -xf "$archive" -C "$temp"
    root="$(find "$temp" -mindepth 1 -maxdepth 1 -type d | head -n 1)"
    [[ -n "$root" && -x "$root/$expected_binary" ]] || { rm -rf -- "$temp"; die "archive lacks $expected_binary"; }
    rm -rf -- "$destination"
    mv -- "$root" "$destination"
    rm -rf -- "$temp"
    normalize_tool_permissions "$destination"
}

install -d -o root -g root -m 0755 "$CACHE_ROOT" "$TOOLS_ROOT"
ROUTER_PACKAGE="$(download_verified mysql-router "$ROUTER_ARCHIVE" "$ROUTER_MD5")"
extract_tool "$ROUTER_PACKAGE" "$ROUTER_HOME" "bin/mysqlrouter"

if (( ADOPT )); then
    SHELL_PACKAGE="$(download_verified mysql-shell "$SHELL_ARCHIVE" "$SHELL_MD5")"
    extract_tool "$SHELL_PACKAGE" "$SHELL_HOME" "bin/mysqlsh"
fi

for binary in "$ROUTER_HOME/bin/mysqlrouter"; do
    missing="$(ldd "$binary" 2>/dev/null | awk '/not found/ {print $1}' | paste -sd, -)"
    [[ -z "$missing" ]] || die "Router runtime libraries are missing: $missing"
done

MYSQL_ARGS=(--host="$METADATA_HOST" --port="$MYSQL_PORT" --user="$ADMIN_USER" --batch --skip-column-names)
members="$(MYSQL_PWD="$AIM_MGR_ADMIN_PASSWORD" "$MYSQL" "${MYSQL_ARGS[@]}" -e \
    "SELECT COUNT(*) FROM performance_schema.replication_group_members WHERE MEMBER_STATE='ONLINE';")"
[[ "$members" == 3 ]] || die "Router deployment requires three ONLINE MGR members; found ${members:-0}"

if (( ADOPT )); then
    metadata="$(MYSQL_PWD="$AIM_MGR_ADMIN_PASSWORD" "$MYSQL" "${MYSQL_ARGS[@]}" -e \
        "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name='mysql_innodb_cluster_metadata';")"
    if [[ "$metadata" == 0 ]]; then
        log "adopting Group Replication as InnoDB Cluster ${CLUSTER_NAME}"
        printf '%s\n' "$AIM_MGR_ADMIN_PASSWORD" | "$SHELL_HOME/bin/mysqlsh" \
            --mysql --host="$METADATA_HOST" --port="$MYSQL_PORT" --user="$ADMIN_USER" \
            --password --passwords-from-stdin --js --execute="dba.createCluster('${CLUSTER_NAME}', {adoptFromGR: true});"
    else
        log "InnoDB Cluster metadata already exists; validating ${CLUSTER_NAME}"
        printf '%s\n' "$AIM_MGR_ADMIN_PASSWORD" | "$SHELL_HOME/bin/mysqlsh" \
            --mysql --host="$METADATA_HOST" --port="$MYSQL_PORT" --user="$ADMIN_USER" \
            --password --passwords-from-stdin --js --execute="dba.getCluster('${CLUSTER_NAME}').status();"
    fi
fi

for attempt in {1..60}; do
    metadata="$(MYSQL_PWD="$AIM_MGR_ADMIN_PASSWORD" "$MYSQL" "${MYSQL_ARGS[@]}" -e \
        "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name='mysql_innodb_cluster_metadata';" 2>/dev/null || true)"
    [[ "$metadata" == 1 ]] && break
    sleep 2
done
[[ "${metadata:-0}" == 1 ]] || die "InnoDB Cluster metadata was not available within 120 seconds"

port_open() {
    (exec 3<>"/dev/tcp/${BIND_ADDRESS}/$1") 2>/dev/null
}

if [[ -f "$INSTANCE_ROOT/mysqlrouter.conf" ]] && port_open "$RW_PORT" && port_open "$RO_PORT" && port_open "$X_RW_PORT" && port_open "$X_RO_PORT"; then
    log "Router is already online and all four listeners were verified"
    exit 0
fi

if ! id mysqlrouter >/dev/null 2>&1; then
    useradd --system --home-dir /nonexistent --shell /usr/sbin/nologin mysqlrouter 2>/dev/null || \
        useradd -r -d /nonexistent -s /sbin/nologin mysqlrouter
fi

if command -v systemctl >/dev/null 2>&1; then
    systemctl stop "$SERVICE_NAME" >/dev/null 2>&1 || true
fi
if [[ -x "$INSTANCE_ROOT/stop.sh" ]]; then
    runuser -u mysqlrouter -- "$INSTANCE_ROOT/stop.sh" >/dev/null 2>&1 || true
fi
install -d -o mysqlrouter -g mysqlrouter -m 0750 "$(dirname -- "$INSTANCE_ROOT")" "$INSTANCE_ROOT"
log "bootstrapping Router at ${BIND_ADDRESS}:${RW_PORT}"
printf '%s\n' "$AIM_MGR_ADMIN_PASSWORD" | "$ROUTER_HOME/bin/mysqlrouter" \
    --bootstrap "${ADMIN_USER}@${METADATA_HOST}:${MYSQL_PORT}" \
    --directory "$INSTANCE_ROOT" \
    --conf-base-port "$RW_PORT" \
    --conf-bind-address "$BIND_ADDRESS" \
    --name "aim-${CLUSTER_NAME}-${RW_PORT}" \
    --user mysqlrouter --disable-rest --strict --force

[[ -f "$INSTANCE_ROOT/mysqlrouter.conf" ]] || die "Router bootstrap did not create mysqlrouter.conf"
chown -R mysqlrouter:mysqlrouter "$INSTANCE_ROOT"

if command -v systemctl >/dev/null 2>&1 && [[ -d /run/systemd/system ]]; then
    cat >"/etc/systemd/system/${SERVICE_NAME}.service" <<EOF
[Unit]
Description=AIM MySQL Router ${RW_PORT}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=mysqlrouter
Group=mysqlrouter
ExecStart=${ROUTER_HOME}/bin/mysqlrouter -c ${INSTANCE_ROOT}/mysqlrouter.conf
Restart=on-failure
RestartSec=3
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
EOF
    systemctl daemon-reload
    systemctl enable --now "$SERVICE_NAME"
else
    runuser -u mysqlrouter -- "$ROUTER_HOME/bin/mysqlrouter" -c "$INSTANCE_ROOT/mysqlrouter.conf" >/dev/null 2>&1 &
fi

for attempt in {1..60}; do
    if port_open "$RW_PORT"; then
        break
    fi
    sleep 1
done
if ! port_open "$RW_PORT"; then
    if command -v systemctl >/dev/null 2>&1 && [[ -d /run/systemd/system ]]; then
        log "Router service diagnostics follow"
        systemctl show "$SERVICE_NAME" \
            -p ActiveState -p SubState -p MainPID -p ExecMainCode -p ExecMainStatus -p NRestarts \
            --no-pager >&2 || true
        journalctl -u "$SERVICE_NAME" -n 30 --no-pager -o short-iso >&2 || true
    fi
    die "Router read/write port did not become ready"
fi
port_open "$RO_PORT" || die "Router read-only port did not become ready"
port_open "$X_RW_PORT" || die "Router X read/write port did not become ready"
port_open "$X_RO_PORT" || die "Router X read-only port did not become ready"

log "Router is online: Classic RW ${RW_PORT}, RO ${RO_PORT}; X RW ${X_RW_PORT}, RO ${X_RO_PORT}"
