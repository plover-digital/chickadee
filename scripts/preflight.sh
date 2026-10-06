#!/bin/bash
set -euo pipefail
[[ -r /dev/kvm && -w /dev/kvm && -r /dev/net/tun && -w /dev/net/tun ]]
[[ -f /etc/chickadee/network-owned ]]
/usr/local/bin/chickadee -config /etc/chickadee/config.json -check
python3 /usr/local/lib/chickadee/profile-settings.py preflight /etc/chickadee/config.json
