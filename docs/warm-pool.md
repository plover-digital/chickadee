# Warm capacity and storage

A worker can keep one small (2 vCPU / 4 GiB) and one medium (4 vCPU / 8 GiB)
Ubuntu 26.04 guest READY using the shared image in `examples/worker.json`.
This requires a 6-vCPU allocation budget and 12 GiB guest-memory budget,
plus host/QEMU headroom. These are physical shared pools, not per-customer pools.
Warm targets are best-effort: running, booting, retiring and uncertain VMs count
against the same budgets. Jobs take priority, so both targets may not be met
under load. Other image profiles may reclaim incompatible credential-free warm
capacity. They must never reclaim a VM after credential intent.

Guests boot independently and await serial configuration; no snapshots or
suspension are used. Small and medium reference the same immutable image and
have separate thin qcow2 overlays. Actual idle resident memory is lower than the
configured guest ceiling; keep admission based on the full ceiling because jobs
can use all of it. Do not substitute idle RSS or compression for a capacity budget.

QEMU passes guest TRIM requests through with `discard=unmap` and converts zero
writes with `detect-zeroes=unmap`. Guests can run `sudo fstrim /` to reclaim free
overlay blocks during a job. The backing bundle remains read-only. Do not trim,
compact or modify an active immutable base. Image builders already sparsify new
bundles before checksumming them. Keep referenced bundles and a deliberate
rollback version when retiring old images.

The worker verifies checksums once during engine startup before opening its API.
The systemd unit avoids repeating that full hash in ExecStartPre; `-check` remains
available for explicit installation validation. Roll one host at a time and gate
the next stop on authenticated inventory showing READY guests, not systemd active.
Logs expose numeric `boot_ms` for READY guests without credentials. The manual
startup-probe workflow accepts small/medium labels and records its first command
in UTC; compare complete request-to-command time, not just VM boot time.

Sources: [QEMU discard and zero-write options](https://www.qemu.org/docs/master/system/qemu-manpage.html),
[virt-sparsify](https://libguestfs.org/virt-sparsify.1.html).
