# Reproducible guest performance benchmarks

Run from the Chickadee checkout, without root or additional packages:

```sh
python3 scripts/performance-benchmark.py --output benchmark.json --comparison baseline --sample 1
```

Use a new output filename for each invocation; existing output files are never
overwritten. `--output -` emits JSON only. Comparisons are named `baseline`,
`candidate` or `hosted`, keeping physical host identities out of public results.
An optional `--image-digest SHA256` records the operator-known immutable bundle
identity. The script uses validated `GITHUB_SHA`, or a bounded local Git lookup,
for source revision; it also hashes its own workload source.

Scratch defaults to a temporary `.chickadee-perf-*` directory in the current
checkout/workspace, not `/tmp`. Use `--scratch-dir LOCAL_DISK_DIRECTORY` only
when an operator has selected the appropriate filesystem. Schema version 2
records `storage.filesystem_type` and `storage.memory_backed` without recording
mountpoints, device names, directory paths or mount options. Mount selection
uses the longest component-matching mountpoint from a bounded mountinfo read,
including kernel path escaping and resolved symlinks.

Disk cases refuse `tmpfs` and `ramfs`. Layered filesystems such as overlay/aufs,
or missing/unverified mount information, also refuse disk cases because their
backing cannot be established from the mount line. The suite then reports
`status: incomplete`, exits unsuccessfully and preserves its six completed CPU
cases. An unverified storage result must not be treated as disk throughput.
Schema-version-1 storage results from default `/tmp` must be discarded unless
the actual filesystem was independently verified; their CPU cases remain useful
if CPU parameters, fixture checks and runtime versions are unchanged.

The original CPU-equivalent default suite completed in approximately **18.5
seconds** on the development machine. A schema-2 smoke with `--cpu-mib 32
--disk-mib 128` completed all nine cases in **5.56 seconds** on verified btrfs.
Storage timings can change after correcting a memory-backed scratch directory.
The suite has a 75-second work deadline and up to five seconds of subprocess
cleanup. A deadline/failure produces `status: incomplete`, retains completed
case measurements and exits unsuccessfully. Compare only completed cases with
matching workload parameters; rejected or missing disk cases are not measurements.
For slower machines use `--cpu-mib 64` or `32`, and optionally
`--disk-mib 128`. Keep these parameters identical across compared samples.
Temporary disk use is approximately 260 MiB; CPU fixtures are streamed/repeated
in one-MiB blocks rather than allocating the total work volume. The benchmark
does not install software, contact GitHub, change affinity, drop caches or alter
host/network settings.

## Fixed workloads and correctness

| Case | Default work per case | Interpretation |
| --- | --- | --- |
| Compression roundtrip, 1/2/4 processes | 512 MiB total, one-MiB blocks, zlib level 6 | Half seeded entropy and half repeated pattern; every decompression must equal its input |
| Cache-resident SHA256, 1/2/4 processes | 1024 MiB total, repeatedly hashing the same one-MiB fixture | Every digest must match; tests CPU/hash acceleration and process overhead, not a large-memory stream |
| Buffered sequential write + fdatasync | 256 MiB, repeated seeded entropy blocks | Checks byte count and size, hashes written content, and waits for fdatasync |
| Cached sequential read + SHA256 | The file immediately written above | Verifies complete content digest; includes hashing cost and expects warm guest page cache |
| Small-file metadata | 512 distinct four-KiB files | Create/write/stat/read/verify/unlink, with directory fsync after creation and deletion; file data is buffered, not individually fdatasynced |

The number of CPU processes changes parallelism, **not total work**. Compression
and hashing use separate processes, avoiding Python GIL assumptions. Do not
compare four-process work with four times the single-process bytes. The fixture
seed, block size, process counts, compression level and exact byte volumes are
included in the result. Small tests cover fixed-work partitioning, actual
compression correctness, corrupted disk-read rejection, scratch cleanup,
expired deadlines and privacy of proc/environment collection.

A buffered write plus guest fdatasync includes filesystem/virtual-disk flush
behavior; it is not a power-loss durability certificate. Cached reads are not
physical NVMe throughput. A fast hash result can reflect CPU instructions and
startup overhead; it does not predict compilation, networking or all workloads.
The development compression cases took approximately 7.36/3.79/2.07 seconds at
1/2/4 processes; the four-process hash case was shorter than one second, so
interpret its scaling cautiously.

## Versioned JSON and resource context

`schema_version: 2` contains `status`, `comparison`, `sample`, source revision,
script SHA256, optional image digest, scratch `storage`, workload `parameters`, whitelisted
`environment`, `results` and `total_wall_seconds`.

Each case records wall, user and system seconds, correctness checks and worker
metrics. Case wall time includes process launch, fixture generation and
validation. CPU user/system measurements include each fresh interpreter's
startup. `max_rss_kib` is the largest worker's lifetime peak, not simultaneous
process-tree memory; `peak_rss_sum_upper_bound_kib` sums individual peaks as an
upper bound. Kernel page cache is not counted in process RSS.

Context before/after each case records guest memory availability/cache/dirty
pages, CPU/memory/I/O pressure and aggregate CPU steal percentage. Steal uses
only the first eight `/proc/stat` counters, avoiding double-counting guest time.
Unavailable pressure/cgroup information is represented as null. These are guest
observations: zero steal does not prove an uncontended host or reveal its full
scheduler behavior. Version metadata includes Python, zlib, OpenSSL, OS/kernel,
CPU model, logical/available affinity count and root cgroup limits. No hostname,
username, runner name, addresses, complete environment or private cgroup paths
are collected.

## Host-affinity and hosted comparisons

For a before/after affinity experiment, keep the guest image digest, machine
type, vCPU/RAM/disk profile, Python/library versions and workload source fixed.
Use the same physical host for baseline and candidate. Keep the scratch
filesystem type and equivalent storage location identical, and verify both
are backed by the intended virtual/local disk; do not compare tmpfs with ext4
or host RAM with NVMe. Paths and host placement belong in private operator
notes, not public JSON. If comparing two hosts,
measure each separately and treat the hardware difference as another variable.
The operator must privately verify actual broker placement: `runs-on: chickadee`
alone does not bind a VM to a physical host. Restricting eligible workers is an
operator action, not an option in this benchmark script.

Collect at least three independent ephemeral-job samples per condition, preferably
interleaved baseline/candidate to reduce time-of-day and background-load bias.
Report medians and the range of individual results, not just the fastest sample.
Compare single-process time, fixed-work multi-process throughput, user/system CPU
time and pressure/steal context together. Preserve each JSON artifact.

A GitHub-hosted runner can run the same script, with `--comparison hosted`, but
hardware, vCPU/memory allocation, kernel and bundled Python may differ. Record
those differences and compare equal resource shapes where possible; this is not
an identical-host affinity experiment.

Warm-pool READY latency, demand acquisition, JIT generation, GitHub connection,
job dispatch and runner replacement are separate lifecycle measurements. Do not
fold them into CPU or disk case timings. The existing
[`scripts/benchmark.py`](../scripts/benchmark.py) measures module download and
cold/warm Go build/test with isolated caches; use it for real repository build
behavior, alongside this suite, with the same pinned checkout and Go version.
