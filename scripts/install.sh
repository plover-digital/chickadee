#!/bin/bash
# Install files only. Networking and service start are explicit subsequent steps.
set -euo pipefail
[[ $EUID == 0 ]] || { echo 'Run with sudo.' >&2; exit 1; }
. /etc/os-release
[[ $ID == ubuntu && $VERSION_ID == 24.04 && $(uname -m) == x86_64 ]] || { echo 'Documented installer requires Ubuntu 24.04 amd64.' >&2; exit 1; }
cd "$(dirname "$0")/.."
config=${1:?Usage: install.sh CONFIG_JSON}
[[ -f bin/chickadee && -f images/base.qcow2 ]]
bin/chickadee -config "$config" -check
(cd images && sha256sum --check --status SHA256SUMS)
if ! id chickadee >/dev/null 2>&1; then useradd --system --user-group --home-dir /var/lib/chickadee --shell /usr/sbin/nologin chickadee; fi
install -d -m 0750 -o root -g chickadee /etc/chickadee
install -d -m 0700 -o chickadee -g chickadee /var/lib/chickadee
install -d -m 0755 /var/lib/chickadee-image /usr/local/lib/chickadee
[[ ! -e /etc/chickadee/config.json ]] || { echo 'Existing installation; stop controller and review config/image upgrade manually.' >&2; exit 1; }
install -m 0640 -o root -g chickadee "$config" /etc/chickadee/config.json
install -m 0755 bin/chickadee /usr/local/bin/chickadee
cp -a images/. /var/lib/chickadee-image/
chown -R root:root /var/lib/chickadee-image
find /var/lib/chickadee-image -type f -exec chmod 0444 {} +
install -m 0755 scripts/network.sh scripts/preflight.sh /usr/local/lib/chickadee/
install -m 0644 deploy/chickadee.service deploy/chickadee-network.service /etc/systemd/system/
mkdir -p /etc/systemd/system/chickadee.service.d
python3 - <<'PY'
import json
c=json.load(open('/etc/chickadee/config.json'))
assert c['state_dir']=='/var/lib/chickadee' and c['image_dir']=='/var/lib/chickadee-image'
# Includes allowance for QEMU overhead and Go controller; cgroup bounds the whole service.
mem=c['max_vms']*(c['memory_mib']+512)+512
cpu=c['max_vms']*c['cpus']*100
with open('/etc/systemd/system/chickadee.service.d/resources.conf','w') as f:
 f.write(f'[Service]\nMemoryMax={mem}M\nCPUQuota={cpu}%\nTasksMax={64+c["max_vms"]*(c["cpus"]+16)}\n')
PY
systemctl daemon-reload
printf '%s\n' 'Installed; service remains stopped. Store the App key, explicitly apply networking, then enable the services as documented.'
