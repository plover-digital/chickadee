#!/bin/bash
# Requires coordinator resource GO; never edits the sealed parent or live images.
set -euo pipefail
umask 077
cd "$(dirname "$0")/.."
source_dir=$(realpath "${1:?sealed parent bundle required}")
out=$(realpath -m "${2:?fresh private output required}")
seal=${3:?exact parent SHA256SUMS digest required}
family=${4:?24.04, 26.04 or rocky required}
[[ ! -e $out && ! -e $out.inputs && $out != "$source_dir" && $out != "$source_dir"/* && $source_dir != "$out"/* ]]
[[ -z $(git status --porcelain --untracked-files=normal) ]]
printf '%s  %s\n' "$seal" "$source_dir/SHA256SUMS" | sha256sum --check
python3 scripts/check-image-bundle.py "$source_dir"
export CHICKADEE_RUNTIME_BOOTSTRAP_SHA256=$(python3 scripts/check-runtime-parent.py "$source_dir")
[[ -z $(find "$source_dir" -maxdepth 1 -name 'pre-runtime-*' -print -quit) ]]
# Fail fast on every supplemental hash before copying a large parent image.
scripts/refresh-image-runtimes.sh --prepare "$out.inputs" "$family"
mkdir "$out"
qemu-img convert -f qcow2 -O qcow2 "$source_dir/base.qcow2" "$out/working.qcow2"
python3 - "$source_dir" "$out" <<'PY'
import pathlib,sys,shutil
src,out=map(pathlib.Path,sys.argv[1:])
replace={'manifest.json','SHA256SUMS','packages.txt','package-provenance.json','ca-trust.json','tool-inputs.json','tool-lock.json','compatibility.json','extra-inputs.json'}
for p in src.iterdir():
 if p.name=='base.qcow2':continue
 if p.is_symlink():(out/p.name).symlink_to(p.readlink())
 else:shutil.copyfile(p,out/(('pre-runtime-' if p.name in replace else '')+p.name))
PY
export LIBGUESTFS_BACKEND=direct LIBGUESTFS_BACKEND_SETTINGS=force_kvm
scripts/refresh-image-runtimes.sh "$out/working.qcow2" "$out.inputs" "$family"
for report in runtime-security runtime-trust; do cp "$out.inputs/$report.json" "$out/$report.json"; done
guestfish --ro -a "$out/working.qcow2" -i download /image-packages.txt "$out/packages.txt"
guestfish --ro -a "$out/working.qcow2" -i download /image-package-provenance.json "$out/package-provenance.json"
python3 scripts/refresh-runtime-metadata.py "$source_dir" "$out" "$family"
# Discard superseded SDK/cache and temporary archive blocks before compaction.
virt-sparsify --in-place "$out/working.qcow2"
qemu-img convert -f qcow2 -O qcow2 "$out/working.qcow2" "$out/base.qcow2"
rm "$out/working.qcow2"
(cd "$out" && find . -maxdepth 1 -type f ! -name SHA256SUMS -printf '%P\0' | sort -z | xargs -0 sha256sum > SHA256SUMS)
chmod 0444 "$out"/*
python3 scripts/check-image-bundle.py "$out"
echo "Sealed private runtime candidate $out; requires actual consumer acceptance before deployment."
