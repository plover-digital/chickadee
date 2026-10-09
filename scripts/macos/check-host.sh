#!/bin/sh
# Read-only Apple Silicon feasibility check; creates no guest or network.
set -eu
[ "$(uname -s)" = Darwin ] && [ "$(uname -m)" = arm64 ] || {
  echo 'Requires an Apple Silicon macOS host' >&2; exit 1;
}
source_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
probe_dir=$(mktemp -d "${TMPDIR:-/tmp}/chickadee-macos-probe.XXXXXX")
trap 'rm -rf "$probe_dir"' EXIT HUP INT TERM
/usr/bin/swiftc "$source_dir/feasibility.swift" -o "$probe_dir/probe"
/usr/bin/codesign --force --sign - --entitlements "$source_dir/virtualization.entitlements" "$probe_dir/probe"
"$probe_dir/probe"
