#!/usr/bin/env bash
# Scan source and the exact candidate binaries; never execute a candidate.
set -euo pipefail
cd "$(dirname "$0")/.."
bin_dir=${1:-bin}
output=${VULNCHECK_OUTPUT:-build/vulncheck}
database=${GOVULNDB:-https://vuln.go.dev}
scanner_version=v1.9.0
mkdir -p "$output"
output=$(cd "$output" && pwd)
tool_dir=$(mktemp -d)
trap 'rm -rf "$tool_dir"' EXIT
GOBIN="$tool_dir" go install "golang.org/x/vuln/cmd/govulncheck@$scanner_version"
scanner="$tool_dir/govulncheck"
{
  date -u '+checked_at=%Y-%m-%dT%H:%M:%SZ'
  git rev-parse HEAD
  go version
  go env GOOS GOARCH CGO_ENABLED GOTOOLCHAIN GOFLAGS
  "$scanner" -version
  printf 'database=%s\n' "$database"
} > "$output/environment.txt"

# Text mode exits nonzero for reachable symbols. Verbose output also records
# package/module-only findings; version output includes database modification time.
status=0
scan() {
  local name=$1
  shift
  if "$scanner" -db "$database" -show version,verbose "$@" 2>&1 | tee "$output/$name.txt"; then
    :
  else
    status=1
  fi
}
CGO_ENABLED=0 scan source ./...
for name in chickadee chickadee-worker chickadee-guest; do
  go version -m "$bin_dir/$name" > "$output/$name.build.txt"
  sha256sum "$bin_dir/$name" > "$output/$name.sha256"
  scan "$name" -mode=binary "$bin_dir/$name"
done
exit "$status"
