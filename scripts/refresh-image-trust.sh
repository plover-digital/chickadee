#!/bin/bash
# Offline build helper. Only pass a fresh private working image, never a live base.
set -euo pipefail
cd "$(dirname "$0")/.."
image=${1:?private working image or --prepare required}
work=${2:?private input workspace required}
mkdir -p "$work"
downloads=${CHICKADEE_TRUST_INPUTS:-$work/downloads}
if [[ -n ${CHICKADEE_TRUST_INPUTS:-} ]]; then
 python3 guest/refresh-ca-trust.py --policy guest/images/ca-trust.json --verify "$downloads"
else
 python3 guest/refresh-ca-trust.py --policy guest/images/ca-trust.json --download "$downloads"
fi
# Only two authenticated Go archives, never the entire developer tool matrix.
python3 - "$work/go-inputs.json" <<'PYGO'
import json,pathlib,sys
lock=json.load(open('guest/images/github-ubuntu-2404.json'))
go={'default_go':lock['default_go'],'tools':[t for t in lock['tools'] if t['name']=='go']}
assert go['default_go']=='1.27.3' and {t['version'] for t in go['tools']}=={'1.26.10','1.27.3'}, 'combine the reviewed Go source update first'
pathlib.Path(sys.argv[1]).write_text(json.dumps(go,indent=2)+'\n')
PYGO
if [[ -n ${CHICKADEE_TRUST_INPUTS:-} ]]; then
 python3 - "$work/go-inputs.json" "$downloads" <<'PYCACHE'
import json,pathlib,sys
root=pathlib.Path(sys.argv[2])
assert all((root/t['filename']).is_file() for t in json.load(open(sys.argv[1]))['tools']), 'missing prefetched Go archive'
PYCACHE
fi
python3 scripts/fetch-image-tools.py "$work/go-inputs.json" "$downloads"
python3 - "$work" "$downloads" <<'PYTAR'
import json,pathlib,sys,tarfile
work,downloads=map(pathlib.Path,sys.argv[1:])
policy=json.load(open('guest/images/ca-trust.json'));go=json.load(open(work/'go-inputs.json'))
with tarfile.open(work/'trust-inputs.tar','w',dereference=True) as tar:
 for t in policy['python_wheels']+list(policy['ubuntu_packages'].values())+go['tools']:
  tar.add(downloads/t['filename'],arcname=t['filename'],recursive=False)
 tar.add(work/'go-inputs.json',arcname='go-inputs.json')
PYTAR
[[ $image != --prepare ]] || exit 0
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
