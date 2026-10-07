# Benchmark-driven performance tuning — 2026-10-06

The useful change was worker selection, not CPU pinning. One pilot consistently
finished CPU work faster. Roost now supports operator-owned placement priorities:
prefer the faster eligible READY worker, but always take an available compatible
READY guest before any cold capacity. Resource budgets and shared warm targets
are unchanged. Actual deployment inventory remains private.

## CPU affinity experiment

Three independent ephemeral medium jobs per condition on pilot B used the same
Ubuntu 26.04 image, 4 vCPU / 8 GiB, fixed workloads and runtime versions. Default
scheduling was compared with disjoint whole-VM sets of P-core logical CPUs, with
one thread per physical core. Every actual QEMU thread mask was inspected. This
was not exclusive host isolation or individual vCPU pinning. Conditions were
sequential, not interleaved; only three samples do not establish significance.

| Fixed work | Default median | VM CPU-set median | Observed change |
| --- | ---: | ---: | ---: |
| Compression, 1 process(es) | 5.111 s | 5.221 s | +2.2% time |
| Compression, 2 process(es) | 2.611 s | 2.754 s | +5.5% time |
| Compression, 4 process(es) | 1.441 s | 1.450 s | +0.6% time |

No reliable CPU benefit appeared, so VM affinity remains disabled. A fixed-source
Go cross-check took 40.507 seconds for cold build + cold tests by default, versus
39.983 seconds with affinity. That is a single sample per condition and a roughly
1.3% difference, insufficient to override the CPU results. Warm tests reused Go
test cache; their timings are not fresh test execution.

The original microbenchmarks used /tmp, which is tmpfs in this image. Their disk
cases are excluded from accepted results. CPU functions and work parameters are
unchanged in schema 2 (AST equality checked), so the CPU cases remain usable.
Worker binaries differed by optional-affinity/prelaunch-cleanup implementation;
successful guest CPU workloads and image contents stayed fixed.

## Faster READY worker preference

The matched guest image and CPU workloads showed a substantial hardware gap.
CPU medians below use three independent jobs per pilot. Both were medium jobs.

| Workload | Pilot A | Pilot B | Sample count |
| --- | ---: | ---: | ---: |
| Compression, 1 process | 7.322 s | 5.111 s | 3 each |
| Compression, 4 processes | 2.176 s | 1.441 s | 3 each |
| Cache-resident SHA256, 1 process | 2.159 s | 0.543 s | 3 each |
| Fixed Go cold build + cold tests | 50.966 s | 40.507 s | 1 each |

Pilot B used about 30% less time for single-process compression and about 20%
less for this one build/test cross-check. Hash results also reflect hardware
instruction support, not merely GHz. These results support preferring B when
both are READY, not promising all customer workloads the same speedup. Provider
controls have different hardware/Python/RAM and are not identical-host tests.

The Go source is pinned to a83e952aecd4a7df1983e321974ad48e94b0a2da, Go 1.26.3,
with isolated empty caches; module download is timed separately. Go caches use
/tmp on the matched guest image. The cold race-test workload includes lifecycle
timeouts, so it is not a pure compiler benchmark.

## Storage correction and launch acceptance

Schema 2 allocates scratch in the checkout and verifies filesystem type. tmpfs,
ramfs or unverified backing cannot produce accepted disk measurements. The
corrected samples used ext4. Buffered writes include hashing and fdatasync;
cached reads are not physical NVMe throughput.

The corrected [TRIM smoke](https://github.com/plover-digital/chickadee/actions/runs/37560038685)
wrote and synced 64 MiB of random data on verified root-backed ext4, deleted it
and trimmed root. Its writable overlay then occupied **37.441 MiB**
of allocated host blocks versus **98.438 MiB** apparent length. The job
succeeded; this is physical reclamation evidence, not the earlier tmpfs probe.

Both post-policy startup probes were verified on the preferred worker:

- [small: 6.405 s creation to first command](https://github.com/plover-digital/chickadee/actions/runs/37559895591)
- [medium: 7.102 s creation to first command](https://github.com/plover-digital/chickadee/actions/runs/37559898118)

These are one sample each, with whole-second workflow creation timestamps and no
independent clock-offset calibration. They demonstrate launch remained low;
normal warm boot occurs before assignment. Each host targets one small and one
medium credential-free guest, subject to running-job budgets.

[Raw accepted results](2026-10-06-performance-tuning.json) retain public run IDs,
versions, workload parameters and pressure/context without private host inventory.
See [methodology](../performance-benchmarks.md), [CPU affinity](../cpu-affinity.md)
and [warm capacity](../warm-pool.md). Real network partitions, hostile-tenant
resource isolation and broader reliability acceptance remain separate work.
