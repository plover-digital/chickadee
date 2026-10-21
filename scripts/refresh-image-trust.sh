#!/bin/bash
# Offline build helper. Only pass a fresh private working image, never a live base.
set -euo pipefail
cd "$(dirname "$0")/.."
image=${1:?private working image required}
work=${2:?private input workspace required}
mkdir -p "$work/downloads"
python3 guest/refresh-ca-trust.py --policy guest/images/ca-trust.json --download "$work/downloads"
# Only two authenticated Go archives, never the entire developer tool matrix.
python3 - "$work/downloads/go-inputs.json" <<'PYGO'
import json,pathlib,sys
lock=json.load(open('guest/images/github-ubuntu-2404.json'))
go={'default_go':lock['default_go'],'tools':[t for t in lock['tools'] if t['name']=='go']}
assert go['default_go']=='1.27.3' and {t['version'] for t in go['tools']}=={'1.26.10','1.27.3'}, 'combine the reviewed Go source update first'
pathlib.Path(sys.argv[1]).write_text(json.dumps(go,indent=2)+'\n')
PYGO
python3 scripts/fetch-image-tools.py "$work/downloads/go-inputs.json" "$work/downloads"
tar -C "$work/downloads" -cf "$work/trust-inputs.tar" .
virt-customize --no-network --memsize 1024 --smp 1 -a "$image" \
 --upload guest/refresh-ca-trust.py:/tmp/chickadee-refresh-ca-trust.py \
 --upload guest/images/ca-trust.json:/tmp/chickadee-ca-policy.json \
 --upload "$work/trust-inputs.tar":/tmp/chickadee-trust-inputs.tar \
 --run-command 'mkdir -p /tmp/chickadee-trust-inputs; tar -xf /tmp/chickadee-trust-inputs.tar -C /tmp/chickadee-trust-inputs' \
 --run-command 'python3 /tmp/chickadee-refresh-ca-trust.py --policy /tmp/chickadee-ca-policy.json --install /tmp/chickadee-trust-inputs' \
 --upload guest/package-provenance.py:/tmp/chickadee-package-provenance.py \
 --run-command 'python3 /tmp/chickadee-package-provenance.py > /image-package-provenance.json' \
 --run-command 'if test -e /etc/rocky-release; then rpm -qa | sort > /image-packages.txt; else dpkg-query -W > /image-packages.txt; fi' \
 --delete /tmp/chickadee-package-provenance.py \
 --run-command 'if test -e /etc/rocky-release; then setfiles -F -e /dev -e /proc -e /sys -e /run /etc/selinux/targeted/contexts/files/file_contexts /; fi' \
 --delete /tmp/chickadee-refresh-ca-trust.py --delete /tmp/chickadee-ca-policy.json \
 --delete /tmp/chickadee-trust-inputs.tar --delete /tmp/chickadee-trust-inputs \
 --no-selinux-relabel
guestfish --ro -a "$image" -i download /etc/chickadee/ca-trust.json "$work/ca-trust.json"
