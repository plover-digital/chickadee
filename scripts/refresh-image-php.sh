#!/bin/bash
# Offline narrow package transaction; image must be a private disposable copy.
set -euo pipefail
cd "$(dirname "$0")/.."
image=${1:?private image or --prepare required}
work=${2:?private inputs workspace required}
mkdir -p "$work"
downloads=${CHICKADEE_PHP_INPUTS:-$work/downloads}
mode=--download
[[ -z ${CHICKADEE_PHP_INPUTS:-} ]] || mode=--verify
python3 guest/refresh-php-security.py --policy guest/images/php-security.json "$mode" "$downloads"
python3 - "$work" "$downloads" <<'PY'
import pathlib,json,tarfile,sys
work,downloads=map(pathlib.Path,sys.argv[1:])
policy=json.load(open('guest/images/php-security.json'))
with tarfile.open(work/'php-inputs.tar','w',dereference=True) as t:
 for release in policy['releases'].values():
  for entry in release['packages']:t.add(downloads/entry['filename'],arcname=entry['filename'],recursive=False)
PY
[[ $image != --prepare ]] || exit 0
virt-customize --no-network --memsize 1024 --smp 1 -a "$image" \
 --upload guest/refresh-php-security.py:/tmp/chickadee-php.py \
 --upload guest/images/php-security.json:/tmp/chickadee-php-policy.json \
 --upload "$work/php-inputs.tar":/tmp/chickadee-php-inputs.tar \
 --run-command 'mkdir -p /tmp/chickadee-php-inputs; tar -xf /tmp/chickadee-php-inputs.tar -C /tmp/chickadee-php-inputs' \
 --run-command 'python3 /tmp/chickadee-php.py --policy /tmp/chickadee-php-policy.json --install /tmp/chickadee-php-inputs' \
 --upload guest/package-provenance.py:/tmp/chickadee-package-provenance.py \
 --run-command 'python3 /tmp/chickadee-package-provenance.py > /image-package-provenance.json' \
 --run-command 'if test -e /etc/rocky-release; then rpm -qa | sort > /image-packages.txt; else dpkg-query -W > /image-packages.txt; fi' \
 --delete /tmp/chickadee-package-provenance.py --delete /tmp/chickadee-php.py \
 --delete /tmp/chickadee-php-policy.json --delete /tmp/chickadee-php-inputs.tar \
 --delete /tmp/chickadee-php-inputs --no-selinux-relabel
guestfish --ro -a "$image" -i download /etc/chickadee/php-security.json "$work/php-security.json"
