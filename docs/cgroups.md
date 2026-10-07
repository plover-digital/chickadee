# Optional per-VM resource cgroups

Standalone and keyless workers support optional cgroup v2 limits. Defaults remain
unchanged until `cgroup` is present in the trusted local configuration:

```json
"cgroup": {
  "memory_overhead_mib": 512,
  "pids_max": 128,
  "io_weight": 100
}
```

Linux must support `CLONE_INTO_CGROUP` (kernel 5.7 or newer), unified cgroup v2 and
CPU, memory, pids and I/O controllers. The supplied opt-in systemd drop-ins require
systemd 254 or newer for `DelegateSubgroup=controller`. Do not enable the JSON
option before preparing delegation. Missing delegation/controllers, read-only
controls or unsupported pre-exec placement fail closed; QEMU never falls back to
unconfined execution.

The worker/controller runs in its service's `controller` leaf. Every VM receives
a fresh sibling `chickadee-vm-<opaque ID>` cgroup. `UseCgroupFD` places the initial
launcher in the VM cgroup before execution; the sandbox and QEMU inherit it.
Guests have no cgroup filesystem mount or management access. The optional `root`
field identifies this process's delegated service hierarchy only, never the
host-wide root or another service. Omitting it derives the service hierarchy.

Each VM has `cpu.max = vCPUs * 100000 / 100000`, `memory.max` equal to configured
guest RAM plus explicit QEMU/headroom allowance, `memory.swap.max=0`,
`memory.oom.group=1`, `pids.max`, and `io.weight`. CPU quota bounds average usage;
it does not reserve physical CPUs or pin individual vCPU threads. Affinity is a
separate option. Host pids count launcher/QEMU tasks, not processes inside a guest.
An OOM in one VM group kills that group; `OOMPolicy=continue` keeps unrelated
worker jobs running. Parent service limits still apply across all groups.

512 MiB is an experimental default, not measured proof of sufficient overhead.
QEMU mappings, threads and charged file cache can exceed it. Measure supported
profiles under representative memory and I/O workloads before stronger claims.
Startup requires a finite parent `memory.max` to cover the guest memory budget,
`max_vms * memory_overhead_mib`, and at least 256 MiB for the manager. For a
12,288 MiB guest budget and three VMs with 512 MiB overhead, that minimum is
14,080 MiB. Review actual host headroom before adjusting parent `MemoryMax` or
reduce advertised capacity. No running parent limit is changed automatically.

## Delegation setup

For a keyless worker, review and install `deploy/chickadee-worker-cgroup.conf` as
`/etc/systemd/system/chickadee-worker.service.d/cgroup.conf`. The standalone
installer installs `deploy/chickadee-cgroup.conf` when the input config explicitly
opts in. It generates aggregate CPU, task and memory limits including configured
headroom. Installation leaves the service stopped.

The drop-ins make cgroupfs read-only except the exact delegated service hierarchy.
A custom unit or systemd slice needs its own reviewed `ReadWritePaths` path.
Keep the guest sandbox policy unchanged. Preflight runs inside the delegated unit;
a shell outside it cannot validate service ownership. Drain existing jobs before
changing delegation, limits or binaries, retain prior configuration for rollback,
and run fresh READY/job/cleanup acceptance before enabling a wider deployment.

## Optional hard I/O limits

`io.weight` is relative sharing and depends on the backing device/scheduler. It
provides no bandwidth guarantee. Operators can configure an explicit device:

```json
"io_max": [{"device": "259:0", "read_bps": 104857600, "write_bps": 52428800}]
```

This illustrative device is not discovered or assumed by Chickadee. Identify the
actual block device(s) charged in `io.stat`, including storage-stack effects, and
verify throttling on the intended runtime filesystem. `read_iops`/`write_iops`
are also supported. Omitted dimensions remain unlimited. Device IDs must exist;
invalid limits/controller writes prevent VM launch. These are block-I/O limits,
not network shaping or a hard storage-capacity quota.

Cleanup confirms the actual QEMU/sandbox-child exit, then requires cgroup
`populated=0` and removes the empty VM group before deleting its disk. Failure
retains disk/capacity. Restart reaps owned processes first, cleans only empty
owned VM groups, and then removes overlays; populated groups block admission.
No unrelated cgroup is adopted, signalled or recursively deleted.

Unit tests cover exact limits, failed writes, invalid pre-exec descriptors and
populated-group disk retention. An explicitly enabled transient delegated Linux
smoke test verifies real pre-exec membership, descendant inheritance and limits;
it does not boot QEMU or execute a GitHub job. Actual VM/OOM/I/O acceptance remains
necessary before claiming production tenant availability or completed isolation.

References: [kernel cgroup v2 documentation](https://www.kernel.org/doc/html/latest/admin-guide/cgroup-v2.html)
and [systemd delegation](https://github.com/systemd/systemd/blob/main/docs/CGROUP_DELEGATION.md).
