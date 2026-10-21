#!/bin/bash
# Acceptance for Ubuntu developer profiles, never a minimal image.
# Defaults preserve the existing Ubuntu26 medium workflow and direct invocation.
set -euo pipefail
case ${UBUNTU_VERSION:-26.04} in
  24.04) expected_os=24.04; expected_node=v22.23.3; expected_clang=clang-18 ;;
  26.04) expected_os=26.04; expected_node=v24.21.0; expected_clang=clang-21 ;;
  *) echo 'Unsupported Ubuntu developer profile' >&2; exit 1 ;;
esac
case ${PROFILE_SIZE:-medium} in
  small) expected_cpus=2; expected_memory=4096 ;;
  medium) expected_cpus=4; expected_memory=8192 ;;
  *) echo 'Unsupported developer profile size' >&2; exit 1 ;;
esac
if [[ ${REQUIRE_KERNEL:-0} == 1 || -n ${EXPECTED_KERNEL:-} ]]; then
  [[ ${EXPECTED_KERNEL:-} =~ ^[0-9]+\.[0-9]+\.[0-9]+-[0-9]+-generic$ ]]
fi
# Compare resolved executables, including the alternatives/symlink chain.
assert_default_executable() {
  local selected
  selected=$(command -v "$1")
  [[ -x "$selected" && -x "$2" ]]
  [[ $(readlink -e "$selected") == $(readlink -e "$2") ]]
}
. /etc/os-release
[[ $ID == ubuntu && $VERSION_ID == "$expected_os" ]]
[[ $(id -u) == 1000 && $(nproc) == "$expected_cpus" ]]
if [[ -n ${EXPECTED_KERNEL:-} ]]; then
  [[ $(uname -r) == "$EXPECTED_KERNEL" ]]
fi
[[ $RUNNER_TOOL_CACHE == /opt/hostedtoolcache ]]
[[ $AGENT_TOOLSDIRECTORY == /opt/hostedtoolcache ]]
[[ $JAVA_HOME == /usr/lib/jvm/java-17-openjdk-amd64 ]]
"$JAVA_HOME/bin/java" -version
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
for tool in git git-lfs unzip tar gzip zstd jq go node npm python3 gcc g++ "$expected_clang" cmake ninja java mvn ruby php composer docker podman buildah skopeo cargo rustc rustup dotnet firefox google-chrome geckodriver; do command -v "$tool"; done
# Source package versions and every installed cohort member must be fixed.
# PHP CLI can also serve untrusted HTTP; command presence is not acceptance.
python3 scripts/check-php-security.py
python3 scripts/check-independent-trust.py
assert_default_executable clang "/usr/bin/$expected_clang"
assert_default_executable java "$JAVA_HOME/bin/java"
clang --version
java -version
test "$(go env GOVERSION)" = go1.27.3
test "$(node --version)" = "$expected_node"
for version in 1.26.10 1.27.3; do
  test -f "$RUNNER_TOOL_CACHE/go/$version/x64.complete"
  test "$(GOTOOLCHAIN=local "$RUNNER_TOOL_CACHE/go/$version/x64/bin/go" env GOVERSION)" = "go$version"
done
for version in 22.23.3 24.21.0; do
  test -f "$RUNNER_TOOL_CACHE/node/$version/x64.complete"
  test "$("$RUNNER_TOOL_CACHE/node/$version/x64/bin/node" --version)" = "v$version"
done
for version in 3.10.21 3.11.16 3.12.14 3.13.15 3.14.7; do
  test -f "$RUNNER_TOOL_CACHE/Python/$version/x64.complete"
  "$RUNNER_TOOL_CACHE/Python/$version/x64/bin/python3" -c 'import ssl,sqlite3,ctypes,zlib,pip'
done
sudo -n true
python3 scripts/isolation-probe.py --cpus "$expected_cpus" --memory-mib "$expected_memory"
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
docker --host unix:///var/run/docker.sock info
docker --host unix:///var/run/docker.sock compose version
docker --host unix:///var/run/docker.sock buildx version
# Correlate these with private worker records and immutable bundle checksums.
uname -r
sha256sum /usr/local/bin/chickadee-guest
printf 'runner=%s\n' "$CURRENT_RUNNER" >> "$GITHUB_OUTPUT"
printf 'developer runner accepted: %s\n' "$CURRENT_RUNNER"
