#!/bin/bash
# Build on Ubuntu 24.04 amd64. No mount, chroot, or host network changes.
set -euo pipefail
umask 077
cd "$(dirname "$0")/.."
[[ $(uname -m) == x86_64 ]]
for tool in go curl gpgv qemu-img virt-make-fs virt-customize guestfish xz; do command -v "$tool" >/dev/null; done
release=20260926
snapshot=20260926T180000Z
runner=2.337.0
runner_sha=70920811a4f8ad4328818682bca5c6469c1c942fab52448868071d0063816613
root_tar=ubuntu-24.04-server-cloudimg-amd64-root.tar.xz
url="https://cloud-images.ubuntu.com/releases/noble/release-$release"
mkdir -p build/downloads images
[[ ! -e images/base.qcow2 ]] || { echo 'images/base.qcow2 already exists; use a clean build directory.' >&2; exit 1; }
for file in SHA256SUMS SHA256SUMS.gpg "$root_tar"; do
  curl --fail --location --proto '=https' --tlsv1.2 "$url/$file" -o "build/downloads/$file"
done
gpgv --keyring /usr/share/keyrings/ubuntu-cloudimage-keyring.gpg build/downloads/SHA256SUMS.gpg build/downloads/SHA256SUMS
python3 - "$root_tar" <<'PY'
import hashlib, pathlib, sys
name=sys.argv[1]; directory=pathlib.Path('build/downloads')
lines=(directory/'SHA256SUMS').read_text().splitlines()
checks=[line.split()[0] for line in lines if line.split()[-1].lstrip('*')==name]
assert len(checks)==1, 'missing or ambiguous signed checksum'
with (directory/name).open('rb') as f: digest=hashlib.file_digest(f,'sha256').hexdigest()
assert digest==checks[0], 'Ubuntu root checksum mismatch'
PY
curl --fail --location --proto '=https' --tlsv1.2 "https://github.com/actions/runner/releases/download/v$runner/actions-runner-linux-x64-$runner.tar.gz" -o build/downloads/runner.tar.gz
printf '%s  %s\n' "$runner_sha" build/downloads/runner.tar.gz | sha256sum --check
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath -ldflags='-s -w' -o build/chickadee-guest ./cmd/chickadee-guest
xz --decompress --stdout "build/downloads/$root_tar" > build/root.tar
export LIBGUESTFS_BACKEND=direct
# Software emulation is sufficient for offline image editing, never for job VMs.
export LIBGUESTFS_BACKEND_SETTINGS=force_tcg
# libguestfs prefers passt whenever it is runnable. Keep the trusted builder on
# QEMU's outbound SLIRP fallback without changing installed host network tools.
mkdir -p build/appliance-bin
printf '#!/bin/sh\nexit 127\n' > build/appliance-bin/passt
chmod 0755 build/appliance-bin/passt
export PATH="$PWD/build/appliance-bin:$PATH"
virt-make-fs --format=qcow2 --type=ext4 --size=16G build/root.tar build/base.qcow2
cat > build/provision.sh <<PROVISION
#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
rm -f /etc/apt/sources.list /etc/apt/sources.list.d/*
printf '%s\n' 'deb [signed-by=/usr/share/keyrings/ubuntu-archive-keyring.gpg check-valid-until=no] https://snapshot.ubuntu.com/ubuntu/$snapshot noble main universe' 'deb [signed-by=/usr/share/keyrings/ubuntu-archive-keyring.gpg check-valid-until=no] https://snapshot.ubuntu.com/ubuntu/$snapshot noble-updates main universe' 'deb [signed-by=/usr/share/keyrings/ubuntu-archive-keyring.gpg check-valid-until=no] https://snapshot.ubuntu.com/ubuntu/$snapshot noble-security main universe' > /etc/apt/sources.list
apt-get update
apt-get install -y --no-install-recommends linux-image-generic initramfs-tools ca-certificates git curl build-essential iproute2 libicu74 libssl3t64 zlib1g libkrb5-3 liblttng-ust1t64
# The imported root tar has no VM partition table or bootloader requirement.
e2label /dev/sda chickadee
printf 'LABEL=chickadee / ext4 defaults 0 1\n' > /etc/fstab
printf 'virtio_mmio\nvirtio_blk\nvirtio_net\nvirtio_rng\n' >> /etc/initramfs-tools/modules
for kernel_config in /boot/config-*; do
  grep -q '^CONFIG_VIRTIO_MMIO_CMDLINE_DEVICES=y' "\$kernel_config"
  grep -Eq '^CONFIG_VIRTIO_MMIO=(y|m)' "\$kernel_config"
done
update-initramfs -u -k all
if getent passwd 1000 >/dev/null; then userdel -r "\$(getent passwd 1000 | cut -d: -f1)"; fi
if getent group 1000 >/dev/null; then groupdel "\$(getent group 1000 | cut -d: -f1)"; fi
groupadd -g 1000 runner
useradd -u 1000 -g 1000 -m -s /bin/bash runner
passwd -l runner
mkdir -p /opt/actions-runner
 tar -xzf /tmp/runner.tar.gz -C /opt/actions-runner
chown -R runner:runner /opt/actions-runner
rm -f /tmp/runner.tar.gz
systemctl mask serial-getty@ttyS0.service getty@tty1.service ssh.service ssh.socket systemd-networkd.service systemd-resolved.service apt-daily.service apt-daily-upgrade.service
systemctl enable chickadee-network.service chickadee-bootstrap.service
mkdir -p /etc/cloud
touch /etc/cloud/cloud-init.disabled
rm -f /etc/machine-id /var/lib/dbus/machine-id /etc/ssh/ssh_host_* /var/lib/systemd/random-seed
: > /etc/machine-id
rm -rf /var/lib/cloud/* /var/lib/apt/lists/* /root/.ssh /home/runner/.ssh
rm -f /etc/resolv.conf
printf 'nameserver 1.1.1.1\n' > /etc/resolv.conf
printf 'chickadee\n' > /etc/hostname
chmod 0755 /usr/local/bin/chickadee-guest /usr/local/bin/chickadee-network
dpkg-query -W > /image-packages.txt
PROVISION
virt-customize -a build/base.qcow2 \
  --upload build/downloads/runner.tar.gz:/tmp/runner.tar.gz \
  --upload build/chickadee-guest:/usr/local/bin/chickadee-guest \
  --upload guest/network.sh:/usr/local/bin/chickadee-network \
  --upload guest/chickadee-network.service:/etc/systemd/system/chickadee-network.service \
  --upload guest/chickadee-bootstrap.service:/etc/systemd/system/chickadee-bootstrap.service \
  --run build/provision.sh
# Inspect the sole installed kernel, then retain versioned artifacts plus fixed names for QEMU.
guestfish --ro -a build/base.qcow2 -m /dev/sda download /image-packages.txt images/packages.txt
kernel=$(guestfish --ro -a build/base.qcow2 -m /dev/sda glob-expand '/boot/vmlinuz-*')
[[ $kernel != *$'\n'* && $kernel == /boot/vmlinuz-* ]]
version=${kernel#/boot/vmlinuz-}
guestfish --ro -a build/base.qcow2 -m /dev/sda download "$kernel" "images/vmlinuz-$version"
guestfish --ro -a build/base.qcow2 -m /dev/sda download "/boot/initrd.img-$version" "images/initrd-$version"
ln -s "vmlinuz-$version" images/vmlinuz
ln -s "initrd-$version" images/initrd
mv build/base.qcow2 images/base.qcow2
printf '{"disk_gib":16,"ubuntu_release":"%s","snapshot":"%s","runner":"%s","kernel":"%s"}\n' "$release" "$snapshot" "$runner" "$version" > images/manifest.json
(cd images && sha256sum base.qcow2 "vmlinuz-$version" "initrd-$version" manifest.json packages.txt > SHA256SUMS)
chmod 0444 images/*
echo 'Image built. Keep the manifest and checksums with these immutable artifacts.'
