#!/bin/bash
# Coordinator resource GO required. Offline immutable Ubuntu developer copy only.
set -euo pipefail
umask 077
cd "$(dirname "$0")/.."
source_dir=$(realpath "${1:?sealed Ubuntu parent required}")
out=$(realpath -m "${2:?fresh output required}")
seal=${3:?exact parent seal required}
plan=$(realpath "${4:?prepared signed transaction required}")
cache=$(realpath "${5:?authenticated offline deb cache required}")
[[ ! -e $out && ! -e $out.inputs.tar && $out != "$source_dir" && $out != "$source_dir"/* && $source_dir != "$out"/* ]]
repository=$(pwd -P)
source_status=$(git -c safe.directory="$repository" status --porcelain --untracked-files=normal)
[[ -z $source_status ]]
printf '%s  %s\n' "$seal" "$source_dir/SHA256SUMS" | sha256sum --check
python3 scripts/check-image-bundle.py "$source_dir"
bootstrap=$(python3 scripts/check-runtime-parent.py "$source_dir")
[[ $bootstrap =~ ^[0-9a-f]{64}$ ]]
python3 - "$source_dir" "$plan" <<'PY'
import json,pathlib,sys
src,plan=map(pathlib.Path,sys.argv[1:]);p=json.loads(plan.read_text());m=json.loads((src/'manifest.json').read_text())
assert m['os']=='ubuntu' and m['os_version']==p['family'] and m.get('software_profile')=='developer'
assert m['kernel']!=p['kernel'] and m['machine']==('microvm' if p['family']=='24.04' else 'q35')
assert json.loads((src/'package-provenance.json').read_text())['packages']==p['parent_packages']
assert not list(src.glob('pre-os-*')), 'Already refreshed parent requires a separately reviewed subsequent revision'
PY
python3 scripts/prefetch-ubuntu-os-inputs.py --verify-only "$plan" "$cache"
# Every small input is authenticated before copying the accepted image.
mkdir "$out"
python3 - "$source_dir" "$out" "$plan" "$cache" <<'PY'
import pathlib,json,shutil,sys,tarfile
src,out,plan,cache=map(pathlib.Path,sys.argv[1:]);replaced={'manifest.json','SHA256SUMS','packages.txt','package-provenance.json','ca-trust.json'}
for path in src.iterdir():
 if path.name in ('base.qcow2','vmlinuz','initrd'):continue
 shutil.copyfile(path,out/(('pre-os-' if path.name in replaced else '')+path.name))
shutil.copyfile(plan,out/'os-package-plan.json')
with tarfile.open(str(out)+'.inputs.tar','w') as archive:
 for row in json.loads(plan.read_text())['packages']:archive.add(cache/row['filename'],arcname=row['filename'],recursive=False)
PY
qemu-img convert -f qcow2 -O qcow2 "$source_dir/base.qcow2" "$out/working.qcow2"
export LIBGUESTFS_BACKEND=direct LIBGUESTFS_BACKEND_SETTINGS=force_kvm LIBGUESTFS_MEMSIZE=1024
virt-customize --no-network --memsize 1024 --smp 1 -a "$out/working.qcow2" \
 --mkdir /tmp/chickadee-os \
 --upload guest/refresh-ubuntu-os.py:/tmp/chickadee-os/install.py \
 --upload "$plan":/tmp/chickadee-os/plan.json \
 --upload "$out.inputs.tar":/tmp/chickadee-os/inputs.tar \
 --upload guest/refresh-ca-trust.py:/tmp/chickadee-os/refresh-ca-trust.py \
 --upload guest/images/ca-trust.json:/tmp/chickadee-os/trust.json \
 --upload guest/package-provenance.py:/tmp/chickadee-os/package-provenance.py \
 --run-command 'mkdir /tmp/chickadee-os/inputs; tar -xf /tmp/chickadee-os/inputs.tar -C /tmp/chickadee-os/inputs' \
 --run-command "python3 /tmp/chickadee-os/install.py --plan /tmp/chickadee-os/plan.json --cache /tmp/chickadee-os/inputs --report /etc/chickadee/os-security.json --expected-bootstrap $bootstrap" \
 --run-command 'python3 /tmp/chickadee-os/refresh-ca-trust.py --policy /tmp/chickadee-os/trust.json --refresh-java-report /etc/chickadee/os-java-trust.json' \
 --run-command 'python3 /tmp/chickadee-os/package-provenance.py > /image-package-provenance.json; dpkg-query -W > /image-packages.txt' \
 --delete /tmp/chickadee-os --no-selinux-relabel
for report in os-security os-java-trust; do guestfish --ro -a "$out/working.qcow2" -i download "/etc/chickadee/$report.json" "$out/$report.json"; done
guestfish --ro -a "$out/working.qcow2" -i download /etc/chickadee/os-security.apt-install.json "$out/os-security.apt-install.json"
guestfish --ro -a "$out/working.qcow2" -i download /etc/chickadee/os-security.apt-plan.txt "$out/os-security.apt-plan.txt"
guestfish --ro -a "$out/working.qcow2" -i download /etc/chickadee/os-security.apt-simulation.json "$out/os-security.apt-simulation.json"
guestfish --ro -a "$out/working.qcow2" -i download /etc/chickadee/os-security.dpkg-preflight.json "$out/os-security.dpkg-preflight.json"
guestfish --ro -a "$out/working.qcow2" -i download /image-packages.txt "$out/packages.txt"
guestfish --ro -a "$out/working.qcow2" -i download /image-package-provenance.json "$out/package-provenance.json"
kernel=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["kernel"])' "$plan")
[[ $kernel =~ ^[0-9]+\.[0-9]+\.[0-9]+-[0-9]+-generic$ ]]
guestfish --ro -a "$out/working.qcow2" -i download "/boot/vmlinuz-$kernel" "$out/vmlinuz-$kernel"
guestfish --ro -a "$out/working.qcow2" -i download "/boot/initrd.img-$kernel" "$out/initrd-$kernel"
ln -s "vmlinuz-$kernel" "$out/vmlinuz"
ln -s "initrd-$kernel" "$out/initrd"
python3 scripts/refresh-ubuntu-os-metadata.py "$source_dir" "$out"
virt-sparsify --in-place "$out/working.qcow2"
qemu-img convert -f qcow2 -O qcow2 "$out/working.qcow2" "$out/base.qcow2"
rm "$out/working.qcow2"
(cd "$out" && find . -maxdepth 1 -type f ! -name SHA256SUMS -printf '%P\0' | sort -z | xargs -0 sha256sum > SHA256SUMS)
chmod 0444 "$out"/*
python3 scripts/check-image-bundle.py "$out"
python3 scripts/check-image-bundle.py "$source_dir"
echo 'Sealed private native OS candidate; cold boot/profile/runtime/job acceptance and coordinator deployment remain pending.'
