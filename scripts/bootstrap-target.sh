#!/usr/bin/env bash

set -Eeuo pipefail
umask 027

PUBLIC_KEY_FILE=""
AIM_SCRIPT="./aim.sh"
EXECUTOR_BINARY="./aim-executor"
AIM_USER="aimops"

usage() {
    cat <<'EOF'
Usage:
  sudo ./scripts/bootstrap-target.sh --public-key /path/to/aim_console.pub \
      [--aim-script ./aim.sh] [--executor ./aim-executor]

Installs the non-daemon AIM restricted executor and creates the dedicated aimops SSH user.
EOF
}

need_arg() { [[ $# -ge 2 && -n "$2" ]] || { printf 'missing value for %s\n' "$1" >&2; exit 2; }; }

while (( $# )); do
    case "$1" in
        --public-key) need_arg "$@"; PUBLIC_KEY_FILE="$2"; shift 2 ;;
        --aim-script) need_arg "$@"; AIM_SCRIPT="$2"; shift 2 ;;
        --executor) need_arg "$@"; EXECUTOR_BINARY="$2"; shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) printf 'unknown option: %s\n' "$1" >&2; usage >&2; exit 2 ;;
    esac
done

[[ $EUID -eq 0 ]] || { printf 'run as root\n' >&2; exit 1; }
[[ -n "$PUBLIC_KEY_FILE" && -r "$PUBLIC_KEY_FILE" ]] || { printf 'a readable --public-key is required\n' >&2; exit 1; }
[[ -r "$AIM_SCRIPT" ]] || { printf 'aim.sh is not readable: %s\n' "$AIM_SCRIPT" >&2; exit 1; }
[[ -x "$EXECUTOR_BINARY" ]] || { printf 'aim-executor is not executable: %s\n' "$EXECUTOR_BINARY" >&2; exit 1; }
command -v sudo >/dev/null 2>&1 || { printf 'sudo is required\n' >&2; exit 1; }
command -v visudo >/dev/null 2>&1 || { printf 'visudo is required\n' >&2; exit 1; }

read -r PUBLIC_KEY <"$PUBLIC_KEY_FILE"
[[ "$PUBLIC_KEY" =~ ^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521))[[:space:]]+[A-Za-z0-9+/=]+([[:space:]].*)?$ ]] || {
    printf 'unsupported or malformed SSH public key\n' >&2
    exit 1
}

if ! id "$AIM_USER" >/dev/null 2>&1; then
    useradd --create-home --shell /bin/bash "$AIM_USER"
fi
AIM_HOME="$(getent passwd "$AIM_USER" | awk -F: '{print $6}')"
[[ -n "$AIM_HOME" && "$AIM_HOME" == /* ]] || { printf 'cannot determine aimops home directory\n' >&2; exit 1; }

install -d -o "$AIM_USER" -g "$AIM_USER" -m 0700 "$AIM_HOME/.ssh"
touch "$AIM_HOME/.ssh/authorized_keys"
chown "$AIM_USER:$AIM_USER" "$AIM_HOME/.ssh/authorized_keys"
chmod 0600 "$AIM_HOME/.ssh/authorized_keys"
AUTHORIZED_KEY="restrict ${PUBLIC_KEY}"
if ! grep -Fqx -- "$AUTHORIZED_KEY" "$AIM_HOME/.ssh/authorized_keys"; then
    printf '%s\n' "$AUTHORIZED_KEY" >>"$AIM_HOME/.ssh/authorized_keys"
fi

install -d -o root -g root -m 0755 /opt/aim /etc/aim
install -d -o "$AIM_USER" -g "$AIM_USER" -m 0750 /var/lib/aim-staging
install -o root -g root -m 0755 "$AIM_SCRIPT" /opt/aim/aim.sh
install -o root -g root -m 0755 "$EXECUTOR_BINARY" /usr/local/sbin/aim-executor

CONFIG_TMP="$(mktemp /etc/aim/executor.json.XXXXXX)"
SUDOERS_TMP="$(mktemp /etc/sudoers.d/aim-executor.XXXXXX)"
cleanup() {
    rm -f -- "$CONFIG_TMP" "$SUDOERS_TMP"
}
trap cleanup EXIT

cat >"$CONFIG_TMP" <<'EOF'
{
  "aim_path": "/opt/aim/aim.sh",
  "base_root": "/opt/mysql",
  "data_root": "/data/mysql",
  "log_root": "/var/log/mysql",
  "tmp_root": "/var/tmp/mysql",
  "staging_root": "/var/lib/aim-staging"
}
EOF
install -o root -g root -m 0600 "$CONFIG_TMP" /etc/aim/executor.json

printf '%s ALL=(root) NOPASSWD: /usr/local/sbin/aim-executor ""\n' "$AIM_USER" >"$SUDOERS_TMP"
chmod 0440 "$SUDOERS_TMP"
visudo -cf "$SUDOERS_TMP" >/dev/null
install -o root -g root -m 0440 "$SUDOERS_TMP" /etc/sudoers.d/aim-executor

printf 'AIM target bootstrap complete.\n'
printf '  SSH user:       %s\n' "$AIM_USER"
printf '  executor:       /usr/local/sbin/aim-executor\n'
printf '  aim.sh:         /opt/aim/aim.sh\n'
printf '  staging:        /var/lib/aim-staging\n'
printf 'The executor is non-daemon and runs only through an authenticated SSH task.\n'
