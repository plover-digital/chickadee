#!/bin/bash
# Acceptance for the Ubuntu26 medium developer profile, never the minimal image.
set -euo pipefail
. /etc/os-release
[[ $ID == ubuntu && $VERSION_ID == 26.04 ]]
[[ $(id -u) == 1000 && $(nproc) == 4 ]]
[[ $RUNNER_TOOL_CACHE == /opt/hostedtoolcache ]]
[[ -n ${CURRENT_RUNNER:-} ]]
if [[ ${EXPECT_REPLACEMENT:-0} == 1 ]]; then
  [[ -n ${PREVIOUS_RUNNER:-} ]]
  [[ $CURRENT_RUNNER != "$PREVIOUS_RUNNER" ]]
fi
# This marker and the native tools distinguish developer from minimal.
python3 - <<'PY'
import json
with open('/etc/chickadee/image-compatibility.json') as stream:
    report = json.load(stream)
assert report['installed_toolcache'], 'missing developer toolcache provenance'
PY
for tool in go node npm python3 gcc clang-21 java docker; do command -v "$tool"; done
go version | grep 'go1.27.2'
node --version | grep -Fx v24.21.0
sudo -n true
python3 scripts/isolation-probe.py --cpus 4 --memory-mib 8192
# Only this explicit developer profile allows a guest-local Docker socket.
# Reject a file/directory bind mount or shared filesystem at the socket.
test -S /var/run/docker.sock
source=$(findmnt -n -o SOURCE --target /var/run/docker.sock)
target=$(findmnt -n -o TARGET --target /var/run/docker.sock)
filesystem=$(findmnt -n -o FSTYPE --target /var/run/docker.sock)
case "$source:$target:$filesystem" in
  /dev/vda*:/:ext4|/dev/sda*:/:ext4|tmpfs:/run:tmpfs) ;;
  *) echo 'Docker socket is not on the guest root or runtime filesystem'; exit 1 ;;
esac
# An environment override or Docker context must not redirect this probe.
docker --host unix:///var/run/docker.sock version
# Correlate these with private worker records and immutable bundle checksums.
uname -r
sha256sum /usr/local/bin/chickadee-guest
printf 'runner=%s\n' "$CURRENT_RUNNER" >> "$GITHUB_OUTPUT"
printf 'developer runner accepted: %s\n' "$CURRENT_RUNNER"
