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

`make vulncheck` builds the three binaries using the normal release build flags,
installs exactly `govulncheck v1.9.0` into a temporary directory, and scans both
`./...` source (with `CGO_ENABLED=0`, matching those binaries) and each actual
binary. To inspect already-built release artifacts without rebuilding them:

```sh
VULNCHECK_OUTPUT=build/release-scan ./scripts/vulncheck.sh /path/to/candidate/bin
```

The script never executes those binaries. It records compiler and scanner
versions, source commit, target platform, database URL/modification time,
binary SHA-256 checksums and embedded build metadata. Reports default to the
ignored `build/vulncheck/` directory. Use a fresh output directory per candidate
and retain reports with its exact artifacts. Build metadata in a binary, rather
than the scanner's compiler, determines its embedded standard-library version.
Source scans use the compiler selected for that invocation.

The hosted `Go vulnerability checks` workflow runs on pushes, PRs, manual
dispatch and weekly, preserving reports even on failure. No self-hosted runner
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
