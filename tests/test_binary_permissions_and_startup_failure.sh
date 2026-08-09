#!/usr/bin/env bash

set -Eeuo pipefail

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"

# shellcheck disable=SC1091
source "${REPO_ROOT}/aim.sh"

tmp="$(mktemp -d /tmp/aim-binary-permissions-test.XXXXXX)"
cleanup() { rm -rf -- "$tmp"; }
trap cleanup EXIT

BASEDIR="$tmp/opt/mysql/8.0.46"
MYSQL_USER="mysql"
MYSQL_GROUP="mysql"
DRY_RUN=0
mkdir -p "$BASEDIR/bin"
touch "$BASEDIR/bin/mysqld"
chmod 0755 "$BASEDIR/bin" "$BASEDIR/bin/mysqld"
chmod 0750 "$BASEDIR"

normalized=0
chown() {
    [[ "$1" == -R && "$2" == "root:$MYSQL_GROUP" && "$3" == "$BASEDIR" ]]
    normalized=1
}
runuser() {
    [[ "$1" == -u && "$2" == "$MYSQL_USER" && "$3" == -- && "$4" == test && "$5" == -x && "$6" == "$BASEDIR/bin/mysqld" ]]
    (( normalized ))
}

secure_mysql_binary_tree
(( normalized )) || {
    printf 'reused binary tree ownership was not normalized\n' >&2
    exit 1
}

MYSQLADMIN="$tmp/mysqladmin"
SOCKET="$tmp/mysql.sock"
LOGDIR="$tmp/log"
SERVICE_NAME="aim-mysql-3319"
MYSQL_START_TIMEOUT=300
mkdir -p "$LOGDIR"
printf 'initialization-only log\n' >"$LOGDIR/error.log"
cat >"$MYSQLADMIN" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
chmod 0755 "$MYSQLADMIN"

systemctl() {
    case "$1" in
        is-failed) return 0 ;;
        status) printf 'status=203/EXEC permission denied\n' >&2; return 3 ;;
        *) return 1 ;;
    esac
}
systemd_runtime_available() { return 0; }
journalctl() { printf 'Failed to execute mysqld: Permission denied\n' >&2; }
sleep() {
    printf 'wait_for_mysql slept instead of failing fast\n' >&2
    return 99
}

trap - ERR
set +e
diagnostics="$(wait_for_mysql 2>&1)"
rc=$?
set -e
trap on_error ERR
[[ "$rc" == 1 ]]
[[ "$diagnostics" == *'status=203/EXEC permission denied'* ]]
[[ "$diagnostics" == *'Failed to execute mysqld: Permission denied'* ]]
[[ "$diagnostics" == *'failed during startup'* ]]
[[ "$diagnostics" != *'wait_for_mysql slept instead of failing fast'* ]]

printf 'binary-tree repair and systemd startup failure diagnostics: ok\n'
