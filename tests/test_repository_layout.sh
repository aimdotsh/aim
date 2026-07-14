#!/usr/bin/env bash

set -Eeuo pipefail

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$REPO_ROOT"

required_files=(
    cmd/aim-console/main.go
    cmd/aim-executor/main.go
    internal/webui/embed.go
    internal/webui/dist/index.html
)

for path in "${required_files[@]}"; do
    [[ -f "$path" ]] || {
        printf 'required build input is missing: %s\n' "$path" >&2
        exit 1
    }
    git check-ignore -q "$path" && {
        printf 'required build input is ignored by Git: %s\n' "$path" >&2
        exit 1
    }
done

printf 'repository build inputs are present and not ignored: ok\n'
