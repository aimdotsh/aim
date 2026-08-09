#!/usr/bin/env bash

set -Eeuo pipefail

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"

# shellcheck disable=SC1091
source "${REPO_ROOT}/aim.sh"

for distro in rhel fedora centos rocky almalinux opencloudos ol; do
    [[ "$(classify_os_family "$distro" "$distro")" == rhel ]]
done

[[ "$(classify_os_family opencloudos opencloudos)" == rhel ]]
[[ "$(classify_os_family ubuntu debian)" == debian ]]
[[ "$(classify_os_family debian debian)" == debian ]]
[[ "$(classify_os_family opensuse suse)" == suse ]]
[[ "$(classify_os_family sles suse)" == suse ]]

if classify_os_family unknown unknown >/dev/null 2>&1; then
    printf 'unknown distribution unexpectedly matched a supported family\n' >&2
    exit 1
fi

printf 'OpenCloudOS and supported Linux family classification: ok\n'
