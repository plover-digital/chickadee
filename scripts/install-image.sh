#!/bin/bash
# Admit a trusted operator-built bundle; this is not an arbitrary image downloader.
set -euo pipefail
[[ $EUID == 0 ]] || { echo 'Run with sudo.' >&2; exit 1; }
source_dir=$(realpath "${1:?Usage: install-image.sh SOURCE_DIR /var/lib/chickadee-images/NAME/REVISION}")
dest=${2:?destination required}
[[ $dest =~ ^/var/lib/chickadee-images/[a-z0-9-]+/[a-zA-Z0-9-]+$ && ! -e $dest ]]
# Share exact-entry, digest and sealed-kernel alias validation with refresh.
python3 "$(dirname "$0")/check-image-bundle.py" "$source_dir"
install -d -m 0755 "$dest"
cp -a --sparse=always "$source_dir/." "$dest/"
chown -R root:root "$dest"
find "$dest" -type d -exec chmod 0755 {} +
find "$dest" -type f -exec chmod 0444 {} +
printf 'Installed immutable bundle: %s\n' "$dest"
