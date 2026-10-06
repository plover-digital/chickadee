# Primary-source design audit

Research date: 2026-10-05. Discovery and retrieval used the Exa plugin: 13 searches
across four workstreams (GitHub, QEMU, image/storage, host lifecycle/networking),
65 returned results, 63 distinct URLs before consolidating versioned mirrors.
Third-party tutorials and promotional pages were excluded from the conclusions.
The references below are upstream documentation or source. Search-result similarity
was not treated as proof; relevant pages and pinned client code were checked.

The core design follows documented ephemeral-runner and microvm primitives. This
is **not** a security certification or proof that the deployment works. There are
concrete prototype limitations, including a shared QEMU/controller UID, aggregate
rather than per-VM cgroups, best-effort log export and unverified real-host networking.

| Area | Upstream guidance / behavior | Chickadee assessment |
|---|---|---|
| One-job lifetime | GitHub recommends ephemeral autoscaling and assigns an ephemeral runner one job. | JIT scale-set runner; credentialed VM always destroyed. Real GitHub execution still pending. |
| Demand | The scale-set client describes `TotalAssignedJobs` as aggregate demand; individual messages can be truncated. Available jobs require acquisition. | Statistics, capacity header, AcquireJobs and post-processing acknowledgment; no VM-to-job binding. |
| App scope | Organization runners require the App's self-hosted-runners permission. | Host-only App key; plover-digital profile uses organization URL. Actual App and group still need provisioning. |
| Public repositories | GitHub advises against exposing self-hosted runners to public fork workloads; groups default to private repositories. | Default public CI uses hosted runners. Self-hosting is opt-in for reviewed main-branch jobs, with group repository/workflow restrictions. |
| microvm | QEMU supports microvm with KVM, host kernel/initrd and virtio-mmio; PCI/ACPI features are absent. | Direct boot, KVM required for job VMs; no snapshots or suspend. Generic kernel features are checked in image build. |
| Serial startup | A listening socket with `wait=on` blocks startup until a client connects. | Host connects before boot; guest disables echo, sends READY once, and waits for CONFIG. Boot-only check exercises this. |
| Process security | QEMU recommends unprivileged processes and access limited to each guest's own resources; seccomp is only one isolation layer. | Non-root and seccomp enabled, but a shared UID can access the host App key and other disks after an emulator escape. Stronger per-VM filesystem/process isolation is required before a managed service. |
| Exit proof | Process pidfds become ready only once all threads in that process have exited. | Recovery uses pidfd signaling **and polling**, then deletes disks. A real-process test covers the wait. |
| Resources | systemd quotas apply to the unit and its processes; swap has a separate limit. | Aggregate CPU/RAM/task quotas, swap disabled, guest vCPU/RAM bounds, and per-QEMU disk file-size cap. Not per-VM CPU/host-overhead cgroups or traffic shaping. |
| Backing storage | Updating a backing image corrupts dependent overlays; live images must not be modified with qemu-img. | Base installed root-owned/read-only; never commit/rebase live disks; overlays deleted after exit. No secure-erase promise. |
| Provenance | Ubuntu supplies signed checksums and fixed-time archive snapshots. | GPG-verified dated rootfs, authenticated snapshot packages, pinned runner SHA-256, versioned kernel/initrd and output manifest. Repeatable inputs, not bitwise image identity or release attestation. |
| Firewall composition | nftables accept in one base chain does not override a later drop; drop is final. | Owned early filtering/NAT tables coexist with host rules. Production forwarding and host-service isolation require actual acceptance tests. |
| Logs | GitHub recommends preserving ephemeral runner diagnostics externally. | Bounded private host logs are best effort after job exit. Crash-safe streaming and restricted archival remain managed-service work. |

## Concrete changes from this audit

- Recovery polls the pidfd instead of trusting `/proc` leader zombie state.
- The GitHub client disables automatic HTTP retries so an ambiguous JIT POST is not
  silently replayed. Its registration intent survives failure; a different VM is
  used for a subsequent attempt.
- The systemd service sets `MemorySwapMax=0` alongside `MemoryMax`.
- Controller/VM startup refuses root execution, including boot-only checks.
- Runner intents bind to the original GitHub URL, group and scale-set name; a
  changed scope fails closed before discarding old state.
- Runtime directories must be private, owned by the controller and not symlinks;
  immutable image and runtime paths cannot overlap. VM directories are created
  exclusively so a collision cannot overwrite an existing disk.
- Hypervisor and image-tool subprocesses get a minimal environment, dropping
  controller token/cloud credentials; a subprocess test checks the boundary.
- Organization deployment documentation includes public-repository access and
  selected-workflow policy prerequisites rather than assuming a label is a security
  boundary. No existing organization policy is silently modified.
