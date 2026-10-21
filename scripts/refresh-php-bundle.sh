#!/bin/bash
# Requires a separate resource GO. Never pass a live image as output.
set -euo pipefail
umask 077
cd "$(dirname "$0")/.."
source_dir=$(realpath "${1:?accepted immutable bundle required}")
out=$(realpath -m "${2:?fresh output directory required}")
[[ ! -e $out && ! -e $out.inputs && $out != "$source_dir" && $out != "$source_dir"/* && $source_dir != "$out"/* ]]
[[ -z $(git status --porcelain --untracked-files=normal) ]]
python3 scripts/check-image-bundle.py "$source_dir"
# This finite refresh retains one parent generation; refuse provenance collisions.
[[ -z $(find "$source_dir" -maxdepth 1 -name 'pre-php-*' -print -quit) ]]
# All authenticated supplemental files checked before copying the large image.
scripts/refresh-image-php.sh --prepare "$out.inputs"
mkdir -p "$out"
qemu-img convert -f qcow2 -O qcow2 "$source_dir/base.qcow2" "$out/working.qcow2"
python3 - "$source_dir" "$out" <<'PY'
import pathlib,sys,shutil
src,out=map(pathlib.Path,sys.argv[1:])
replaced={'manifest.json','SHA256SUMS','packages.txt','package-provenance.json','php-security.json','php-build-provenance.json'}
for p in src.iterdir():
 if p.name=='base.qcow2':continue
 if p.is_symlink():(out/p.name).symlink_to(p.readlink())
 else:shutil.copyfile(p,out/(('pre-php-' if p.name in replaced else '')+p.name))
PY
export LIBGUESTFS_BACKEND=direct LIBGUESTFS_BACKEND_SETTINGS=force_kvm
scripts/refresh-image-php.sh "$out/working.qcow2" "$out.inputs"
cp "$out.inputs/php-security.json" "$out/php-security.json"
guestfish --ro -a "$out/working.qcow2" -i download /image-packages.txt "$out/packages.txt"
guestfish --ro -a "$out/working.qcow2" -i download /image-package-provenance.json "$out/package-provenance.json"
python3 - "$source_dir" "$out" <<'PY'
import pathlib,json,hashlib,subprocess,datetime,sys
src,out=map(pathlib.Path,sys.argv[1:])
seal=hashlib.sha256((src/'SHA256SUMS').read_bytes()).hexdigest()
m=json.loads((src/'manifest.json').read_text());m.update(php_security_refresh='USN-8948-1',php_parent_bundle_sha256=seal)
(out/'manifest.json').write_text(json.dumps(m,indent=2)+'\n')
r={'schema':1,'source_commit':subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip(),'built_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'parent_sha256sums_sha256':seal,'policy_sha256':hashlib.sha256(pathlib.Path('guest/images/php-security.json').read_bytes()).hexdigest(),'scope':'PHP source-cohort-only offline refresh; bootstrap, CA and tool caches retained; requires separate runtime/job acceptance'}
(out/'php-build-provenance.json').write_text(json.dumps(r,indent=2)+'\n')
PY
qemu-img convert -f qcow2 -O qcow2 "$out/working.qcow2" "$out/base.qcow2"
rm "$out/working.qcow2"
(cd "$out" && find . -maxdepth 1 -type f ! -name SHA256SUMS -printf '%P\0' | sort -z | xargs -0 sha256sum > SHA256SUMS)
chmod 0444 "$out"/*
python3 scripts/check-image-bundle.py "$out"
echo "Sealed PHP candidate $out; no live install or job acceptance performed."
