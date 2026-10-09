#!/bin/sh
set -eu
[ "$(uname -s)" = Darwin ] && [ "$(uname -m)" = arm64 ] || {
  echo 'Requires Apple Silicon macOS' >&2; exit 1;
}
source_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
[ "$#" = 1 ] || { echo 'Usage: build-native.sh OUTPUT' >&2; exit 1; }
/usr/bin/swiftc -warnings-as-errors "$source_dir/native-vm.swift" -o "$1"
/usr/bin/codesign --force --sign - --entitlements "$source_dir/virtualization.entitlements" "$1"
