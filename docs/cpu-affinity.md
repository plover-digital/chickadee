# Optional VM CPU affinity

Workers may set `cpu_ids` to an ordered list of host logical CPU IDs. The pool
must contain at least `budget.max_cpus` unique IDs, all online and available in
the worker's process/cgroup affinity. Empty or omitted preserves default Linux
scheduling. Configuration version and worker API remain version 1; deploy a
worker supporting this optional field before adding it to local configuration.

For example, a host with sufficient physical cores might use:

```json
"cpu_ids": [0, 2, 4, 6, 8, 10]
```

These numbers are illustrative; verify your topology with `lscpu -e` and
`/sys/devices/system/cpu/cpu*/topology/thread_siblings_list`. On hybrid processors,
select the intended core type; do not assume a CPU number implies a performance
core. Prefer one logical thread from each physical core for this experiment.
Leave capacity for the host and evaluate competing workloads. A host with fewer
physical cores than its vCPU budget cannot assign disjoint physical cores to all
VMs without reducing capacity; default scheduling can remain the better choice.

The worker assigns each VM as many IDs as its vCPU count. Booting, reserved,
running, retiring and uncertain VMs keep their assignments until confirmed QEMU
exit and disk cleanup. Existing warm/resource rules remain authoritative. After
a worker restart, existing owned VMs are reaped before new CPU sets are assigned.

Host `taskset` from util-linux wraps the existing prlimit/bubblewrap launcher
before execution. All QEMU threads inherit the VM set; no QMP lookup or affinity
change is on the READY-to-JIT path. Descriptor passing and pidfd exit monitoring
are preserved. Missing taskset or invalid/restricted CPU IDs fail startup.

This confines each VM to a disjoint set, not one specific host CPU per vCPU.
QEMU emulation/I/O threads share the same set. It does not reserve those CPUs
against other host processes or replace cgroup CPU limits. There are no hostwide
isolcpus, interrupt-affinity, power-governor or real-time scheduling changes.
Drain before changing the pool, CPU topology or cgroup affinity.

Measure same-host baseline/candidate workloads and record the actual thread
masks under `/proc/PID/task/TID/status`, plus host load/throttling context. Keep
startup latency separate from throughput; pinning is not assumed to improve
either. See [benchmark methodology](performance-benchmarks.md).

Sources: [Linux affinity semantics](https://man7.org/linux/man-pages/man2/sched_setaffinity.2.html),
[Linux CPU topology](https://docs.kernel.org/arch/x86/topology.html),
[systemd CPUAffinity](https://github.com/systemd/systemd/blob/main/man/systemd.exec.xml).
