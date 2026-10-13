# Optional Landlock process runners

Chickadee can run a fresh GitHub runner process in a Linux sandbox instead of
booting a VM. Select `machine: landlock` in a **dedicated worker** configuration.
The VM controller and existing VM profiles remain unchanged. Initial support is
Linux amd64. This mode needs no KVM, Roost account, external database or container
engine. It shares the host kernel and read-only host userland: use it for trusted
workflows until your deployment's isolation and workload compatibility have been
reviewed. It is not a distro-specific immutable guest OS image.

## Availability and service boundary

This backend is available only to operators deploying Chickadee themselves.
**chickadee.run provides disposable VM runners only.** Landlock profiles are not
advertised, admitted or routed by the hosted service; Roost has no Landlock queue
option. A self-hosted integration must explicitly select this different trust
boundary and must never substitute it for an existing VM queue.

## Isolation and limits

Each execution gets:

- Mandatory private user, mount, PID, network, IPC, UTS and cgroup namespaces,
  no capabilities, no-new-privileges and disabled further user namespaces.
- Strict Landlock ABI6 filesystem and IPC restrictions, using pinned
  `github.com/landlock-lsm/go-landlock v0.10.1`. Unsupported kernels fail closed;
  `BestEffort` and unsandboxed fallback are never used.
- Directory listing in the private mount tree permits runner ancestor-path
  validation; it grants no broader file read/write access.
- Read-only `/usr`, runtime libraries, public OS/CA metadata, the verified runner
  archive and trusted worker executable. Runtime alternatives are normalized only
  when their symlinks resolve into these already-exposed read-only trees.
  Host home, management state, keys,
  sockets, `/sys`, KVM and Docker devices are absent. Keep exposed runtime trees
  free of secrets. Runtime/tool versions come from the host, not the bundle digest.
- A fresh size-limited tmpfs for the runner, home, workspace, tools and temporary
  files. All writable data is RAM-backed and charged to the execution cgroup.
  The configured disk size is an upper tmpfs bound; available RAM can exhaust
  first. This mode does not offer a disk-backed workspace or shared caches.
- Mandatory delegated cgroup-v2 CPU quota, memory/swap and process-count limits,
  applied before the sandbox starts with `CLONE_INTO_CGROUP`. Existing cgroup
  defaults include 512MiB runtime overhead beyond configured runner memory;
  account for it in the parent service budget. The parent unit also limits the
  controller/relay. Optional existing I/O limits constrain host runtime reads.

Local test servers can use private loopback ports. Direct external networking
has no route/interface. Outbound HTTPS uses a local CONNECT proxy linked to a
host-side relay through an inherited Unix socket. The relay resolves once,
rejects private/loopback/link-local/metadata/reserved and host addresses, and dials
only validated public IPv4 on 443. It never exposes a host listener or mounted
host socket. Set `landlock_deny_prefixes` to every host **public NAT/egress** address
as well as any additional restricted networks; local interface addresses are
also refreshed and blocked for every connection, including after a sandbox has
become warm. Interface inventory errors fail closed. Keep configured public NAT
addresses current. IPv6, direct DNS,
SSH, arbitrary outbound ports and ordinary outbound HTTP are unsupported.
Proxy-aware HTTPS clients work; clients ignoring proxy variables have no direct
network route. Landlock port rules do not enforce IP destination policy, so this
mode relies on network namespaces and the relay for networking restrictions.

There is no sudo/package installation, host Docker socket, Docker container
Actions, or service-container support. Add required tools to the trusted host
before deployment, or use VM profiles for those workflows. Host administration,
kernel vulnerabilities, side channels and denial-of-service remain part of the
shared-kernel trust model. This prototype is not a completed hostile multi-tenant
security review.

## One-job lifecycle

The existing worker scheduler maintains configurable shared warm sandboxes.
A warm sandbox extracts a fresh runner into its private tmpfs, enforces Landlock,
reports READY over bounded pipes and waits without GitHub credentials. The
existing journal records reservation, seal and credential intent before sending
one fresh JIT configuration. GitHub matches a job; creation is not a binding to a
specific workflow job. A sandbox is never reused after credential intent.

The pinned runner consumes JIT using `ACTIONS_RUNNER_INPUT_JITCONFIG`, removes
that input from its environment, and runs at most one job. JIT is absent from
process arguments and controller logs. The runner receives no App/OAuth keys.
Job output remains in GitHub; bounded opaque diagnostic chunks are retained
privately as 0600 files and must be treated as sensitive, untrusted data.
Collection inspects at most 64 entries and copies at most 256 KiB. Opened files
are verified as regular files without following symlinks or blocking on FIFOs.

On completion, failure or timeout, the manager kills the entire execution
cgroup, waits for kernel-confirmed `populated 0` and guardian exit, closes relay
connections, and removes its runtime metadata/cgroup. Namespace destruction
releases all writable tmpfs data. Missing exit proof retains capacity and blocks
unsafe cleanup. Restart reconciliation uses validated private execution markers
and the **original delegated cgroup path**, never stale PID numbers. Do not move
unresolved runtime state to another unit or cgroup root. Worker journal tombstone
limits and diagnostic retention rules are the same as VM workers.

