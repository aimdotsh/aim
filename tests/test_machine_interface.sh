#!/usr/bin/env bash

set -Eeuo pipefail

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"

status_output="$(bash "${REPO_ROOT}/aim.sh" --status -v 8.0.46 -p 65000 \
    --base-root /tmp/aim-test/opt --data-root /tmp/aim-test/data \
    --log-root /tmp/aim-test/log --tmp-root /tmp/aim-test/tmp \
    --dry-run --machine-readable --no-print-secrets)"
[[ "$status_output" == *'"ok":true'* ]]
[[ "$status_output" == *'"action":"status"'* ]]
[[ "$status_output" == *'"state":"missing"'* ]]

if bash "${REPO_ROOT}/aim.sh" --status --start -v 8.0.46 -p 65000 --dry-run >/dev/null 2>&1; then
    printf 'conflicting lifecycle actions were accepted\n' >&2
    exit 1
fi

# shellcheck disable=SC1091
source "${REPO_ROOT}/aim.sh"
VERSION="8.0.46"
ROLE="source"
PORT=65000
SERVICE_NAME="aim-mysql-65000"
CNF_FILE="/tmp/aim-test/data/65000/my.cnf"
BASEDIR="/tmp/aim-test/opt/8.0.46"
DATADIR="/tmp/aim-test/data/65000/data"
SOCKET="/tmp/aim-test/data/65000/mysql.sock"
ROOT_PASSWORD="root-secret-must-not-leak"
REPL_PASSWORD="replication-secret-must-not-leak"
DRY_RUN=0
PRINT_SECRETS=0
MACHINE_READABLE=1

summary_output="$(summary 2>&1)"
[[ "$summary_output" != *"$ROOT_PASSWORD"* ]]
[[ "$summary_output" != *"$REPL_PASSWORD"* ]]
[[ "$summary_output" == *'"ok":true'* ]]

printf 'machine lifecycle interface and secret suppression: ok\n'
