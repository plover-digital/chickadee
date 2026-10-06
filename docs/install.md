# Install on Ubuntu 24.04 amd64

Use a dedicated Linux host with hardware virtualization enabled, `/dev/kvm`,
local storage, systemd, and outbound internet access. The reference installer
supports Ubuntu 24.04 amd64 only. Other Linux distributions need reviewed package,
firewall, service, and image-build adaptations. Nothing requires repartitioning:
an existing local SSD/NVMe filesystem can hold `/var/lib/chickadee`.

## Build

Install build and runtime dependencies on the intended Ubuntu machine:

```sh
sudo apt-get update
sudo apt-get install -y qemu-system-x86 qemu-utils libguestfs-tools isc-dhcp-client nftables \
  iproute2 util-linux python3 curl gpgv ubuntu-keyring xz-utils build-essential linux-image-generic
```

Install Go **1.26.3** from the official [Go downloads](https://go.dev/dl/) after
verifying its published checksum. The pinned scale-set module declares Go 1.25.3
and brings newer transitive module requirements; this project uses 1.26.3 for
its documented build and CI. Keep Go's default module checksum database enabled.

Clone this repository locally, then:

```sh
git clone https://github.com/plover-digital/chickadee.git
cd chickadee
make build test
mkdir -p build/appliance
builder_kernel=$(compgen -G '/boot/vmlinuz-*-generic' | sort -V | tail -n 1)
builder_kernel_version=${builder_kernel##*/vmlinuz-}
sudo install -m 0644 "$builder_kernel" build/appliance/vmlinuz
export SUPERMIN_KERNEL="$PWD/build/appliance/vmlinuz"
export SUPERMIN_KERNEL_VERSION="$builder_kernel_version"
export SUPERMIN_MODULES="/lib/modules/$builder_kernel_version"
make image
cp examples/config.json config.json
```

The dependency graph and verified checksums are committed in `go.mod` and
`go.sum`. Build/test use readonly module mode. Dependency changes should be
reviewed with their corresponding checksum changes.

Libguestfs/supermin needs a readable builder kernel and matching modules. Ubuntu
may install `/boot/vmlinuz-*` readable only by root. Copy a selected installed
kernel into `build/appliance/vmlinuz` using `sudo install -m 0644`, then set
`SUPERMIN_KERNEL`, `SUPERMIN_KERNEL_VERSION`, and `SUPERMIN_MODULES` to that copy
and its matching `/lib/modules/VERSION`. This avoids changing permissions on the
host kernel. The public image workflow includes these steps. See the
[upstream libguestfs FAQ](https://libguestfs.org/guestfs-faq.1.html).

The trusted image-building appliance uses QEMU's SLIRP outbound networking.
A build-local PATH shim disables libguestfs's automatic passt preference; no host
network tools are removed or modified. This is separate from job VM TAP/NAT.

The image build takes time and substantial temporary disk space. It uses
libguestfs with KVM where available and a software-emulation fallback for
**offline image editing**, without
mounting the image on the host. Job VMs always require KVM. Inputs are Ubuntu's
signed dated rootfs release `20260926`, Ubuntu's authenticated archive snapshot
`20260926T180000Z`, and GitHub runner `2.337.0` with a fixed SHA-256. The installed
kernel version, package list, initrd, manifest and image checksums are retained.
These are repeatable input versions, not a claim of bit-for-bit filesystem
reproducibility: filesystem UUIDs, package timestamps and initrd metadata vary.
Ubuntu snapshot retention is finite; archive the verified inputs for long-term
rebuilds. Check [Ubuntu snapshot documentation](https://snapshot.ubuntu.com/).

The base filesystem is 16 GiB. Keep `disk_gib` at 16 with this build. To change
it, update the image script and manifest, then rebuild the complete profile.
Never replace a backing image while overlays or guests exist. Build a clean
image set, stop the controller, reconcile state, and replace the whole set.

## GitHub App

For organization scope, the optional local helper prefills the minimum App
permissions and saves the generated key directly outside the checkout:

```sh
python3 scripts/setup-app.py --org YOUR_ORG
```

Open the printed localhost URL in a browser signed in as an organization
administrator, click **Create GitHub App**, then follow the installation link.
The helper detects the installation and writes `app.pem` (0600) and `app.json`
inside `~/.config/chickadee` (0700). It does not request a PAT, OAuth user access,
webhooks, or repository contents permissions. IDs and key path are collected
locally; never paste the key into chat or GitHub. App setup uses GitHub's official
[manifest flow](https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest).
The App is private to its owner; the source repository remains public.

If the browser is on another machine, forward the local port to the build host
first, for example `ssh -L 18734:127.0.0.1:18734 YOUR_HOST`, then open the printed
URL in your local browser. The helper binds only to 127.0.0.1 and exits after
verified installation or 30 minutes. If interrupted after creating the App, keep
the generated files and install that same App through GitHub's App settings;
do not create a duplicate just to recover its installation ID. A failed setup
may retain the key and initial metadata for recovery. It never overwrites existing
credentials. The IDs are private local configuration, not inputs to commit.

For manual setup or repository scope, follow these steps instead:

Create and install an organization-owned GitHub App for the intended scope.
Disable webhooks; chickadee polls. Repository scope needs Administration read/write
and Metadata read-only. Organization scope needs Self-hosted runners read/write;
limit the runner group's repository access. Install the App on the selected
repositories or organization, record its client ID (App ID also works) and
installation ID, then download an RSA private key. See the official
[App authentication guide](https://docs.github.com/en/actions/how-tos/manage-runners/use-actions-runner-controller/authenticate-to-the-api).

Set `github_url` to `https://github.com/ORG/REPO` or `https://github.com/ORG`;
enterprise scope, GHES and PAT authentication are outside this prototype.
Set `app_client_id`, `app_installation_id`, and the actual `runner_group_id`
(default group is commonly 1; verify your scope). Use a **dedicated** scale-set
name. Chickadee creates it if absent and refuses an existing set with auto-update
enabled. Do not share the set or state directory with another runner manager.

Keep the private key outside the checkout and generated image. The example
configuration only names `/etc/chickadee/app.pem`; no key is embedded in JSON.

## Install files and key

Review `config.json` and `scripts/install.sh` first. The supplied systemd unit
uses fixed paths `/var/lib/chickadee` and `/var/lib/chickadee-image`. The controller
source supports other paths; changing the unit and installer paths is a manual
adaptation. Start with warm pool 1 and maximum 2 VMs.

```sh
sudo ./scripts/install.sh config.json
sudo install -m 0600 -o chickadee -g chickadee /PATH/TO/APP-KEY.pem /etc/chickadee/app.pem
```

The service account can read the key; guest QEMU arguments, disk overlays, runner
environment and base image never contain App credentials. Only per-runner JIT
configuration crosses serial. Rotate the App key by installing a replacement
and restarting the controller. Restart interrupts running jobs.

The installer generates aggregate systemd CPU, memory and task limits from the
configuration. Recompute the drop-in before changing resource settings. Reserve
at least `max_vms * (disk_gib + 1)` GiB of free space plus the base image, logs and
host overhead. Use a dedicated filesystem or storage quota when isolation from
other host disk consumers matters. Btrfs users should keep runtime VM disks out
of automatic filesystem snapshots: snapshots retain deleted credentialed data.

## Explicitly apply host networking

Review `scripts/network.sh` and the existing firewall before proceeding. This is
a **host networking change**. The command creates 32 persistent TAP slots, routes
`10.203.SLOT.0/30`, installs owned nftables filtering/NAT tables, and enables IPv4
forwarding. Check for address-space collisions. Use an actual public egress
interface in place of `YOUR_WAN_INTERFACE`:

Check the generated filtering/NAT rules before applying them:

```sh
sudo ./scripts/network.sh plan YOUR_WAN_INTERFACE
```

`plan` runs nftables' kernel validation and prints the exact rules used by `apply`.
It does not create TAPs, change forwarding, or install firewall rules. It cannot
prove that an existing firewall will permit guest forwarding; review that
integration separately.

```sh
sudo /usr/local/lib/chickadee/network.sh apply YOUR_WAN_INTERFACE
sudo systemctl enable --now chickadee-network.service
sudo systemctl enable --now chickadee.service
sudo journalctl -u chickadee.service -f
```

The script preserves other nftables tables and saves the previous forwarding
setting for uninstall. Existing UFW/firewalld/nftables policies can still drop
forwarded traffic; `accept` in chickadee's chain cannot override another chain's
`drop`. Integration with an existing firewall needs explicit administrator
review. Do not flush the host firewall. On partial setup failure, stop the
service and run `network.sh remove` before reapplying.

Guests use static addresses and public DNS, and outbound IPv4 NAT. Filtering
blocks guest traffic to host-local addresses, private/reserved destinations,
other guests, and IPv6. The host management plane has no guest network listener.
The network service restores the reviewed rules and TAPs after reboot. Do not
flush or replace these rules while the controller runs.

## Execute the real vertical slice

After deployment, wait for `VM ready`. Warm guests should not appear as GitHub
runners. Deliberately copy `examples/smoke.yml` into the target repository's
`.github/workflows/chickadee-smoke.yml`, commit it, and dispatch it there. This
repository does not perform that write or publish anything automatically.

The first job runs on chickadee; the dependent second job asserts a different
runner name. In host logs confirm `VM reserved for one job`, destruction and
overlay removal, followed by replacement READY. Compare the runner-name suffix
to the VM ID: the old directory under `vms` must be absent and its name must no
longer be registered in GitHub. Inspect saved diagnostics. A green GitHub job
alone does not prove host cleanup. See [validation](validation.md) for additional
failure and restart checks.

## Uninstall

First stop the controller and let it clean up. Use cleanup-only mode with working
App credentials to reconcile saved registrations without starting jobs. Running
jobs are interrupted by stopping the service.

```sh
sudo systemctl stop chickadee.service
sudo -u chickadee /usr/local/bin/chickadee -cleanup -config /etc/chickadee/config.json
sudo ./scripts/uninstall.sh --remove
```

Cleanup retains recent intents for the ten-minute delayed-registration grace period;
repeat cleanup after that period before discarding state.

Uninstall removes owned services and networking, restoring the saved forwarding
value. It retains credentials, state, logs, images, service account and GitHub
scale set so you can inspect and finish registration cleanup. Remove the scale
set through GitHub administration after checking it has no active runners,
revoke/uninstall the App as appropriate, and deliberately delete retained local
artifacts after confirming no owned QEMU processes remain. Do not delete state
before runner reconciliation; the ownership journal is required for cleanup.

## Optional public image-build workflow

The manual `Build runner image` workflow runs the same `make image` recipe on
GitHub-hosted Ubuntu 24.04 and saves a short-lived artifact. It needs no App key
or deployment secret and does not change your host. Review the commit and successful
workflow before using its artifact; extract it into `images/`, verify `SHA256SUMS`,
and retain its provenance. This is a development artifact, not a signed release
or proof of a real-job deployment. Building locally remains the reference path.

## Check boot and destruction before configuring GitHub

On a KVM-capable host, use a separate scratch state directory to verify two real
independent boots reach READY and each QEMU/disk is destroyed. This mode does not
contact GitHub, read the App key, send JIT, create TAPs or change the host firewall.
Guest networking is blocked by QEMU's restricted user network. It does not prove
production NAT or real job execution.

```sh
mkdir -p build
python3 - <<'PY'
import json, os
c = json.load(open('examples/config.json'))
c['image_dir'] = os.path.abspath('images')
c['state_dir'] = '/tmp/ck-bootcheck'
json.dump(c, open('build/bootcheck.json', 'w'))
PY
bin/chickadee -boot-check -config build/bootcheck.json
```

Run this command as a normal user with KVM access, not with sudo.
If boot fails, inspect private QEMU diagnostics in the scratch `logs/` directory.
