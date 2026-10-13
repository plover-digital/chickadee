#!/bin/bash
# Build a checksum-verified runner bundle. Host userland is supplied read-only
# by this backend; this is not a guest OS image and starts no runner/network.
set -euo pipefail
umask 077
out=${1:?usage: build-landlock-bundle.sh OUTPUT_DIRECTORY [TMPFS_GIB]}
disk=${2:-8}
[[ "$disk" =~ ^[0-9]+$ && "$disk" -ge 8 && "$disk" -le 64 ]]
[[ ! -e "$out" ]] || { echo 'Output exists; refusing to overwrite.' >&2; exit 1; }
runner=2.338.0
sha=af4b794c1bc41d73d40535e3fe092a39f9679cd8d965954c2aca25a05ca41d32
mkdir -m 0700 -p "$out"
curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 --connect-timeout 15 --max-time 600 --retry 3 "https://github.com/actions/runner/releases/download/v$runner/actions-runner-linux-x64-$runner.tar.gz" -o "$out/runner.tar.gz"
printf '%s  %s\n' "$sha" "$out/runner.tar.gz" | sha256sum --check
python3 - "$out" "$runner" "$disk" <<'PY'
import json,pathlib,sys
out=pathlib.Path(sys.argv[1])
(out/'manifest.json').write_text(json.dumps({'machine':'landlock','disk_gib':int(sys.argv[3]),'architecture':'amd64','runner_version':sys.argv[2],'userland':'host-read-only','storage':'private-tmpfs'},sort_keys=True)+'\n')
PY
(cd "$out"; sha256sum runner.tar.gz manifest.json > SHA256SUMS)
chmod 0444 "$out/runner.tar.gz" "$out/manifest.json" "$out/SHA256SUMS"
echo 'Bundle digest:'
sha256sum "$out/SHA256SUMS"
