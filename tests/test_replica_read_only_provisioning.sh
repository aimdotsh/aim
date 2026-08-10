#!/usr/bin/env bash
set -Eeuo pipefail

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
# shellcheck source=../aim.sh
source "${REPO_ROOT}/aim.sh"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

MYSQL="$TMP/mysql"
export AIM_TEST_STATE="$TMP/state"
export AIM_TEST_LOG="$TMP/sql.log"

cat >"$MYSQL" <<'EOF'
#!/usr/bin/env bash
set -u
sql=""
while (($#)); do
    if [[ "$1" == -e ]]; then
        sql="${2:-}"
        break
    fi
    shift
done
if [[ "$sql" == 'SELECT 1;' ]]; then
    if [[ -f "$AIM_TEST_STATE/root-secured" ]]; then
        [[ "${MYSQL_PWD:-}" == 'new-root-secret' ]]
    else
        [[ -z "${MYSQL_PWD:-}" ]]
    fi
    exit
fi
if [[ "$sql" == *"ALTER USER 'root'@'localhost'"* ]]; then
    touch "$AIM_TEST_STATE/root-secured"
    printf 'secure-root\n' >>"$AIM_TEST_LOG"
elif [[ "$sql" == 'SET GLOBAL super_read_only=OFF; SET GLOBAL read_only=OFF;' ]]; then
    if [[ -f "$AIM_TEST_STATE/root-secured" ]]; then
        [[ "${MYSQL_PWD:-}" == 'new-root-secret' ]]
    else
        [[ -z "${MYSQL_PWD:-}" ]]
    fi
    printf 'disable-read-only\n' >>"$AIM_TEST_LOG"
elif [[ "$sql" == *'CHANGE REPLICATION SOURCE TO'* ]]; then
    [[ "${MYSQL_PWD:-}" == 'new-root-secret' ]]
    printf 'configure-replica\n' >>"$AIM_TEST_LOG"
elif [[ "$sql" == 'SET GLOBAL read_only=ON; SET GLOBAL super_read_only=ON;' ]]; then
    [[ "${MYSQL_PWD:-}" == 'new-root-secret' ]]
    printf 'enable-read-only\n' >>"$AIM_TEST_LOG"
else
    printf 'unexpected SQL: %s\n' "$sql" >&2
    exit 1
fi
EOF
chmod 0755 "$MYSQL"
mkdir -p "$AIM_TEST_STATE"

ROLE="replica"
SERIES="8.0"
VERSION="8.0.46"
ROOT_PASSWORD="new-root-secret"
SOURCE_HOST="10.2.8.6"
SOURCE_PORT="3326"
SOURCE_USER="aim_repl"
SOURCE_PASSWORD="replication-secret"
SOCKET="$TMP/mysql.sock"
DRY_RUN=0

prepare_replica_provisioning
secure_root
configure_replica
finalize_replica_read_only

expected=$'disable-read-only\nsecure-root\nconfigure-replica\nenable-read-only'
actual="$(cat "$AIM_TEST_LOG")"
[[ "$actual" == "$expected" ]] || {
    printf 'unexpected replica provisioning order:\n%s\n' "$actual" >&2
    exit 1
}
(( REPLICA_PROVISIONING_WRITABLE == 0 )) || {
    printf 'replica provisioning writable flag was not cleared\n' >&2
    exit 1
}

prepare_replica_provisioning
restore_replica_read_only_best_effort
tail -n 2 "$AIM_TEST_LOG" | grep -qx 'disable-read-only' || {
    printf 'best-effort restoration did not begin from a writable replica state\n' >&2
    exit 1
}
tail -n 1 "$AIM_TEST_LOG" | grep -qx 'enable-read-only' || {
    printf 'best-effort restoration did not re-enable replica protection\n' >&2
    exit 1
}
(( REPLICA_PROVISIONING_WRITABLE == 0 )) || {
    printf 'best-effort restoration left the writable flag set\n' >&2
    exit 1
}

SERIES="5.6"
VERSION="5.6.51"
[[ "$(replica_read_only_sql 0)" == 'SET GLOBAL read_only=OFF;' ]]
[[ "$(replica_read_only_sql 1)" == 'SET GLOBAL read_only=ON;' ]]

printf 'replica provisioning temporarily relaxes and restores super-read-only: ok\n'