- The trust model names shared-UID exposure and per-VM resource isolation gaps as
  managed-service blockers; KVM plus seccomp alone is not claimed to solve them.

## Primary references

1. [GitHub self-hosted runner lifecycle, updates and authentication](https://docs.github.com/en/actions/reference/runners/self-hosted-runners)
2. [GitHub secure use: self-hosted runner hardening](https://docs.github.com/en/actions/reference/secure-use-reference#hardening-for-self-hosted-runners)
3. [GitHub runner group repository/workflow access](https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/manage-access)
4. [GitHub App authentication permissions](https://docs.github.com/en/actions/how-tos/manage-runners/use-actions-runner-controller/authenticate-to-the-api)
5. [Pinned scaleset v0.4.0 README](https://github.com/actions/scaleset/blob/v0.4.0/README.md), [client](https://github.com/actions/scaleset/blob/v0.4.0/client.go), [sessions](https://github.com/actions/scaleset/blob/v0.4.0/session_client.go), [HTTP options](https://github.com/actions/scaleset/blob/v0.4.0/common_client.go)
6. [QEMU microvm machine](https://www.qemu.org/docs/master/system/i386/microvm.html)
7. [QEMU security architecture and supported virtualization use cases](https://www.qemu.org/docs/master/system/security.html)
8. [QEMU invocation: chardev and sandbox](https://www.qemu.org/docs/master/system/invocation.html)
9. [QEMU image utility warnings](https://www.qemu.org/docs/master/tools/qemu-img.html) and [backing-image immutability](https://wiki.qemu.org/Documentation/CreateSnapshot)
10. [Linux pidfd_open readiness semantics](https://man7.org/linux/man-pages/man2/pidfd_open.2.html) and [pidfd_send_signal](https://man7.org/linux/man-pages/man2/pidfd_send_signal.2.html)
11. [systemd resource control](https://www.freedesktop.org/software/systemd/man/latest/systemd.resource-control.html) and [execution isolation](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html)
12. [nftables base-chain verdict and priority semantics](https://wiki.nftables.org/wiki-nftables/index.php/Configuring_chains)
13. [Ubuntu cloud image artifact verification](https://ubuntu.com/docs/public-images/public-images-reference/artifacts/) and [snapshot service](https://snapshot.ubuntu.com/)

Upstream `master` manuals can describe newer versions than Ubuntu 24.04's packages.
They establish design guidance, not feature availability on the installed host.
The reference host and pinned image must still pass [real-host acceptance](validation.md).

The first public image build verified Ubuntu's GPG signature and the runner
checksum, then failed constructing the supermin appliance. Upstream libguestfs
documents Ubuntu's root-only kernel permissions and supports explicit kernel and
module overrides. The builder now copies a kernel into its own build directory
rather than changing `/boot` permissions. This cause is an informed diagnosis;
the corrected workflow must demonstrate successful appliance construction.
[libguestfs FAQ](https://libguestfs.org/guestfs-faq.1.html),
[supermin kernel overrides](https://libguestfs.org/supermin.1.html).

Workflow action pins were also updated to current official Node 24 releases after
GitHub CI reported deprecated Node 20 action runtimes.

The readable-kernel fix advanced the image build past appliance construction.
The next failure was libguestfs's automatic `passt` startup. Its upstream direct
backend probes `passt --help` and uses SLIRP when the helper is unavailable. The
recipe now applies a private, build-only PATH shim that reports `passt` unavailable,
selecting the upstream SLIRP fallback without uninstalling or modifying host tools.
This affects the trusted image-building appliance only; job VM networking remains
explicit routed TAP/NAT.
[Backend probe](https://github.com/libguestfs/libguestfs/blob/master/lib/launch.c),
[SLIRP fallback](https://github.com/libguestfs/libguestfs/blob/master/lib/launch-direct.c).

The third build reached guest provisioning, confirming both appliance fixes.
It exposed that virt-customize's `--run` path invoked `/bin/sh`, ignoring our Bash
shebang and rejecting `pipefail`. Provisioning now uploads the script and explicitly
invokes Bash via `--run-command`, then removes the script from the image.

Provisioning then exposed the rootfs's unresolved systemd-resolved stub during
offline customization. The provisioning script now uses the appliance's SLIRP
DNS proxy before apt and restores production DNS configuration before finishing.
APT index updates fail on any error instead of silently accepting partial indexes.

The public appliance diagnostics also exposed a missing DHCP client in the Ubuntu
24.04 builder: libguestfs tried `dhcpcd` after failing to find `dhclient`, leaving
its interface down. The documented builder dependencies now explicitly include
`isc-dhcp-client`; provisioning uses the resolver supplied by libguestfs.
