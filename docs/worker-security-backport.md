# Legacy worker security backport

This maintenance line starts at unchanged source
`f59d919f5b4b07fd0ca6ce1040b6e83108662a46`. It is separate from current main and
is intended only for an existing installation whose worker can be reproduced
from that source. It retains the legacy worker API v1, configuration, identities,
certificates and journal/state contract. It does not add the later host-ownership
fence or adopt state initialized by ownership-enforcing releases.

The only runtime source change is the worker API cancellation fix also reviewed
in the current-main security PR: after reading a bounded response, preserve a
canceled context even if the peer returned a successful empty HTTP 200/EOF.
Previously this race returned a malformed-response error and lost
`errors.Is(err, context.Canceled)`. Cleanup closes the response and idle
connections; concurrent active requests are retained. A deterministic regression
covers the successful-EOF race and an ordinary malformed response. No credential
replay or state mutation is introduced.

## Provenance and build

On Linux amd64, an original-source build with Go1.26.3, CGO disabled and
`-buildvcs=false -mod=readonly -trimpath` produces worker SHA-256
`72f733fd816dfc33312d56ecc28425d037f979157657a964649067543a0cc7ef`.
Require byte equality with the original executable before using this compatibility
path; a runbook revision without a reproducible binary match is insufficient.
The original Git source archive SHA-256 is
`a3474a5efe8657a68fc607c7905e4847a5ef6c8cbfdeb30363e6e99a2adf987b`
(`git archive --format=tar f59d919f5b4b07fd0ca6ce1040b6e83108662a46`).

Build the reviewed maintenance commit, with Go's module checksum database enabled:

```sh
export GOTOOLCHAIN=go1.26.9
export GOMAXPROCS=2 GOFLAGS=-p=1
make build test
make vulncheck
```

The toolchain directive and hosted CI use Go1.26.9. Language minimum1.25.3 is
retained. The legacy worker links the standard library and this module; it has
no linked x/sys dependency. `make vulncheck` installs pinned govulncheck v1.9.0,
requires a clean committed checkout, builds/scans a temporary Git source
snapshot and all three exact build outputs, and records database timestamp,
compiler/scanner metadata and checksums under ignored `build/vulncheck`.
Tracked edits, untracked files and ignored generated Go/assembly/module inputs
are rejected before building/evidence. Ignored binaries/reports are allowed.
Workspace discovery and persisted Go settings are disabled (`GOWORK=off`,
`GOENV=off`); explicit external source/module overrides are rejected.
Provided binaries are explicitly marked as externally built and require their
own source/build provenance.

GitHub schedules run only on the default branch. The default-main vulnerability
workflow explicitly checks out `maintenance/worker-f59d919` for scheduled/manual
maintenance scans and retains separately named artifacts. This branch only
triggers push/PR/manual checks; a cron here would never execute. Merge this
maintenance implementation before enabling the default-main scheduled job.

The live vulnerability database changes; a retained snapshot is required for
identical historical findings (`GOVULNDB` selects it). Scans are reachability
evidence, not proof of exploitation or absence of unknown issues.

Only `bin/chickadee-worker` is the release artifact for this maintenance path.
Building the controller and guest is test coverage, not authorization to replace
them or an immutable image. Retain the reviewed source commit, pristine source
archive, worker hash, embedded `go version -m` output and test/scan evidence.
Use private scratch output (`BIN_DIR`, `TMPDIR`, `VULNCHECK_OUTPUT`) on shared hosts.

## Acceptance and rollback boundaries

Do not copy historical configuration, images, deployment units or credentials
from this branch. Preserve the existing worker identity, certificates, journal
and tombstones. Confirm no ownership claim exists; if state is already claimed,
this maintenance worker is incompatible. Never create/remove ownership markers
to force a rollout.

Drain one worker using its authenticated API; wait for jobs and actual execution
exit, then stop the service. Keep the prior compatible binary and configuration
privately, stage the verified worker atomically, and run offline preflight under
the existing unit hardening. Verify preflight creates no ownership claim. Start
the same service and verify the running executable's hash and Go version,
authenticated READY capacity, one real job, terminal cleanup and credential-free
replacement before advancing to another worker. Never duplicate GitHub listeners.

Rollback requires the same drain/exit proof and preserved legacy unbound state.
The original binary is compatible only while that state contract is unchanged;
it remains vulnerable and is a temporary recovery measure. Never run it or this
maintenance binary against newly claimed state. A future full ownership upgrade
requires its separately documented fresh-state/identity migration and coordinated
broker/certificate changes. This compatibility release does not close that work.

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
