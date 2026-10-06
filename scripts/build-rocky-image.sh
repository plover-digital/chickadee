#!/bin/bash
# Build Rocky 10.2 amd64 with its stock kernel for QEMU q35, using libguestfs.
set -euo pipefail
umask 077
cd "$(dirname "$0")/.."
out=${1:-images}
[[ $(uname -m) == x86_64 && ! -e $out/base.qcow2 ]]
for tool in go curl qemu-img virt-resize virt-customize guestfish python3; do command -v "$tool" >/dev/null; done
source_name=Rocky-10-GenericCloud-Base-10.2-20260525.0.x86_64.qcow2
source_sha=9fc9e9ff16888bb68ac39b0392e25c9c92684d50c85f1cce6ab549363bbc4b48
runner=2.337.0
runner_sha=70920811a4f8ad4328818682bca5c6469c1c942fab52448868071d0063816613
mkdir -p build/rocky-downloads "$out"
fetch() { curl -fsSL --retry 3 --connect-timeout 15 --max-time 600 --proto '=https' --tlsv1.2 "$1" -o "$2"; }
[[ -e build/rocky-downloads/source.qcow2 ]] || fetch "https://download.rockylinux.org/pub/rocky/10.2/images/x86_64/$source_name" build/rocky-downloads/source.qcow2
printf '%s  %s\n' "$source_sha" build/rocky-downloads/source.qcow2 | sha256sum --check
[[ -e build/rocky-downloads/runner.tar.gz ]] || fetch "https://github.com/actions/runner/releases/download/v$runner/actions-runner-linux-x64-$runner.tar.gz" build/rocky-downloads/runner.tar.gz
printf '%s  %s\n' "$runner_sha" build/rocky-downloads/runner.tar.gz | sha256sum --check
# Every package input is pinned by URL/hash. No live solver or package update
# runs during image construction. Cache inputs for upstream retention changes.
python3 - <<'PY'
import hashlib,json,pathlib,tarfile,time,urllib.request
root=pathlib.Path('build/rocky-downloads/rpms');root.mkdir(exist_ok=True)
entries=json.load(open('guest/images/rocky-102-rpms.json'))
for e in entries:
 name=e['name'];assert pathlib.Path(name).name==name and name.endswith('.rpm')
 p=root/name
 if not p.exists():
  for attempt in range(3):
   try:
    with urllib.request.urlopen(e['url'],timeout=60) as r,p.open('wb') as f:
     while chunk:=r.read(1024*1024):f.write(chunk)
    break
   except Exception:
    p.unlink(missing_ok=True)
    if attempt==2:raise
    time.sleep(1+attempt)
 assert hashlib.file_digest(p.open('rb'),'sha256').hexdigest()==e['sha256'], 'RPM checksum mismatch'
with tarfile.open('build/rocky-downloads/rpms.tar','w') as tar:
 for e in entries:tar.add(root/e['name'],arcname=e['name'])
