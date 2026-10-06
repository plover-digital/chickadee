#!/bin/bash
# Layer pinned GitHub-hosted Ubuntu tools onto a credential-free Chickadee image.
# Run with the same Ubuntu 24.04/libguestfs build environment as build-image.sh.
set -euo pipefail
umask 077
cd "$(dirname "$0")/.."
source_dir=${1:-images}
out=${2:-build/github-compatible/image}
for tool in go python3 qemu-img guestfish virt-customize virt-sparsify; do command -v "$tool" >/dev/null; done
[[ ! -e $out/base.qcow2 ]] || { echo 'Output image already exists' >&2; exit 1; }
(cd "$source_dir" && sha256sum --check SHA256SUMS)
mkdir -p "$out" build/github-compatible/downloads
python3 - "$source_dir" <<'PY'
import json,pathlib,sys
m=json.load(open(pathlib.Path(sys.argv[1])/'manifest.json'))
assert m.get('ubuntu_release') or (m.get('os')=='ubuntu' and m.get('os_version')=='24.04'), 'Ubuntu 24.04 source required'
PY
# Inputs are exact official release assets; verify even already-cached downloads.
python3 scripts/fetch-image-tools.py guest/images/github-ubuntu-2404.json build/github-compatible/downloads
python3 guest/install-github-extra-tools.py --lock guest/images/github-extra-tools.json --download build/github-compatible/extra-downloads
export LIBGUESTFS_BACKEND=direct
if [[ -r /dev/kvm && -w /dev/kvm ]]; then export LIBGUESTFS_BACKEND_SETTINGS=force_kvm; else export LIBGUESTFS_BACKEND_SETTINGS=force_tcg; fi
mkdir -p build/github-compatible/appliance-bin
printf '#!/bin/sh\nexit 127\n' > build/github-compatible/appliance-bin/passt
chmod 0755 build/github-compatible/appliance-bin/passt
export PATH="$PWD/build/github-compatible/appliance-bin:$PATH"
qemu-img convert -f qcow2 -O qcow2 "$source_dir/base.qcow2" "$out/working.qcow2"
qemu-img resize "$out/working.qcow2" 48G
guestfish -a "$out/working.qcow2" run : resize2fs /dev/sda
python3 - <<'PY'
import json,pathlib,tarfile
lock=json.load(open('guest/images/github-ubuntu-2404.json'))
with tarfile.open('build/github-compatible/tools.tar','w') as tar:
 for e in lock['tools']:tar.add(pathlib.Path('build/github-compatible/downloads')/e['filename'],arcname=e['filename'])
extra=json.load(open('guest/images/github-extra-tools.json'))
with tarfile.open('build/github-compatible/extra-tools.tar','w') as tar:
 for e in extra['tools']:tar.add(pathlib.Path('build/github-compatible/extra-downloads')/e['filename'],arcname=e['filename'])
PY
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath -ldflags='-s -w' -o build/github-compatible/chickadee-guest ./cmd/chickadee-guest
virt-customize -a "$out/working.qcow2" \
  --upload build/github-compatible/chickadee-guest:/usr/local/bin/chickadee-guest \
  --upload guest/network.sh:/usr/local/bin/chickadee-network \
  --chmod 0755:/usr/local/bin/chickadee-network \
  --chmod 0755:/usr/local/bin/chickadee-guest \
  --upload guest/images/github-ubuntu-2404.json:/tmp/github-image-lock.json \
  --upload build/github-compatible/tools.tar:/tmp/github-tools.tar \
  --upload guest/install-github-tools.py:/tmp/install-github-tools.py \
  --run-command 'python3 /tmp/install-github-tools.py' \
  --delete /tmp/github-image-lock.json --delete /tmp/github-tools.tar --delete /tmp/install-github-tools.py
virt-customize -a "$out/working.qcow2" \
  --upload guest/images/github-extra-tools.json:/tmp/github-extra-lock.json \
  --upload build/github-compatible/extra-tools.tar:/tmp/github-extra-tools.tar \
  --upload guest/install-github-extra-tools.py:/tmp/install-github-extra-tools.py \
  --run-command 'mkdir -p /tmp/github-extra-downloads; tar -xf /tmp/github-extra-tools.tar -C /tmp/github-extra-downloads' \
  --run-command 'python3 /tmp/install-github-extra-tools.py --lock /tmp/github-extra-lock.json --install /tmp/github-extra-downloads' \
  --delete /tmp/github-extra-downloads --delete /tmp/github-extra-lock.json --delete /tmp/github-extra-tools.tar --delete /tmp/install-github-extra-tools.py \
  --run-command 'rm -f /var/lib/systemd/random-seed /var/lib/dbus/machine-id; truncate -s 0 /etc/machine-id' \
  --delete /etc/resolv.conf --write /etc/resolv.conf:'nameserver 1.1.1.1'
# Discard deleted temporary archive blocks, then compact qcow2 physical file length.
virt-sparsify --in-place "$out/working.qcow2"
# Compact allocated blocks so startup digest checks do not scan a giant sparse file.
qemu-img convert -f qcow2 -O qcow2 -o compat=1.1 "$out/working.qcow2" "$out/base.qcow2"
rm "$out/working.qcow2"
guestfish --ro -a "$out/base.qcow2" -m /dev/sda download /image-packages.txt "$out/packages.txt"
guestfish --ro -a "$out/base.qcow2" -m /dev/sda download /etc/chickadee/image-compatibility.json "$out/compatibility.json"
python3 - "$source_dir" "$out" <<'PY'
import hashlib,json,pathlib,shutil,sys
src,out=map(pathlib.Path,sys.argv[1:]);m=json.load(open(src/'manifest.json'))
m.update({'disk_gib':48,'os':'ubuntu','os_version':'24.04','architecture':'amd64','machine':'microvm','compatibility':'github-ubuntu-2404-core','runner_images_commit':'5f7588b285eccc2edbeb1cd79d65ee0b577e4b4a'})
(out/'manifest.json').write_text(json.dumps(m,sort_keys=True)+'\n')
shutil.copyfile('guest/images/github-ubuntu-2404.json',out/'tool-lock.json')
shutil.copyfile('guest/images/github-extra-tools.json',out/'extra-tool-lock.json')
for alias in ['vmlinuz','initrd']:
 resolved=(src/alias).resolve();shutil.copyfile(resolved,out/resolved.name);(out/alias).symlink_to(resolved.name)
with (out/'SHA256SUMS').open('w') as f:
 for p in sorted(out.iterdir()):
  if p.is_file() and not p.is_symlink() and p.name!='SHA256SUMS':
   with p.open('rb') as data:digest=hashlib.file_digest(data,'sha256').hexdigest()
   f.write(f'{digest}  {p.name}\n')
PY
chmod 0444 "$out"/*
echo "Built $out. This core bundle is not yet complete GitHub-hosted image parity; see compatibility.json."
