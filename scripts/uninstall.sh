#!/bin/bash
set -euo pipefail
[[ $EUID == 0 ]]
[[ ${1:-} == --remove ]] || { echo 'Usage: uninstall.sh --remove (keeps credentials, logs, images, state and GitHub scale set)' >&2; exit 1; }
systemctl disable --now chickadee.service chickadee-network.service
/usr/local/lib/chickadee/network.sh remove
rm -f /etc/systemd/system/chickadee.service /etc/systemd/system/chickadee-network.service
rm -rf /etc/systemd/system/chickadee.service.d
rm -f /usr/local/bin/chickadee /usr/local/lib/chickadee/network.sh /usr/local/lib/chickadee/preflight.sh /usr/local/lib/chickadee/profile-settings.py
systemctl daemon-reload
echo 'Removed services and owned networking. Retained /etc/chickadee, /var/lib/chickadee, image artifacts and GitHub scale set; reconcile runner registrations before removing state.'
