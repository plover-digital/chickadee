#!/bin/bash
# Image-build service only. No GitHub registration or credentials.
set -euo pipefail
umask 077
state=/var/db/chickadee-build
mkdir -p "$state"
if [ -f "$state/status" ] && [ "$(cat "$state/status")" = PROVISIONED_OFFLINE ]; then
    exit 0
fi
exec >"$state/provision.log" 2>&1
trap 'printf "FAILED\n" > "$state/status"' ERR
printf 'PROVISIONING\n' > "$state/status"
if ! /usr/bin/id runner >/dev/null 2>&1; then
    password=$(/usr/bin/openssl rand -hex 24)
    /usr/sbin/sysadminctl -addUser runner -fullName 'Actions Runner' -UID 501 -GID 20 -home /Users/runner -shell /bin/bash -password "$password" -admin >/dev/null 2>&1
    unset password
fi
/usr/bin/id runner
mkdir -p /Users/runner
/usr/sbin/chown runner:staff /Users/runner
export DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer
/usr/bin/xcodebuild -license accept
/usr/bin/xcodebuild -runFirstLaunch
/usr/bin/xcodebuild -version > "$state/xcode-version"
/usr/bin/xcodebuild -showsdks > "$state/xcode-sdks"
/usr/bin/sw_vers > "$state/os-version"
printf 'PROVISIONED_OFFLINE\n' > "$state/status"
# This marker is not runner READY: network, runner and remaining tools are absent.
