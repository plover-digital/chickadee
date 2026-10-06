# Runner profiles on one host

The controller accepts the original flat config or a profile catalog. See
[examples/profiles.json](../examples/profiles.json) for a secret-free seven-queue
configuration with Ubuntu 24.04/26.04 and Rocky 10.2 images.

| Label | vCPU | RAM | OS |
| --- | --- | --- | --- |
| `chickadee` | 4 | 8 GiB | Ubuntu 26.04 |
| `chickadee-small-rocky-102` | 2 | 4 GiB | Rocky 10.2 |
| `chickadee-medium-rocky-102` | 4 | 8 GiB | Rocky 10.2 |
| `chickadee-small-ubuntu-2404` | 2 | 4 GiB | Ubuntu 24.04 |
| `chickadee-medium-ubuntu-2404` | 4 | 8 GiB | Ubuntu 24.04 |
| `chickadee-small-ubuntu-2604` | 2 | 4 GiB | Ubuntu 26.04 |
| `chickadee-medium-ubuntu-2604` | 4 | 8 GiB | Ubuntu 26.04 |

Use `runs-on: chickadee` for the default. The default is an explicit catalog
entry: operators can change its image/resource references on a controlled
stop/restart. An explicit versioned label remains stable. Compact versions are
catalog names, not a version parser. Store the dotted version in the image
manifest. GitHub selects any matching job; queue demand is not job affinity.

## Budgets and warm capacity

Each profile has its own scale set, assigned-demand stream, maximum and warm
target. All use one state lock, one actor, shared TAP slots and aggregate VM,
vCPU and guest RAM limits. Booting, ready, spent and retiring guests all count
until exit and disk cleanup. Per-profile maxima do not reserve capacity.

The example allows at most two VMs, six allocated vCPUs and 12 GiB of guest RAM.
That fits one medium plus one small or two small guests; two medium guests must
queue. Emulator/controller allowances add 1.5 GiB at maximum concurrency.
Preflight leaves at least 1 GiB of physical RAM for the host, with swap disabled
for the service. vCPUs share physical host CPUs; this is not dedicated-core
allocation. RAM is the QEMU allocation; guest MemTotal excludes firmware and
kernel reservations (the Rocky medium guest reports 7934 MiB of 8192 MiB
allocated). Reserve more host RAM where other services need it.

Only the default has a warm target of one; other profiles boot on demand. Do not
reserve a warm guest for every label on a small host. Older unsatisfied demand
has admission priority over new boots. The actor can retire optional
uncredentialed warm guests to admit another queue, but never evicts a
credentialed job to satisfy another profile. A larger waiting request prevents
smaller new boots from continuously consuming its required capacity. Queue
acquisition is bounded per scale set; aggregate scheduling can add queue latency
and GitHub may reassign requests if capacity stays unavailable.

## Image installation and upgrades

Build and validate each image bundle independently. Install trusted bundles:

```sh
sudo scripts/install-image.sh IMAGE_DIR /var/lib/chickadee-images/OS-VERSION/REVISION
```

Set each image's `path`, `os`, `version`, `machine` and `disk_gib` in the catalog. Preflight
checks root ownership, checksums, boot artifacts, OS/version/architecture,
filesystem size and required CPU features. Rocky and Ubuntu 26.04 use QEMU q35 with stock
kernels; Ubuntu 24.04 retains QEMU microvm. All boot directly with a versioned
kernel/initrd and use private serial control, KVM, fresh qcow2 overlays and
one-job cleanup. A comparative microvm investigation is deferred. Image revision paths are immutable;
never replace a base with overlays still in existence. There are no workflow
image URLs, guest access to host writable paths, snapshots or hot reload.

Install with `sudo scripts/install.sh config.json` after admitting all bundles.
For upgrades, wait for jobs to finish, stop the service, keep the private journal,
install new immutable bundles, validate the config and regenerate the resource
drop-in with `profile-settings.py resources`, then start the service. No host
network change is needed for an additional profile: the existing TAP slots are
shared. Do not clear the journal to resolve scope errors.

On restart, the controller reaps owned QEMU processes before disk deletion and
recovers journaled registrations even if their profile was removed or renamed.
Historical scale sets are looked up for cleanup and are not recreated. Changing
GitHub scope/group still fails closed. Removed scale-set definitions themselves
are not automatically deleted; the operator can retire them after registrations
and journal grace periods are reconciled.

## Validation scope

Unit/lifecycle tests cover legacy configuration, default resolution, routing,
shared CPU/RAM/concurrency limits, warm reclamation, spent-guest protection,
cleanup-before-TAP-reuse and removed-profile recovery. Actual image support
requires a real READY boot and one-job/cleanup/replacement validation. See the
release validation log for deployed evidence; catalog support alone is not proof
that every OS release has a validated image builder.