PY
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath -ldflags='-s -w' -o build/chickadee-guest ./cmd/chickadee-guest
export LIBGUESTFS_BACKEND=direct
if [[ -r /dev/kvm && -w /dev/kvm ]]; then export LIBGUESTFS_BACKEND_SETTINGS=force_kvm; else export LIBGUESTFS_BACKEND_SETTINGS=force_tcg; fi
qemu-img create -q -f qcow2 build/rocky-base.qcow2 16G
virt-resize --quiet --expand /dev/sda4 build/rocky-downloads/source.qcow2 build/rocky-base.qcow2
# Root partition identity is stable; no firmware bootloader is used by QEMU.
guestfish -a build/rocky-base.qcow2 run : set-label /dev/sda4 chickadee
cat > build/rocky-provision.sh <<'PROVISION'
#!/bin/bash
set -euo pipefail
. /etc/os-release
[[ $ID == rocky && $VERSION_ID == 10.2 ]]
mkdir -p /tmp/rpms
tar -xf /tmp/rpms.tar -C /tmp/rpms
rpm --import /etc/pki/rpm-gpg/RPM-GPG-KEY-Rocky-10
dnf --disablerepo='*' --setopt=localpkg_gpgcheck=1 install -y /tmp/rpms/*.rpm
rm -rf /tmp/rpms /tmp/rpms.tar /var/cache/dnf/*
# Remove older distribution kernels so extraction is unambiguous.
kernel=$(ls -1 /lib/modules | sort -V | tail -1)
for installed in $(rpm -q kernel-core --qf '%{VERSION}-%{RELEASE}.%{ARCH}\n'); do
 if [[ $installed != "$kernel" ]]; then rpm -e "kernel-core-$installed" "kernel-modules-core-$installed"; fi
done
printf 'LABEL=chickadee / xfs defaults 0 1\n' > /etc/fstab
# Generic initrd includes PCI virtio transport for q35; no custom kernel needed.
dracut --force --no-hostonly --add-drivers 'virtio_pci virtio_blk virtio_net' "/boot/initramfs-$kernel.img" "$kernel"
if getent passwd 1000 >/dev/null; then userdel -r "$(getent passwd 1000 | cut -d: -f1)"; fi
if getent group 1000 >/dev/null; then groupdel "$(getent group 1000 | cut -d: -f1)"; fi
groupadd -g 1000 runner
useradd -u 1000 -g 1000 -m -s /bin/bash runner
passwd -l runner
mkdir -p /opt/actions-runner
tar -xzf /tmp/runner.tar.gz -C /opt/actions-runner
chown -R runner:runner /opt/actions-runner
rm -f /tmp/runner.tar.gz
chmod 0755 /usr/local/bin/chickadee-guest /usr/local/bin/chickadee-network
/opt/actions-runner/bin/Runner.Listener --version
systemctl mask serial-getty@ttyS0.service getty@tty1.service sshd.service NetworkManager.service NetworkManager-wait-online.service
systemctl enable chickadee-network.service chickadee-bootstrap.service
mkdir -p /etc/cloud
touch /etc/cloud/cloud-init.disabled
# SELinux remains enforcing; virt-customize relabels all uploaded files afterward.
grep -q '^SELINUX=enforcing' /etc/selinux/config
rm -f /etc/machine-id /var/lib/dbus/machine-id /etc/ssh/ssh_host_* /var/lib/systemd/random-seed
: > /etc/machine-id
rm -rf /var/lib/cloud/* /root/.ssh /home/runner/.ssh
rm -f /etc/resolv.conf
printf 'nameserver 1.1.1.1\n' > /etc/resolv.conf
printf 'chickadee\n' > /etc/hostname
rpm -qa | sort > /image-packages.txt
PROVISION
virt-customize --no-network --memsize 2048 -a build/rocky-base.qcow2 \
 --upload build/rocky-downloads/rpms.tar:/tmp/rpms.tar \
 --upload build/rocky-downloads/runner.tar.gz:/tmp/runner.tar.gz \
 --upload build/chickadee-guest:/usr/local/bin/chickadee-guest \
 --upload guest/network.sh:/usr/local/bin/chickadee-network \
 --upload guest/chickadee-network.service:/etc/systemd/system/chickadee-network.service \
 --upload guest/chickadee-bootstrap.service:/etc/systemd/system/chickadee-bootstrap.service \
 --upload build/rocky-provision.sh:/tmp/chickadee-provision.sh \
 --run-command 'bash /tmp/chickadee-provision.sh' \
 --delete /tmp/chickadee-provision.sh --selinux-relabel
# /boot is still a separate filesystem in the source GPT layout; inspect mounts
# it for extraction even though guests only need the root partition at runtime.
kernel=$(guestfish --ro -a build/rocky-base.qcow2 -m /dev/sda4 -m /dev/sda3:/boot glob-expand '/boot/vmlinuz-*')
[[ $kernel != *$'\n'* && $kernel == /boot/vmlinuz-* ]]
version=${kernel#/boot/vmlinuz-}
guestfish --ro -a build/rocky-base.qcow2 -m /dev/sda4 -m /dev/sda3:/boot download "$kernel" "$out/vmlinuz-$version"
guestfish --ro -a build/rocky-base.qcow2 -m /dev/sda4 -m /dev/sda3:/boot download "/boot/initramfs-$version.img" "$out/initrd-$version"
guestfish --ro -a build/rocky-base.qcow2 -m /dev/sda4 -m /dev/sda3:/boot download /image-packages.txt "$out/packages.txt"
ln -s "vmlinuz-$version" "$out/vmlinuz"
ln -s "initrd-$version" "$out/initrd"
mv build/rocky-base.qcow2 "$out/base.qcow2"
cp guest/images/rocky-102-rpms.json "$out/rpm-inputs.json"
printf '{"disk_gib":16,"os":"rocky","os_version":"10.2","architecture":"amd64","machine":"q35","minimum_cpu":"x86-64-v3","source_image":"%s","source_sha256":"%s","runner":"%s","kernel":"%s"}\n' "$source_name" "$source_sha" "$runner" "$version" > "$out/manifest.json"
(cd "$out" && sha256sum base.qcow2 "vmlinuz-$version" "initrd-$version" manifest.json packages.txt rpm-inputs.json > SHA256SUMS)
chmod 0444 "$out"/*
echo 'Rocky 10.2 q35 bundle built; perform READY and real-job validation before deployment.'
