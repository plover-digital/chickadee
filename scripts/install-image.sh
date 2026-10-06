#!/bin/bash
# Admit a trusted operator-built bundle; this is not an arbitrary image downloader.
set -euo pipefail
[[ $EUID == 0 ]] || { echo 'Run with sudo.' >&2; exit 1; }
source_dir=$(realpath "${1:?Usage: install-image.sh SOURCE_DIR /var/lib/chickadee-images/NAME/REVISION}")
dest=${2:?destination required}
[[ $dest =~ ^/var/lib/chickadee-images/[a-z0-9-]+/[a-zA-Z0-9-]+$ && ! -e $dest ]]
(cd "$source_dir" && sha256sum --check --status SHA256SUMS)
# Fixed bundle format. Refuse links escaping it, devices and unknown extra files.
python3 - "$source_dir" <<'PY'
import pathlib,sys
root=pathlib.Path(sys.argv[1]); entries=set()
for line in (root/'SHA256SUMS').read_text().splitlines():
 digest,name=line.split(maxsplit=1);name=name.lstrip('*')
 assert len(digest)==64 and all(c in '0123456789abcdef' for c in digest) and name not in entries and '/' not in name and name not in ('.','..')
 entries.add(name)
assert {'base.qcow2','manifest.json'}<=entries
for item in root.iterdir():
 assert item.is_file() and item.name in entries|{'SHA256SUMS','vmlinuz','initrd'}
 if item.is_symlink():
  assert item.name in ('vmlinuz','initrd') and item.resolve().parent==root and item.resolve().name in entries
PY
install -d -m 0755 "$dest"
cp -a --sparse=always "$source_dir/." "$dest/"
chown -R root:root "$dest"
find "$dest" -type d -exec chmod 0755 {} +
find "$dest" -type f -exec chmod 0444 {} +
printf 'Installed immutable bundle: %s\n' "$dest"
