#!/usr/bin/env bash
set -euo pipefail
case "${PROFILE_SIZE:?}" in
  small) cpus=2; memory_mib=4096 ;;
  medium) cpus=4; memory_mib=8192 ;;
  *) echo 'Unsupported Rocky profile size' >&2; exit 1 ;;
esac
. /etc/os-release
test "$ID:$VERSION_ID" = rocky:10.2
test "$(id -u)" = 1000
test "$(/usr/sbin/getenforce)" = Enforcing
test "$(uname -r)" = "${EXPECTED_KERNEL:?}"
test "$(nproc)" = "$cpus"
actual_mib=$(awk '/MemTotal:/ {print int($2/1024)}' /proc/meminfo)
test "$actual_mib" -ge "$((memory_mib-memory_mib/16))"
test "$actual_mib" -le "$memory_mib"
python3 scripts/isolation-probe.py --cpus "$cpus" --memory-mib "$memory_mib"
for tool in git git-lfs unzip zip tar gzip zstd jq sudo node npm npx python3 pip3 go gcc g++ clang cmake ninja java mvn ruby podman buildah skopeo; do
  command -v "$tool"
done
test "${RUNNER_TOOL_CACHE:?}" = /opt/hostedtoolcache
test "${AGENT_TOOLSDIRECTORY:?}" = /opt/hostedtoolcache
for version in 1.26.9 1.27.2; do test -f "$RUNNER_TOOL_CACHE/go/$version/x64.complete"; done
for version in 22.23.3 24.21.0; do test -f "$RUNNER_TOOL_CACHE/node/$version/x64.complete"; done
test "$(GOTOOLCHAIN=local go env GOVERSION)" = go1.27.2
sudo -n true
java -version
"${JAVA_HOME:?}/bin/java" -version
podman --version
buildah --version
skopeo --version
python3 -c 'import ssl,sqlite3,ctypes,zlib'
scratch=$(mktemp -d "${RUNNER_TEMP:?}/rocky-smoke.XXXXXXXX")
trap 'rm -rf -- "$scratch"' EXIT
python3 -m venv "$scratch/venv"
"$scratch/venv/bin/python" -m pip --version
printf 'int main(void){return 0;}\n' > "$scratch/main.c"
gcc "$scratch/main.c" -o "$scratch/main"
"$scratch/main"
printf 'package main\nfunc main(){}\n' > "$scratch/main.go"
GOTOOLCHAIN=local go run "$scratch/main.go"
node -e 'console.log(process.version)'
test -n "${CURRENT_RUNNER:?}"
if [ "${EXPECT_REPLACEMENT:-0}" = 1 ]; then
  test -n "${PREVIOUS_RUNNER:?}"
  test "$PREVIOUS_RUNNER" != "$CURRENT_RUNNER"
fi
printf 'runner=%s\n' "$CURRENT_RUNNER" >> "${GITHUB_OUTPUT:?}"
printf 'Rocky native profile verified: kernel=%s cpus=%s memory_mib=%s runner=%s\n' "$(uname -r)" "$cpus" "$actual_mib" "$CURRENT_RUNNER"
