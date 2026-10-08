# Runner resource measurements

When per-VM cgroups are enabled, the host samples each credentialed runner VM
at two-second intervals, starting immediately before CONFIG delivery. Boot and
credential-free warm CPU/I/O are excluded. Collection is read-only and bounded:
one in-memory accumulator per VM, no unbounded point history, and one summary
saved after proven QEMU exit, cgroup removal and disk cleanup. Missing counters
are unavailable; read failures retain successful observations with `partial=true`.
A worker crash can lose the accumulator; recovered assignments have no summary.

CPU `usage_usec` is summed over the measured interval. Average and sampled peak
percentages divide by allocated vCPUs (100% uses all of that CPU allocation).
`throttled_usec` is the kernel's cumulative CPU throttling measure, not a wall-time
percentage or evidence of a guest process bottleneck. CPU includes QEMU overhead.
Memory is the sampled maximum of `memory.current`, including QEMU and charged
cache. This is not guest application RSS or the kernel's lifetime `memory.peak`;
short spikes between samples can be missed. `memory.max` includes host overhead;
Host `memory.events` OOM kills (not guest-kernel OOM events) are reported as a delta from credential delivery.

I/O reads `io.stat` for exactly one device when `cgroup.io_max` selects one device.
It does not sum physical and device-mapper layers, expose device IDs to tenants,
or count cache hits. With zero/multiple selected devices I/O is unavailable;
CPU/memory collection still works. Byte totals and peak interval rates do not
measure guest disk space, latency, queue depth, or network throughput.

The semantics follow the official [Linux cgroup v2 documentation](https://docs.kernel.org/admin-guide/cgroup-v2.html).
No host or guest credentials, paths, command lines, process lists or job content
are collected. Standalone operation saves optional summaries in its private
`usage.json`; hosted workers save them in durable terminal reservation records.
Host isolation, resource limits and one-job destruction remain unchanged.

## Compatibility and deployment

Worker API remains `/v1`, with optional `resources` on terminal records and
resource schema `version: 1`. The bounded response limit grows to 2 MiB to cover
1024 terminal records with summaries. Legacy records with no summary remain valid.
Strict older clients reject the new field: upgrade the Roost broker/worker API
client before starting upgraded workers. Upgrade the website before a collector
sends resource summaries. Then drain/restart one worker at a time, retaining the
other host's service and waiting for actual READY capacity before proceeding.
The worker journal remains version 1 with an optional terminal-record field.
Old strict journal readers cannot read populated summaries; rollback requires a
metrics-aware binary. Never remove lifecycle tombstones merely to downgrade.
