# Selectable runner profiles: proposed design

Status: design draft, informed by [runner-platform research](runner-platform-research.md).
The current controller has not yet implemented this schema or multiple profiles.
This extends the originally single-profile prototype at the user's request;
it does not introduce Kubernetes, databases, snapshots or multiple hosts.

## Four separate objects

**Image bundle:** immutable root disk plus matching versioned kernel/initrd and
manifest/checksums. Describes OS/version, architecture, runner/bootstrap versions
and tested capabilities. The controller is independent of the workload's language.
The initial reference builder remains Ubuntu; other Linux bundles must implement
the same bounded serial/bootstrap and networking contract.

**Resource class:** CPU and RAM allocation, such as 2 vCPU / 4 GiB,
4 vCPU / 8 GiB or 8 vCPU / 16 GiB. These are vCPUs, not a promise of dedicated
physical cores. Disk selection requires compatible image/filesystem sizing;
merely enlarging a qcow2 virtual disk does not enlarge the guest filesystem.
Retain manifest-controlled disk size initially rather than imply otherwise.

**Profile/queue:** a named image + resource class + pool policy. A profile has
its own scale-set/workflow label, warm target, maximum VMs and lifecycle timeouts.
It is an approved catalog entry, not an arbitrary resource request from a job.

**Host limits:** aggregate VM, vCPU and memory budgets, including emulator and
controller allowance. Count booting, warm, credentialed and retiring VMs until
process exit/cleanup confirms release. A per-profile maximum does not guarantee
that all profiles can run at their maxima simultaneously.

Caches and network policy can become separate approved policies later. Neither
is required for the first selectable-profile implementation.

## Example proposed configuration

This is an **illustrative fragment**, not a config accepted by the current binary.
Shared GitHub App, runner-group, state-directory and install settings are omitted.
All image paths below are examples, not artifacts shipped in this repository.

```json
{
  "images": {
    "ubuntu-2404": {"path": "/var/lib/chickadee-images/ubuntu-2404/20261006"},
    "debian": {"path": "/var/lib/chickadee-images/debian/validated-version"}
  },
  "resource_classes": {
    "small": {"cpus": 2, "memory_mib": 4096},
    "medium": {"cpus": 4, "memory_mib": 8192},
    "large": {"cpus": 8, "memory_mib": 16384}
  },
  "profiles": {
    "chickadee-ubuntu-2404-small": {
      "image": "ubuntu-2404", "resources": "small", "warm_pool": 1, "max_vms": 2
    },
    "chickadee-debian-medium": {
      "image": "debian", "resources": "medium", "warm_pool": 0, "max_vms": 2
    },
    "chickadee-ubuntu-2404-large": {
      "image": "ubuntu-2404", "resources": "large", "warm_pool": 0, "max_vms": 1
    }
  },
  "limits": {"max_vms": 4, "max_vcpus": 16, "max_memory_mib": 32768}
}
```

Workflow selection remains one line:

```yaml
runs-on: chickadee-debian-medium
```

A job targeting that label may run on any matching ephemeral runner in its scale
set. It cannot select an arbitrary image path or override host limits. The host
needs enough physical capacity for its declared budgets; this example is not a
recommendation to deploy 32 GiB of guests on a 16 GiB machine.

## Controller behavior

- Hold one host/state ownership lock before any cleanup or GitHub sessions.
- Maintain one official scale-set client/session per profile; advertise bounded
  capacity and consume assigned-demand statistics for each.
- Keep all VM state, TAP slots and aggregate resource reservations under one
  owner. A guest is created with a fixed profile/image identity.
- Reserve a READY guest only for its own profile. Generate fresh JIT using that
  profile's scale set, persist intent before delivery and never reuse it afterward.
- Demand has priority over optional idle warm capacity. Retire excess warm guests
  to free resources for another profile, but do not kill credentialed job VMs just
  to make room. Release budgets only after confirmed exit.
- Wait within the relevant queue when resources are unavailable; do not silently
  substitute a smaller resource class or incompatible image. Define a fair policy
  so continuously busy small queues cannot starve a large queued request.
- Preserve profile/scale-set identity in durable records. Restart must reconcile
  every owned registration, including profiles removed or renamed in a config
  update. If ownership cannot be proven, fail closed and explain recovery.

Capacity reporting/backpressure needs integration tests: several independently
polled scale sets must not imply that one physical host has unlimited capacity.
The scheduler must handle cancellation and revised assigned statistics without
provisioning from raw message counts or binding a VM to a specific event.

## Image admission and compatibility

Only approved local, read-only image artifacts are selectable in the first
version. Verify checksums, virtual/filesystem size, architecture and bootstrap
protocol compatibility. Keep kernel/initrd/rootfs versions together. Reject
unsafe paths, symlinks where they could redirect ownership, unknown references,
unsupported capabilities and profiles that can never fit the host budget.

Changing a profile's image must not reinterpret an existing VM/overlay using a
new base image. Track immutable artifact identity for its full lifecycle.
Configuration reload while jobs run is not a requirement for the first version;
an explicit drain/restart procedure is easier to make safe and reviewable.

Do not claim native ARM, Windows or macOS support by adding names to the catalog.
The current QEMU/guest implementation is Linux amd64. Other architecture/OS
backends require their own hardware, boot mechanism, agent and lifecycle tests.

Custom images in a managed service require a separate isolated builder and trust
model; user-submitted build instructions must not run as root on the controller
host or gain its GitHub App credentials. Operator-authored local images are the
initial supported trust boundary.

## Required validation before calling it implemented

- Legacy one-profile configuration still works with its existing workflow label.
- Two profiles boot with different resources and receive JIT from the correct
  scale set; a guest cannot cross queues or receive credentials twice.
- Real GitHub jobs target two different labels and observe the expected resources.
- Shared VM/vCPU/memory limits survive mixed-profile bursts, boot failures,
  retiring guests and canceled demand; TAP slots are never concurrently reused.
- Large requests can make progress after other jobs finish; warm targets do not
  cause continuous churn or prevent demand from another profile.
- A removed/renamed profile and ambiguous JIT failure retain enough ownership
  information to recover disks/processes/registrations after restart.
- Each image is independently booted to READY and runs one actual job; guest
  diagnostics survive cleanup and every writable disk is destroyed afterward.
- Installer/preflight/systemd resource budgets cover all admitted shapes/images
  and cannot oversubscribe physical memory silently.

No runtime/profile/cache feature is enabled merely by this draft. Existing
single-host lifecycle, networking restrictions and trust limitations remain.
