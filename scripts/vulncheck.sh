#!/usr/bin/env bash
# Scan an immutable committed source snapshot and exact candidate binaries.
set -euo pipefail
cd "$(dirname "$0")/.."
./scripts/check-go-source.sh
source_commit=$(git rev-parse HEAD)
build=false
if [[ ${1:-} == --build ]]; then
  build=true
  shift
fi
bin_dir=${1:-bin}
output=${VULNCHECK_OUTPUT:-build/vulncheck}
database=${GOVULNDB:-https://vuln.go.dev}
scanner_version=v1.9.0
# Workspace discovery and source overlays must not change the committed inputs.
export GOWORK=off
mkdir -p "$output" "$bin_dir"
output=$(cd "$output" && pwd)
bin_dir=$(cd "$bin_dir" && pwd)
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
mkdir -p "$scratch/source" "$scratch/tools"
git archive "$source_commit" | tar -x -C "$scratch/source"
cd "$scratch/source"
if "$build"; then
  make -C "$scratch/source" build BIN_DIR="$bin_dir"
fi
GOBIN="$scratch/tools" go install "golang.org/x/vuln/cmd/govulncheck@$scanner_version"
scanner="$scratch/tools/govulncheck"
{
  date -u '+checked_at=%Y-%m-%dT%H:%M:%SZ'
  printf 'source_commit=%s\nsource_mode=git-archive\nbinaries_built_from_snapshot=%s\n' "$source_commit" "$build"
  go version
  CGO_ENABLED=0 go env -json GOOS GOARCH CGO_ENABLED GOTOOLCHAIN GOFLAGS GOWORK
  "$scanner" -version
  printf 'database=%s\n' "$database"
} > "$output/environment.txt"

# Text mode fails for reachable symbols; verbose output separates module-only
# findings and version output includes the database modification time.
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
CGO_ENABLED=0 scan source -C "$scratch/source" ./...
for name in chickadee chickadee-worker chickadee-guest; do
  go version -m "$bin_dir/$name" > "$output/$name.build.txt"
  sha256sum "$bin_dir/$name" > "$output/$name.sha256"
  scan "$name" -mode=binary "$bin_dir/$name"
done
exit "$status"
