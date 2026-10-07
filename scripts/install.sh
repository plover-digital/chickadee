#!/bin/bash
# Install files only. Networking and service start are explicit subsequent steps.
set -euo pipefail
[[ $EUID == 0 ]] || { echo 'Run with sudo.' >&2; exit 1; }
. /etc/os-release
[[ $ID == ubuntu && $VERSION_ID == 24.04 && $(uname -m) == x86_64 ]] || { echo 'Documented installer requires Ubuntu 24.04 amd64.' >&2; exit 1; }
cd "$(dirname "$0")/.."
config=${1:?Usage: install.sh CONFIG_JSON}
[[ -f bin/chickadee ]]
command -v bwrap >/dev/null || { echo 'Install bubblewrap before installing Chickadee.' >&2; exit 1; }
bin/chickadee -config "$config" -check
catalog=$(python3 -c 'import json,sys;c=json.load(open(sys.argv[1]));print(bool(c.get("profiles") or c.get("scopes")))' "$config")
if [[ $catalog == False ]]; then
  (cd images && sha256sum --check --status SHA256SUMS)
else
  echo 'Catalog installation: install each immutable bundle with scripts/install-image.sh first.'
fi
if ! id chickadee >/dev/null 2>&1; then useradd --system --user-group --home-dir /var/lib/chickadee --shell /usr/sbin/nologin chickadee; fi
install -d -m 0750 -o root -g chickadee /etc/chickadee
install -d -m 0700 -o chickadee -g chickadee /var/lib/chickadee
install -d -m 0755 /var/lib/chickadee-image /usr/local/lib/chickadee
[[ ! -e /etc/chickadee/config.json ]] || { echo 'Existing installation; stop controller and review config/image upgrade manually.' >&2; exit 1; }
install -m 0640 -o root -g chickadee "$config" /etc/chickadee/config.json
install -m 0755 bin/chickadee /usr/local/bin/chickadee
if [[ $catalog == False ]]; then
 cp -a images/. /var/lib/chickadee-image/
 chown -R root:root /var/lib/chickadee-image
 find /var/lib/chickadee-image -type d -exec chmod 0755 {} +
 find /var/lib/chickadee-image -type f -exec chmod 0444 {} +
fi
install -m 0755 scripts/network.sh scripts/preflight.sh scripts/profile-settings.py /usr/local/lib/chickadee/
install -m 0644 deploy/chickadee.service deploy/chickadee-network.service /etc/systemd/system/
mkdir -p /etc/systemd/system/chickadee.service.d
cgroup_enabled=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1])).get("cgroup") is not None)' "$config")
if [[ $cgroup_enabled == True ]]; then
  systemd_version=$(systemd --version | head -n1 | awk '{print $2}')
  [[ $systemd_version -ge 254 ]] || { echo 'Per-VM delegation requires systemd >=254.' >&2; exit 1; }
  install -m 0644 deploy/chickadee-cgroup.conf /etc/systemd/system/chickadee.service.d/cgroup.conf
fi

python3 scripts/profile-settings.py resources /etc/chickadee/config.json > /etc/systemd/system/chickadee.service.d/resources.conf
python3 scripts/profile-settings.py preflight /etc/chickadee/config.json
systemctl daemon-reload
printf '%s\n' 'Installed; service remains stopped. Store the App key, explicitly apply networking, then enable the services as documented.'