## Build and configuration

Use a Linux amd64 host with Landlock ABI6 or newer (normally Linux 6.12+ with the
LSM enabled), bubblewrap 0.9+ with unprivileged namespaces, cgroup v2 and systemd
delegation. Ubuntu 26.04 is a suitable host target; Ubuntu 24.04 needs a sufficiently
new HWE kernel. Confirm actual ABI/preflight rather than trusting a version string.
Install standard runner dependencies, e.g. on Ubuntu:

```sh
sudo apt-get install bubblewrap ca-certificates curl git libicu-dev libssl-dev zlib1g
make build test
./scripts/build-landlock-bundle.sh /absolute/private/bundle 8
```

The builder pins official runner 2.338.0 and verifies its official release SHA-256.
It creates `runner.tar.gz`, `manifest.json` and `SHA256SUMS`; no daemon is started.
The worker verifies every bundle checksum. Copy
[the example](../examples/landlock-worker.json), set private owned runtime/image
paths, the manifest digest printed by the builder, resource budgets and real
public-egress deny prefixes. Store configuration as 0600 or root/service-group 0640.
Every profile on that worker must be Landlock; run VM workers separately.

### Standalone one-job operation

Run inside a dedicated delegated systemd unit as an unprivileged account.
`chickadee-worker -check-landlock -config /path/config.json` validates configuration
and actual isolation without credentials, a TLS listener, a job or host-network
changes. The TLS example fields are ignored only in standalone/preflight modes.

The caller creates fresh repository/organization JIT using GitHub's API and
passes only its encoded configuration to private stdin:

```sh
chickadee-worker -config /path/config.json -jit-stdin < /private/fresh-jit
```

This command must run in that delegated unit, not a normal shell lacking resource
controls. It requires one profile and one slot. Use a fixed unit name while state
is unresolved. `RUNNER_RETIRED` confirms local cleanup, not job success; inspect
GitHub for the result. The caller owns removal of stale GitHub registrations after
failed standalone attempts. Delete protected temporary JIT input afterward and
never replay it. The core contains no vendor-account dependency.

### Keyless worker API operation

Configure the worker's mTLS identity/TLS files as described in
[worker APIv1](../workerapi/README.md), install
[the dedicated unit](../deploy/chickadee-landlock-worker.service), and create its
separate service account, protected configuration, readable trusted bundle and
owned state directory. Adjust the unit's CPU/memory/tasks budgets together with
configuration. No TAP/NAT service is required. Do not share an account, runtime
or delegated subtree with the VM worker. Do not expose the API publicly.

API v1 adds the `landlock` backend enum for independent self-hosted clients;
older clients reject it. A custom broker must update its pinned API dependency
and explicit backend validation before using a dedicated process worker.
Match backend, digest and resources, and verify inventory and cleanup before
accepting jobs. No Roost rollout or hosted queue activation is part of this feature.

## Verification

`make build test` includes negative archive traversal/symlink checks, protected
network/port refusals before dialing, private HTTP/2 relay streaming, mandatory
resource/network configuration and mixed-backend rejection, plus existing
reservation/replay/drain/uncertain-cleanup lifecycle tests. Real-kernel preflight
checks private writable storage, hidden management paths, immutable runtime,
private loopback and absence of direct external networking. Relay tests also
cover the complete CONNECT/socket bridge, denial propagation and host-address
changes after warm startup. A delegated opt-in kernel test leaves descendants
running and verifies both normal cleanup and recovery after guardian loss:

```sh
go test -race -c -o /tmp/chickadee-host-cgroup-tests ./internal/host
sudo systemd-run --unit=chickadee-cgroup-test --wait --collect \
  --property="User=$(id -un)" --property=Delegate=yes \
  --property=MemoryMax=2G --property=MemorySwapMax=0 \
  --property=TasksMax=640 --setenv=CHICKADEE_CGROUP_SMOKE=1 \
  /tmp/chickadee-host-cgroup-tests -test.v \
  -test.run '^TestRealExecutionGroupKillsDescendantsAndRecovers$'
```

### Prototype acceptance

[Real one-job acceptance](https://github.com/plover-digital/chickadee/actions/runs/38448046800)
passed checkout, native compilation, filesystem isolation, filtered HTTPS and
private loopback, including a deliberately surviving background descendant.
The execution retired, writable runtime was removed and the ephemeral GitHub
registration disappeared. This is prototype evidence, not a hostile-workload
security certification.

The test creates only its own execution cgroups in that transient unit. Normal
cleanup checks `populated 0` before removal; recovery kills surviving descendants
and requires the same kernel empty-group proof before removal. This does not
replace full controller-crash acceptance. A real Actions job and deployment
restart checks are required before relying on this backend.

Sources: [Landlock kernel documentation](https://docs.kernel.org/userspace-api/landlock.html),
[Go API](https://pkg.go.dev/github.com/landlock-lsm/go-landlock/landlock),
[GitHub proxy configuration](https://docs.github.com/en/actions/how-tos/manage-runners/use-proxy-servers),
[pinned runner environment input handling](https://github.com/actions/runner/blob/v2.338.0/src/Runner.Listener/CommandSettings.cs).
