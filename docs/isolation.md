# Per-VM QEMU isolation

QEMU now starts through mandatory bubblewrap namespaces. There is no automatic
unsandboxed fallback. Install the distribution's `bubblewrap` package; run
`chickadee -check-sandbox` as the service user before starting the controller.
The installed preflight runs this check under the controller's systemd hardening.
A denied user namespace, missing KVM permission or unavailable sandbox fails startup.
Do not disable AppArmor or systemd hardening globally to bypass a failed check.

The launcher creates private mount, PID, network, IPC and UTS namespaces with a
user namespace, disables creation of further user namespaces, drops capabilities, clears the environment and starts a new
session. Bubblewrap applies no-new-privileges. QEMU's existing seccomp policy
remains enabled. The filesystem contains only:

- Read-only QEMU executable, distribution runtime-library trees and firmware.
- Read-only `base.qcow2`, kernel and initrd from the selected immutable bundle.
- The selected VM's writable directory, including overlay and serial socket.
- Private `/proc`, `/tmp` and basic devices, plus explicit KVM and random devices.

Controller keys, configuration, sibling VM directories, host `/proc`, management
sockets and home directories are absent. The controller opens an existing owned
TAP and passes its descriptor to QEMU. The TAP remains in the host network
namespace, with the existing NAT policy; QEMU's namespace has only loopback.
QEMU receives neither `/dev/net/tun` nor network-administration capabilities.
Boot checks use restricted userspace networking and do not open a TAP.

The bubblewrap guardian supplies the actual sandbox child PID through a bounded
private descriptor. The controller obtains a pidfd and confirms the child's
whole thread group has exited before deleting its overlay. Guardian termination
alone does not release the disk or pool capacity. Missing process identity or an
unconfirmed child exit retains the disk and stops unsafe cleanup.

A failed GitHub demand listener clears demand for that queue and retries with
bounded backoff. It does not cancel already credentialed VMs or other queues.
Queue removal and reload retain the existing one-job lifecycle rules.

## Validation and rollout

Host tests probe denied access to synthetic key/sibling files, immutable image
writes, controller PIDs and host network interfaces; they verify access to the
own VM directory and KVM. Lifecycle tests hold a simulated QEMU child alive after
its guardian exits and confirm the overlay remains until actual child exit.
The executable preflight uses the same mount policy without credentials or a VM.

Before a binary upgrade, drain jobs and retain the previous binary/configuration
for rollback. Run the preflight under an isolated systemd unit with the production
hardening, then run two credential-free READY boot checks with a fresh private
state directory and an immutable image. Verify child exit and overlay deletion.
Finally validate an existing TAP with a real one-job runner and replacement;
network isolation and descriptor behavior need actual host acceptance.

A Fedora 44 acceptance run completed both offline READY boots with SELinux
enforcing and the candidate hardening: each actual QEMU child exited and its
overlay was deleted. The namespace-only preflight also confirmed denied host
sysctl write access without changing a value. These checks did not open a TAP
or register a GitHub runner; a real job/network acceptance remains required.

This is an additional isolation layer, not completed hostile multi-tenant
release acceptance. KVM, QEMU and host-kernel vulnerabilities remain in scope.
Runtime-library and firmware trees must be root-owned and contain no secrets.
Per-VM cgroup limits, storage quotas, broader negative-network testing, log
archival and an independent security review remain required before stronger
production isolation or availability claims.

The documented Ubuntu 24.04 package provides bubblewrap 0.9.0; the required
namespace and monitoring options are present in its upstream manual. QEMU 8.2
supports inherited TAP descriptors. Its `fd=` backend rejects `script` and
`downscript` options, so the launcher omits both; that backend does not apply
default network scripts. `ProtectKernelTunables=yes` masks the parent proc and prevents the required
unprivileged private-proc mount on the tested Fedora host. The candidate unit
therefore uses `ProtectKernelTunables=no`, retaining the unprivileged service UID,
empty capability set, no-new-privileges, strict filesystem protection and kernel
module/control-group protection. Preflight verifies zero effective host
capabilities and denied write access to a representative host kernel sysctl by
opening it without writing any value. This is a scoped service policy; no host
sysctl or SELinux enforcement is changed. Live services require a reviewed upgrade.

Sources: [Ubuntu package metadata](https://launchpad.net/ubuntu/noble/+package/bubblewrap),
[bubblewrap 0.9.0 options](https://github.com/containers/bubblewrap/blob/v0.9.0/bwrap.xml),
[QEMU 8.2 TAP implementation](https://github.com/qemu/qemu/blob/v8.2.0/net/tap.c),
[bubblewrap policy and limitations](https://github.com/containers/bubblewrap),
[bubblewrap implementation](https://github.com/containers/bubblewrap/blob/main/bubblewrap.c).
