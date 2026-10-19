# Go build and vulnerability baseline

Host builds and CI use Go **1.26.9**, the patched supported 1.26 release verified
on 2026-10-19 against the [official release history](https://go.dev/doc/devel/release).
The module retains its Go 1.25.3 language minimum for downstream worker API
consumers, with `toolchain go1.26.9` selecting the build baseline. The language
minimum is not a security recommendation. Use `GOTOOLCHAIN=go1.26.9` for an exact
baseline build; Go's default automatic selection can use a newer installed Go.
Keep the public module checksum database enabled.

`golang.org/x/sys` is updated from v0.42.0 to v0.44.0, the first version fixing
[GO-2026-5024](https://pkg.go.dev/vuln/GO-2026-5024). Its affected API is Windows-only;
it was a module-only finding on Linux. This targeted update keeps its Go 1.25.0
requirement compatible with our existing language minimum. Linux tests and macOS
cross-compilation cover the platforms used by the host module; Windows is not a
supported controller platform.

## Reproduce the checks

On Linux amd64, with Go, Python 3, Make, Bash and `sha256sum` available:

```sh
export GOTOOLCHAIN=go1.26.9
make build test
make vulncheck
```

`make vulncheck` requires a clean committed checkout, then builds the three
binaries from a temporary `git archive` snapshot using the normal release flags,
installs exactly `govulncheck v1.9.0` into a temporary directory, and scans both
`./...` source from that same snapshot (with `CGO_ENABLED=0`) and each actual
binary. To inspect already-built release artifacts without rebuilding them:

```sh
VULNCHECK_OUTPUT=build/release-scan ./scripts/vulncheck.sh /path/to/candidate/bin
```

The script never executes those binaries. Tracked edits, untracked files and
ignored generated Go/assembly/module inputs are rejected before building or
recording evidence; ordinary ignored binaries/reports are allowed. Workspace
discovery and persisted Go environment settings are disabled (`GOWORK=off`,
`GOENV=off`); explicit external source/module overrides are rejected.
The immutable snapshot prevents concurrent checkout edits from changing the
scanned source. Evidence explicitly states whether binaries were built from that
snapshot; provided release binaries require their own source/build provenance. It records compiler and scanner
versions, source commit, target platform, database URL/modification time,
binary SHA-256 checksums and embedded build metadata. Reports default to the
ignored `build/vulncheck/` directory. Use a fresh output directory per candidate
and retain reports with its exact artifacts. Build metadata in a binary, rather
than the scanner's compiler, determines its embedded standard-library version.
Source scans use the compiler selected for that invocation.

The hosted `Go vulnerability checks` workflow runs on pushes, PRs, manual
dispatch and weekly, preserving reports even on failure. Because GitHub only
runs schedules from the default branch, its additional maintenance job explicitly
checks out `maintenance/worker-f59d919` on scheduled/manual runs and keeps its
reports in a separately named artifact. The maintenance workflow itself has no
cron trigger. Merge the maintenance scanning implementation before relying on
this default-branch dispatch. No self-hosted runner
or deployment credentials are needed. Reachable source symbols and symbols
present in binaries fail the check; verbose reports separately list affected
packages/modules without reachable symbols. Binary scanning has less precise
reachability than source scanning and may report additional findings. Neither
mode proves exploitation or absence of unknown vulnerabilities. These checks
cover Go dependencies, not the host kernel, QEMU or guest OS packages.

The public vulnerability database changes over time: pinned compiler/scanner
versions make the procedure reproducible, not future results identical. Reports
record the database modification timestamp used by each scan. For a historical
replay, retain an operator-controlled Go vulnerability database snapshot and set
`GOVULNDB` to its URL (including a supported `file://` snapshot). A timestamp
alone cannot reconstruct a historical database. Review new findings rather than
adding blanket allowlists. See the official
[govulncheck documentation](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck).

On a shared host, assess capacity first and use private scratch directories for
`TMPDIR`, `BIN_DIR` and `VULNCHECK_OUTPUT`; `GOMAXPROCS=2 GOFLAGS=-p=1` bounds Go
parallelism. Do not point tests at service state or image directories. Full
image builds and privileged live-host acceptance are separate operator work.

## Upgrade and rollback

Go standard-library fixes are embedded in executables. Updating a host compiler
alone does **not** fix an existing controller, worker or guest bootstrap binary.
Build, scan and retain the exact candidate hashes before rollout; guest image
builders and installed guest toolchains have their own pins and acceptance.

There is no worker API, serial protocol, config or state-schema change in this
security update. A consumer importing `workerapi` still chooses its own build
toolchain and must rebuild its own executable. No Roost account or service is
required for standalone operation, and no synchronized protocol upgrade is
required.

Drain jobs before replacing a controller/worker binary. Preserve registration
intents, ownership claims and consumed-assignment tombstones. When upgrading
from a version predating host ownership enforcement, complete the separate
[state ownership migration](state-ownership.md); this toolchain change does not
bypass it. Never start a second GitHub listener for the same queue.

Retain the prior compatible binary/configuration and candidate hashes privately.
Follow the [isolation preflight and acceptance](isolation.md#validation-and-rollout),
then verify the running executable's hash, authenticated inventory/READY capacity,
one real job and credential-free replacement. Roll back only after draining,
using a binary compatible with the existing ownership/state format; an old
vulnerable binary is a temporary recovery measure, not completed remediation.
Source/CI success is not deployment acceptance.

Each scan attempt writes a fresh `run-*` directory under `VULNCHECK_OUTPUT`
(default `build/vulncheck`). The `current` file names the latest attempt;
its `status.txt` is `running`, `complete`, or `failed exit=N`. Only `complete`
means the entire attempt succeeded. Earlier attempts remain identified in their
own directories, including partial failure evidence; caller files are preserved.
Do not treat older root-level reports from previous script versions as current.

Each attempt uses a fresh private `GOMODCACHE` for both application dependencies
and pinned scanner installation. Previously extracted modules in the caller's
cache are never used or modified. Evidence records the effective cache path and
`module_cache_policy=fresh-per-attempt-cleaned-on-exit`. Cleanup makes only the
private scratch tree writable before removing downloaded read-only modules.
This requires fresh downloads per attempt; the ordinary Go build cache remains
available. Keep Go's checksum verification enabled for release scans.

### Public module authentication policy

Before any build/install, scans enforce `GO111MODULE=on`,
`GOSUMDB=sum.golang.org`, `GONOSUMDB=none`, and `GOINSECURE=none`.
The standard Go toolchain's built-in checksum-database key authenticates public
module downloads, including the scanner's transitive graph and downloaded Go
toolchains. Committed application `go.sum` entries remain trusted source inputs.
Private or unauthenticated dependency graphs are not supported for release
attestation. This overrides disabled/custom checksum databases and exclusion
patterns, including `GOPRIVATE`'s implicit `GONOSUMDB` default.

`GOPROXY`, `GONOPROXY`, and `GOPRIVATE` may still select proxy versus direct
transport; neither transport bypasses the enforced checksum policy. Proxy URLs
and private patterns are not written into environment metadata. A proxy may
mirror checksum records, but cannot substitute the database signing key.
The policy follows the [Go module authentication reference](https://go.dev/ref/mod#authenticating).

The environment boundary also disables persisted Go settings and workspace
selection, rejects external source/module overlays, and uses a fresh module
cache. It assumes a trusted installed Go executable/toolchain and ordinary local
build cache; it does not attest a compromised compiler or hostile local operator.
The vulnerability database remains a separately selected scan input, recorded
with the findings. No repository or service-wide Go settings are changed.

### External package drivers

The scanner exports `GOPACKAGESDRIVER=off` before installing or invoking
`govulncheck`, and records that effective value in `environment.txt`. This forces
its `go/packages` loader to use the Go command against the archived source.
Merely unsetting the variable is insufficient: the loader can otherwise discover
`gopackagesdriver` on `PATH`. An explicit driver or an automatically discovered
driver could substitute package metadata and files outside the recorded commit.
See the upstream [package driver protocol](https://pkg.go.dev/golang.org/x/tools/go/packages#hdr-The_driver_protocol).

The offline scanner-harness regression exercises both selection paths with a
sentinel executable, verifies neither runs under the policy, and removes the
export in a negative control to demonstrate false-success substituted-graph
evidence. It tests script environment propagation without downloading modules or
running a full vulnerability scan; actual source/binary scans remain release
checks. Prior evidence lacking this policy must be reassessed or rerun; this
change does not alter runtime binaries, protocols, configuration, or state.
