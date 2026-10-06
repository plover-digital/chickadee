#!/bin/bash
# Ubuntu 26.04 with its stock generic kernel, direct boot on QEMU q35.
# Image editing uses libguestfs; no host mounts or networking changes.
set -euo pipefail
umask 077
cd "$(dirname "$0")/.."
out=${1:-images/ubuntu-2604}
profile=${2:-minimal}
[[ $profile == minimal || $profile == developer ]]
[[ $(uname -m) == x86_64 && ! -e $out/base.qcow2 ]]
for tool in go curl gpgv qemu-img virt-make-fs virt-customize virt-sparsify guestfish xz python3; do command -v "$tool" >/dev/null; done
work=build/ubuntu-2604
downloads=build/ubuntu-2604-downloads
mkdir -p "$work" "$downloads" "$out"
disk_gib=16
uploads=()
if [[ $profile == developer ]]; then
 disk_gib=48
 python3 scripts/fetch-image-tools.py guest/images/github-ubuntu-2604.json build/github-compatible/downloads
 python3 guest/install-github-extra-tools.py --lock guest/images/github-extra-ubuntu-2604.json --download build/github-compatible/extra-downloads
 python3 - <<'PY'
import json,pathlib,tarfile
with tarfile.open('build/ubuntu-2604/tools.tar','w') as archive:
 for t in json.load(open('guest/images/github-ubuntu-2604.json'))['tools']:
  archive.add(pathlib.Path('build/github-compatible/downloads')/t['filename'],arcname=t['filename'])
with tarfile.open('build/ubuntu-2604/extra-tools.tar','w') as archive:
 for t in json.load(open('guest/images/github-extra-ubuntu-2604.json'))['tools']:
  archive.add(pathlib.Path('build/github-compatible/extra-downloads')/t['filename'],arcname=t['filename'])
PY
 uploads=(--upload guest/images/github-ubuntu-2604.json:/tmp/ubuntu-2604-tool-lock.json
 --upload "$work/tools.tar":/tmp/ubuntu-2604-tools.tar
 --upload guest/install-ubuntu-2604-tools.py:/tmp/install-ubuntu-2604-tools.py
 --upload guest/images/github-extra-ubuntu-2604.json:/tmp/ubuntu-2604-extra-lock.json
 --upload "$work/extra-tools.tar":/tmp/ubuntu-2604-extra-tools.tar
 --upload guest/install-github-extra-tools.py:/tmp/install-github-extra-tools.py)
fi
read -r release snapshot root_tar root_sha signer runner runner_sha < <(python3 - <<'PY'
import json
c=json.load(open('guest/images/ubuntu-2604.json'))
print(*(c[k] for k in ['release','snapshot','root_filename','root_sha256','signing_fingerprint','runner_version','runner_sha256']))
PY
)
url="https://cloud-images.ubuntu.com/releases/resolute/release-$release"
fetch() { curl -fsSL --retry 3 --connect-timeout 15 --max-time 600 --proto '=https' --tlsv1.2 "$1" -o "$2"; }
for file in SHA256SUMS SHA256SUMS.gpg "$root_tar"; do
  [[ -e $downloads/$file ]] || fetch "$url/$file" "$downloads/$file"
