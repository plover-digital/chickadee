#!/bin/bash
# Create a new immutable bundle from an accepted one. Requires resource approval
# before use: libguestfs starts a trusted appliance, never a job/runner VM.
set -euo pipefail
umask 077
cd "$(dirname "$0")/.."
source_dir=$(realpath "${1:?accepted source bundle required}")
out=$(realpath -m "${2:?new output directory required}")
bootstrap=$(realpath "${3:?patched guest bootstrap required}")
bootstrap_manifest=$(realpath "${4:?combined-source bootstrap manifest required}")
[[ ! -e $out.inputs && ! -e $out && $out != "$source_dir"/* && $source_dir != "$out"/* ]]
for tool in go python3 qemu-img virt-customize guestfish sha256sum; do command -v "$tool" >/dev/null; done
# A dirty source checkout cannot be represented by a commit provenance value.
[[ -z $(git status --porcelain --untracked-files=normal) ]]
python3 - "$bootstrap" "$bootstrap_manifest" <<'PYBOOT'
import hashlib,json,pathlib,subprocess,sys
binary,manifest=map(pathlib.Path,sys.argv[1:]);record=json.loads(manifest.read_text())
commit=subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip()
assert subprocess.check_output(['git','rev-parse',record['source_commit']],text=True).strip()==commit, 'bootstrap source must match this exact combined source'
assert hashlib.file_digest(binary.open('rb'),'sha256').hexdigest()==record['binary_sha256'], 'bootstrap manifest checksum mismatch'
assert record['compiler']=='go1.27.3', 'patched compiler required'
actual=subprocess.check_output(['go','version','-m',str(binary)],text=True).splitlines()[0]
assert actual == str(binary)+': '+record['compiler'], 'actual bootstrap compiler must match the manifest go1.27.3'
PYBOOT
python3 scripts/check-image-bundle.py "$source_dir"
mkdir -p "$out/inputs"
cp "$bootstrap_manifest" "$out/bootstrap-build-provenance.json"
# Verify every supplemental input before the expensive independent image copy.
scripts/refresh-image-trust.sh --prepare "$out/inputs"
# A full independent qcow2 copy: no backing reference to a live bundle.
qemu-img convert -f qcow2 -O qcow2 "$source_dir/base.qcow2" "$out/working.qcow2"
# Keep kernel/initrd and other parent provenance. Replaced files are saved too.
python3 - "$source_dir" "$out" <<'PY'
import pathlib,shutil,sys
src,out=map(pathlib.Path,sys.argv[1:])
replaced={'manifest.json','SHA256SUMS','packages.txt','package-provenance.json','guest-build.txt','ca-trust.json','tool-inputs.json','tool-lock.json','compatibility.json'}
for p in src.iterdir():
 if p.name=='base.qcow2':continue
 if p.is_symlink():
  if p.name not in ('vmlinuz','initrd'):raise ValueError('unexpected bundle symlink')
  target=p.readlink()
  if target.is_absolute() or len(target.parts)!=1:raise ValueError('unsafe bundle symlink')
  (out/p.name).symlink_to(target)
 elif p.is_file():shutil.copyfile(p,out/(('parent-' if p.name in replaced else '')+p.name))
 else:raise ValueError('unexpected directory in source bundle')
PY
export LIBGUESTFS_BACKEND=direct
export LIBGUESTFS_BACKEND_SETTINGS=force_kvm
virt-customize --no-network --memsize 1024 --smp 1 -a "$out/working.qcow2" \
 --upload "$bootstrap":/usr/local/bin/chickadee-guest \
 --chmod 0755:/usr/local/bin/chickadee-guest --no-selinux-relabel
scripts/refresh-image-trust.sh "$out/working.qcow2" "$out/inputs"
cp "$out/inputs/ca-trust.json" "$out/ca-trust.json"
python3 scripts/refresh-trust-metadata.py "$source_dir" "$out"
guestfish --ro -a "$out/working.qcow2" -i download /image-packages.txt "$out/packages.txt"
guestfish --ro -a "$out/working.qcow2" -i download /image-package-provenance.json "$out/package-provenance.json"
go version -m "$bootstrap" > "$out/guest-build.txt"
python3 - "$source_dir" "$out" "$bootstrap" <<'PY'
import datetime,hashlib,json,pathlib,subprocess,sys
src,out,bootstrap=map(pathlib.Path,sys.argv[1:])
manifest=json.loads((src/'manifest.json').read_text())
manifest['trust_refresh']='emudhra-20261021-v1'
manifest['parent_bundle_sha256']=hashlib.sha256((src/'SHA256SUMS').read_bytes()).hexdigest()
# Original cloud-root/full-package snapshot fields deliberately remain intact.
(out/'manifest.json').write_text(json.dumps(manifest,indent=2)+'\n')
provenance={'schema':1,'source_commit':subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip(),
 'built_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),
 'parent_sha256sums_sha256':manifest['parent_bundle_sha256'],
 'bootstrap_sha256':hashlib.file_digest(bootstrap.open('rb'),'sha256').hexdigest(),
 'ca_policy_sha256':hashlib.sha256(pathlib.Path('guest/images/ca-trust.json').read_bytes()).hexdigest(),
 'scope':'offline trust-only refresh of an accepted immutable bundle; requires separate boot/job acceptance'}
(out/'trust-build-provenance.json').write_text(json.dumps(provenance,indent=2)+'\n')
PY
# Inputs were verified by the installer and recorded in ca-trust.json. Preserve
# downloads outside the final bundle so it retains the installer's flat format.
mv "$out/inputs" "$out.inputs"
# Compact the independent copy and seal only after all editing has finished.
qemu-img convert -f qcow2 -O qcow2 "$out/working.qcow2" "$out/base.qcow2"
rm "$out/working.qcow2"
(cd "$out" && find . -maxdepth 1 -type f ! -name SHA256SUMS -printf '%P\0' | sort -z | xargs -0 sha256sum > SHA256SUMS)
chmod 0444 "$out"/*
echo "Sealed $out. No live installation or VM/job acceptance performed."
