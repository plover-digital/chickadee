#!/usr/bin/env bash
# Scan an immutable committed source snapshot and exact candidate binaries.
set -euo pipefail
# Ignore persisted go env -w settings, especially source/module overlays.
export GOENV=off
# Match Make builds even when a newer vulnerable bundled Go runs this script.
export GOTOOLCHAIN=go1.26.10
# go/packages must use the Go command, never an ambient external package graph.
# Unsetting this variable would still allow PATH discovery of gopackagesdriver.
export GOPACKAGESDRIVER=off
# This release graph is public. Authenticate every module, including scanner
# transitive dependencies and downloaded toolchains, against Go's built-in key.
# Explicit GONOSUMDB overrides the default inherited from GOPRIVATE.
export GO111MODULE=on GOSUMDB=sum.golang.org GONOSUMDB=none GOINSECURE=none
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
output_root=$(cd "$output" && pwd)
# Never reuse reports or delete caller files. Each attempt owns a new directory;
# current identifies this attempt even if build/install/metadata fails early.
output=$(mktemp -d "$output_root/run-XXXXXXXX")
printf 'running\n' > "$output/status.txt"
printf '%s\n' "${output##*/}" > "$output/current.tmp"
mv -f "$output/current.tmp" "$output_root/current"
scratch=
finish() {
  local result=$?
  if [[ $result == 0 ]]; then
    printf 'complete\n' > "$output/status.txt"
  else
    printf 'failed exit=%s\n' "$result" > "$output/status.txt"
  fi
  if [[ -n "$scratch" ]]; then
    # Downloaded module directories are read-only. Touch only our private tree.
    chmod -R u+w "$scratch"
    rm -rf "$scratch"
  fi
}
trap finish EXIT
bin_dir=$(cd "$bin_dir" && pwd)
scratch=$(mktemp -d)
# Both the application and scanner resolve dependencies afresh; extracted
# modules in the caller's cache may have been edited after checksum verification.
export GOMODCACHE="$scratch/modules"
mkdir -p "$scratch/source" "$scratch/tools" "$GOMODCACHE"
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
  printf 'module_cache_policy=fresh-per-attempt-cleaned-on-exit\n'
  printf 'GOENV=%s\n' "$GOENV"
  printf 'GOPACKAGESDRIVER=%s\n' "$GOPACKAGESDRIVER"
  go version
  CGO_ENABLED=0 go env -json GOOS GOARCH CGO_ENABLED GOTOOLCHAIN GOFLAGS GOWORK GOENV GOMODCACHE GO111MODULE GOSUMDB GONOSUMDB GOINSECURE
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