done
gpgv --status-fd 1 --keyring /usr/share/keyrings/ubuntu-cloudimage-keyring.gpg "$downloads/SHA256SUMS.gpg" "$downloads/SHA256SUMS" > "$work/signature-status"
grep -q "^\[GNUPG:\] VALIDSIG $signer " "$work/signature-status"
python3 - <<'PY'
import hashlib,json,pathlib
lock=json.load(open('guest/images/ubuntu-2604.json'));root=pathlib.Path('build/ubuntu-2604-downloads')
checks=[line.split()[0] for line in (root/'SHA256SUMS').read_text().splitlines() if line.split()[-1].lstrip('*')==lock['root_filename']]
assert checks==[lock['root_sha256']], 'signed root checksum differs from pinned lock'
with (root/lock['root_filename']).open('rb') as data:assert hashlib.file_digest(data,'sha256').hexdigest()==lock['root_sha256']
PY
[[ -e $downloads/runner.tar.gz ]] || fetch "https://github.com/actions/runner/releases/download/v$runner/actions-runner-linux-x64-$runner.tar.gz" "$downloads/runner.tar.gz"
printf '%s  %s\n' "$runner_sha" "$downloads/runner.tar.gz" | sha256sum --check
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath -ldflags='-s -w' -o "$work/chickadee-guest" ./cmd/chickadee-guest
xz --decompress --stdout "$downloads/$root_tar" > "$work/root.tar"
export LIBGUESTFS_BACKEND=direct
if [[ -r /dev/kvm && -w /dev/kvm ]]; then export LIBGUESTFS_BACKEND_SETTINGS=force_kvm; else export LIBGUESTFS_BACKEND_SETTINGS=force_tcg; fi
mkdir -p "$work/appliance-bin"
printf '#!/bin/sh\nexit 127\n' > "$work/appliance-bin/passt"
chmod 0755 "$work/appliance-bin/passt"
export PATH="$PWD/$work/appliance-bin:$PATH"
virt-make-fs --format=qcow2 --type=ext4 --size="${disk_gib}G" "$work/root.tar" "$work/base.qcow2"
cat > "$work/provision.sh" <<'PROVISION'
#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
. /etc/os-release
[[ $ID == ubuntu && $VERSION_ID == 26.04 ]]
sed -i 's/^hosts:.*/hosts: files dns/' /etc/nsswitch.conf
python3 - <<'PY'
import json,pathlib
c=json.load(open('/tmp/ubuntu-2604-inputs.json'));snapshot=c['snapshot']
for p in pathlib.Path('/etc/apt/sources.list.d').glob('*'):p.unlink()
pathlib.Path('/etc/apt/sources.list').write_text(''.join(f'deb [signed-by=/usr/share/keyrings/ubuntu-archive-keyring.gpg check-valid-until=no] https://snapshot.ubuntu.com/ubuntu/{snapshot} {suite} main universe restricted multiverse\n' for suite in ['resolute','resolute-updates','resolute-security']))
PY
apt-get -o Acquire::Retries=3 -o APT::Update::Error-Mode=any update
packages=$(python3 -c 'import json; print(" ".join(json.load(open("/tmp/ubuntu-2604-inputs.json"))["minimal_packages"]))')
apt-get install -y --no-install-recommends $packages
e2label /dev/sda chickadee
printf 'LABEL=chickadee / ext4 defaults 0 1\n' > /etc/fstab
printf 'virtio_pci\nvirtio_blk\nvirtio_net\nvirtio_rng\n' >> /etc/initramfs-tools/modules
for config in /boot/config-*; do
 grep -Eq '^CONFIG_VIRTIO_PCI=(y|m)' "$config"
 grep -q '^CONFIG_SERIAL_8250_CONSOLE=y' "$config"
done
update-initramfs -u -k all
if getent passwd 1000 >/dev/null; then userdel -r "$(getent passwd 1000 | cut -d: -f1)"; fi
if getent group 1000 >/dev/null; then groupdel "$(getent group 1000 | cut -d: -f1)"; fi
groupadd -g 1000 runner
useradd -u 1000 -g 1000 -m -s /bin/bash runner
passwd -l runner
mkdir -p /home/runner/work /opt/actions-runner
tar -xzf /tmp/runner.tar.gz -C /opt/actions-runner
chown -R runner:runner /home/runner/work /opt/actions-runner
printf 'runner ALL=(ALL) NOPASSWD:ALL\n' > /etc/sudoers.d/runner
chmod 0440 /etc/sudoers.d/runner
if [[ -e /tmp/ubuntu-2604-tool-lock.json ]]; then
 python3 /tmp/install-ubuntu-2604-tools.py
 mkdir -p /tmp/ubuntu-2604-extras
 tar -xf /tmp/ubuntu-2604-extra-tools.tar -C /tmp/ubuntu-2604-extras
 python3 /tmp/install-github-extra-tools.py --lock /tmp/ubuntu-2604-extra-lock.json --install /tmp/ubuntu-2604-extras
 rm -rf /tmp/ubuntu-2604-extras
 rm -f /tmp/install-ubuntu-2604-tools.py /tmp/install-github-extra-tools.py /tmp/ubuntu-2604-extra-tools.tar /tmp/ubuntu-2604-tool-lock.json /tmp/ubuntu-2604-extra-lock.json
