#!/usr/bin/env bash

set -Eeuo pipefail

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
# shellcheck disable=SC1091
source "${REPO_ROOT}/aim.sh"

ROLE="mgr"
DRY_RUN=0
MGR_BOOTSTRAP=1
MGR_GROUP_NAME="b32b3ad1-031b-4c53-bfd4-1ea75424021a"
MGR_LOCAL_ADDRESS="172.20.23.184"
MGR_PORT=33061
MGR_RECOVERY_USER="aim_mgr"
MGR_RECOVERY_PASSWORD="shared-test-secret"

mysql_root() {
    local sql=""
    if (( $# == 0 )); then
        sql="$(command cat)"
        [[ "$sql" == *"FOR CHANNEL 'group_replication_recovery'"* ]] || {
            printf 'missing group_replication_recovery channel SQL\n' >&2
            return 1
        }
        [[ "$sql" != *"GET_SOURCE_PUBLIC_KEY"* ]] || {
            printf 'GET_SOURCE_PUBLIC_KEY is invalid for group_replication_recovery\n' >&2
            return 1
        }
        return
    fi

    case "${2:-}" in
        *"COUNT(*)"*) printf '0\n' ;;
        *"MEMBER_ID=@@server_uuid"*) printf 'ONLINE\n' ;;
        *"MEMBER_HOST"*) printf '172.20.23.184\t8046\tONLINE\tPRIMARY\t8.0.46\n' ;;
    esac
}

configure_mgr >/dev/null
printf 'MGR recovery channel SQL: ok\n'
