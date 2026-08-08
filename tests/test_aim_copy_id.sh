#!/usr/bin/env bash

set -Eeuo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
cleanup() { rm -r -- "$TMP"; }
trap cleanup EXIT

mkdir -p "$TMP/bin" "$TMP/payload" "$TMP/home"
printf 'executor' >"$TMP/payload/aim-executor-linux-amd64"
mkdir -p "$TMP/standalone/scripts"
cp "$ROOT/scripts/aim-copy-id" "$TMP/standalone/aim-copy-id"
cp "$ROOT/aim.sh" "$TMP/standalone/aim.sh"
cp "$ROOT/router.sh" "$TMP/standalone/router.sh"
cp "$ROOT/scripts/bootstrap-target.sh" "$ROOT/scripts/install-staged-target.sh" "$TMP/standalone/scripts/"

cat >"$TMP/bin/ssh" <<'EOF'
#!/usr/bin/env bash
printf 'ssh %s\n' "$*" >>"$AIM_COPY_TEST_LOG"
for argument in "$@"; do
    if [[ "$argument" == ControlPath=* ]]; then
        control_path="${argument#ControlPath=}"
        (( ${#control_path} < 104 )) || { printf 'test ControlPath is too long\n' >&2; exit 1; }
    fi
done
if [[ "$*" == *"uname -m"* ]]; then
    printf 'x86_64\n'
fi
EOF

cat >"$TMP/bin/scp" <<'EOF'
#!/usr/bin/env bash
printf 'scp %s\n' "$*" >>"$AIM_COPY_TEST_LOG"
EOF

chmod 0755 "$TMP/bin/ssh" "$TMP/bin/scp"

OUTPUT="$(
    HOME="$TMP/home" \
    PATH="$TMP/bin:$PATH" \
    AIM_COPY_TEST_LOG="$TMP/calls.log" \
    AIM_HOST_KIT_PAYLOAD_DIR="$TMP/payload" \
    bash "$TMP/standalone/aim-copy-id" --key "$TMP/home/aim-key" root@192.168.1.100
)"

[[ -s "$TMP/home/aim-key" && -s "$TMP/home/aim-key.pub" ]]
grep -q 'aim-executor-linux-amd64' "$TMP/calls.log"
grep -q 'aim_console_ed25519.pub' "$TMP/calls.log"
grep -q 'router.sh' "$TMP/calls.log"
if grep -q "mv 'aim_console_ed25519.pub' aim_console_ed25519.pub" "$TMP/calls.log"; then
    printf 'default public key name must not be moved onto itself\n' >&2
    exit 1
fi
grep -q 'sudo.*install-staged-target.sh' <<<"$OUTPUT"
grep -q 'AIM private key for the web form' <<<"$OUTPUT"

printf 'aim-copy-id staged onboarding flow: ok\n'