fi
runuser -u runner -- /opt/actions-runner/bin/Runner.Listener --version
rm -rf /opt/actions-runner/_diag
rm -f /tmp/runner.tar.gz /tmp/ubuntu-2604-inputs.json
systemctl mask serial-getty@ttyS0.service getty@tty1.service ssh.service ssh.socket systemd-networkd.service systemd-resolved.service apt-daily.service apt-daily-upgrade.service
systemctl enable chickadee-network.service chickadee-bootstrap.service
mkdir -p /etc/cloud
touch /etc/cloud/cloud-init.disabled
rm -f /etc/machine-id /var/lib/dbus/machine-id /etc/ssh/ssh_host_* /var/lib/systemd/random-seed
: > /etc/machine-id
apt-get clean
rm -rf /var/lib/cloud/* /var/lib/apt/lists/* /root/.ssh /home/runner/.ssh
rm -f /etc/resolv.conf
printf 'nameserver 1.1.1.1\n' > /etc/resolv.conf
printf 'chickadee\n' > /etc/hostname
chmod 0755 /usr/local/bin/chickadee-guest /usr/local/bin/chickadee-network
dpkg-query -W > /image-packages.txt
PROVISION
virt-customize --memsize 2048 -a "$work/base.qcow2" \
 "${uploads[@]}" \
 --upload "$downloads/runner.tar.gz":/tmp/runner.tar.gz \
 --upload guest/images/ubuntu-2604.json:/tmp/ubuntu-2604-inputs.json \
 --upload "$work/chickadee-guest":/usr/local/bin/chickadee-guest \
 --upload guest/network.sh:/usr/local/bin/chickadee-network \
 --upload guest/chickadee-network.service:/etc/systemd/system/chickadee-network.service \
 --upload guest/chickadee-bootstrap.service:/etc/systemd/system/chickadee-bootstrap.service \
 --upload "$work/provision.sh":/tmp/chickadee-provision.sh \
 --run-command 'bash /tmp/chickadee-provision.sh' \
 --delete /tmp/chickadee-provision.sh --delete /etc/resolv.conf \
 --write /etc/resolv.conf:"nameserver 1.1.1.1"
guestfish --ro -a "$work/base.qcow2" -m /dev/sda download /image-packages.txt "$out/packages.txt"
kernel=$(guestfish --ro -a "$work/base.qcow2" -m /dev/sda glob-expand '/boot/vmlinuz-*')
[[ $kernel != *$'\n'* && $kernel == /boot/vmlinuz-* ]]
version=${kernel#/boot/vmlinuz-}
guestfish --ro -a "$work/base.qcow2" -m /dev/sda download "$kernel" "$out/vmlinuz-$version"
guestfish --ro -a "$work/base.qcow2" -m /dev/sda download "/boot/initrd.img-$version" "$out/initrd-$version"
ln -s "vmlinuz-$version" "$out/vmlinuz"
ln -s "initrd-$version" "$out/initrd"
# Compact zero/sparse space so startup integrity hashing reads actual content,
# rather than the virtual 48 GiB address range emitted by virt-make-fs.
virt-sparsify --in-place "$work/base.qcow2"
qemu-img convert -O qcow2 "$work/base.qcow2" "$out/base.qcow2"
rm "$work/base.qcow2"
cp guest/images/ubuntu-2604.json "$out/build-inputs.json"
extras=()
if [[ $profile == developer ]]; then
 cp guest/images/github-ubuntu-2604.json "$out/tool-inputs.json"
 cp guest/images/github-extra-ubuntu-2604.json "$out/extra-inputs.json"
 guestfish --ro -a "$out/base.qcow2" -m /dev/sda download /etc/chickadee/image-compatibility.json "$out/compatibility.json"
 extras=(tool-inputs.json extra-inputs.json compatibility.json)
fi
printf '{"disk_gib":%s,"os":"ubuntu","os_version":"26.04","architecture":"amd64","machine":"q35","ubuntu_release":"%s","snapshot":"%s","runner":"%s","kernel":"%s","software_profile":"%s"}\n' "$disk_gib" "$release" "$snapshot" "$runner" "$version" "$profile" > "$out/manifest.json"
(cd "$out" && sha256sum base.qcow2 "vmlinuz-$version" "initrd-$version" manifest.json packages.txt build-inputs.json "${extras[@]}" > SHA256SUMS)
chmod 0444 "$out"/*
echo 'Ubuntu 26.04 q35 bundle built; require READY and real-job validation before changing a default.'
