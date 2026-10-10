#!/bin/sh
set -eu
umask 077
test "$(uname -s)" = Darwin && test "$(uname -m)" = arm64
test "$#" = 1 || { echo 'Usage: build-pilot.sh PRIVATE_OUTPUT_DIRECTORY' >&2; exit 1; }
repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
mkdir -p "$1"
out=$(CDPATH= cd -- "$1" && pwd)
"$repo/scripts/macos/build-native.sh" "$out/native-vm"
CGO_ENABLED=0 go -C "$repo" build -mod=readonly -trimpath -o "$out/chickadee-macos-guest" ./cmd/chickadee-macos-guest
CGO_ENABLED=0 go -C "$repo" build -mod=readonly -trimpath -o "$out/chickadee-macos-worker" ./cmd/chickadee-macos-worker
CGO_ENABLED=0 go -C "$repo/scripts/macos/netproxy" build -mod=readonly -trimpath -o "$out/netproxy" .
codesign --force --sign - "$out/chickadee-macos-guest"
codesign --force --sign - "$out/netproxy"
