#!/bin/bash
set -euo pipefail
[[ -r /dev/kvm && -w /dev/kvm && -r /dev/net/tun && -w /dev/net/tun ]]
[[ -f /etc/chickadee/network-owned ]]
(cd /var/lib/chickadee-image && sha256sum --check --status SHA256SUMS)
python3 - <<'PY'
import json, os
c=json.load(open('/etc/chickadee/config.json'))
m=json.load(open('/var/lib/chickadee-image/manifest.json'))
assert c['disk_gib']==m['disk_gib'], 'disk_gib must equal the built filesystem size'
assert c['state_dir']=='/var/lib/chickadee' and c['image_dir']=='/var/lib/chickadee-image', 'installer uses fixed directories'
v=os.statvfs(c['state_dir'])
# Reserve the possible overlay footprint of every concurrent VM plus metadata.
assert v.f_bavail*v.f_frsize >= c['max_vms']*(c['disk_gib']+1)*2**30, 'insufficient space for bounded pool'
PY
