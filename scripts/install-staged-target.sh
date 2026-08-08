#!/usr/bin/env bash

set -Eeuo pipefail
umask 027

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

[[ $EUID -eq 0 ]] || { printf 'run with sudo or as root\n' >&2; exit 1; }

for required_file in aim_console_ed25519.pub aim.sh router.sh aim-executor bootstrap-target.sh; do
    [[ -f "$SCRIPT_DIR/$required_file" ]] || { printf 'missing staged file: %s\n' "$required_file" >&2; exit 1; }
done

chmod 0755 "$SCRIPT_DIR/aim.sh" "$SCRIPT_DIR/router.sh" "$SCRIPT_DIR/aim-executor" "$SCRIPT_DIR/bootstrap-target.sh"

exec "$SCRIPT_DIR/bootstrap-target.sh" \
    --public-key "$SCRIPT_DIR/aim_console_ed25519.pub" \
    --aim-script "$SCRIPT_DIR/aim.sh" \
    --router-script "$SCRIPT_DIR/router.sh" \
    --executor "$SCRIPT_DIR/aim-executor"
