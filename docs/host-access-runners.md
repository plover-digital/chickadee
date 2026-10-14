# Optional host-integrated process runners

A Landlock profile may include `host_access` for trusted self-hosted workflows
that need local tools, authentication sockets and host/private networking.
This is independently deployed Chickadee software. **chickadee.run is VM-only.**

| Behavior | Sealed (no host_access) | Host-integrated |
| --- | --- | --- |
| Landlock | Strict V6, ABI6+ | Strict V5, ABI5+, no IPC scopes |
| Network | Private namespace/public HTTPS relay | Host namespace; host/private services reachable |
| Identity | Synthetic UID/GID1000 | Real service UID/primary GID; supplementary-group access unsupported |
| Writable storage | Bounded private tmpfs | Same tmpfs plus explicitly unbounded disk workspace |
| CPU/I/O | Existing mandatory controller requirements | CPU optional only with validated cpu_ids; explicit I/O limits still mandatory |
| Local lifecycle | One-use JIT, cgroup exit proof | Same; workspace contents cleaned after exit proof |

The private user/mount/PID/IPC/UTS/cgroup namespaces, no capabilities,
no-new-privileges and disabled nested user namespaces remain. Host integration
is an explicit authority grant, not VM-equivalent isolation. Any socket or network
service the sandbox can reach may act on the service identity outside the sandbox.
The sandbox does not constrain that service's authority or the work it launches.
Local cgroups and Landlock contain only the local process tree; **remote containment
is none**. Workflows must wait for externally dispatched work to finish, or it can
outlive the runner and lose its workspace. The external service owns cancellation
and resource enforcement. Same-UID remote processes may recover broader site
access than the local sandbox has.

## Configuration

Start with [the example](../examples/host-access-worker.json). It is illustrative:
replace paths, CPU IDs, bundle digest, worker identity, budgets and TLS settings with local
operator configuration. Do not put secrets in `env`: the nonsecret normalized
plan is passed in the bubblewrap command line. App/OAuth/gh credentials stay on
the host and are never added to the runner environment.

```json
"host_access": {
  "ro_paths": ["/opt/site-tools", "/opt/site-config"],
  "sockets": ["/run/site-service/authorization.sock"],
  "work_dir": "/shared/work/slot-{slot}",
  "env": {"PATH": "/opt/site-tools/bin:/usr/bin:/bin"}
}
```

`ro_paths` allows regular files/directories and reviewed tool symlinks; requested
aliases and canonical targets share one normalized mount/Landlock plan. Broad
host roots, management/image/workspace overlaps, conflicting mounts and special
files are rejected. Socket endpoints must be direct Unix sockets; only the
individual endpoint is mounted. `/var/run` aliases the sandbox's `/run`.
Both requested aliases and resolved targets are mounted, including symlinked
site prefixes. List the actual tool configuration paths, wherever the site
installed them; do not reflexively bind `/etc` subtrees. The `/var/run` alias
supports client libraries that use legacy paths, independently of which service
owns the socket. Public CAs and selected resolver/NSS files are mounted; private
keys and unrelated home directories are absent. Exposed runtime trees must be
free of secrets.
Host userland is mutable: policy identity does not certify every runtime file.

Operator `env` seeds the runner environment. PATH can select installed site
tools. JIT/Chickadee control variables, home/tmp/tool-cache, CA and proxy settings
are protected. No relay proxies are injected in host mode; this v1 does not
support operator proxy overrides. Limits: 32 read-only paths, 16 sockets,
128 environment entries, 8192 bytes/value and 64KiB total child plan.
No activation script, Docker/service containers, sudo or package installation
is supplied. Modules and dynamic runtime dependencies must already be installed
and exposed by the site's manifest. JavaScript/composite actions remain subject
to the actual host runtime and are covered by real-job acceptance.

## Workspaces and concurrency

A literal work_dir is valid with one slot, including standalone `-jit-stdin`.
Multi-slot host access is **implemented, not yet target-validated on ABI5**.
Two concurrent sandboxes/cleanup were tested locally on ABI6 with strict V5;
the ABI5 owner exercised only standalone single-slot operation. Validate multiple
warm slots, reservation/JIT paths, isolation and recovery on the actual site before
expanding capacity. For concurrent workers, use exactly one `{slot}` token: existing scheduler slots
expand it to decimal1–32. Precreate each directory as the service user, mode0700,
empty, with no symlinks in any component. Workspaces must be outside `/usr`,
`/bin`, `/sbin`, `/lib` and `/lib64`; their read-only runtime mounts would hide
a writable bind beneath them. The worker locks the directory and pins
its object for mounts and cleanup; another instance cannot use that workspace.
Do not share it with another application or point it at persistent shared scratch.

The directory is bound at the **same absolute path**, so remote nodes can resolve
executables, cwd, inputs and outputs. If an external executor resolves paths
itself, the workspace must be on a shared filesystem visible at this identical
path on both sides. Host/private namespace parents are not mounted wholesale and
sibling slots remain hidden locally; same-UID remote access
is not confined by this worker. `disk_gib` bounds only private tmpfs. It does not
cap shared disk consumption; the operator explicitly accepts the unbounded
workspace and manages site storage capacity. An ordinary directory has no
Chickadee-enforced disk quota: a workflow can fill its backing filesystem and
disrupt other runners or management writes. Use site-managed quotas or separate
storage to protect journals/logs and other workloads. This v1 neither validates
nor provisions those controls; it is an operator responsibility, not a disk-limit
guarantee. Sealed/VM storage limits remain unchanged.

After kernel-confirmed whole-tree exit and guardian reaping, only contents of
the matching pinned directory are deleted. The directory/parent are retained.
Unmatched dirty state, changed ownership/mode/inode, policy mismatch or missing
exit proof fail closed. Normal startup reaps matching owned crash state before
requiring emptiness for a replacement; read-only checks never delete data.
Drain before policy changes/upgrades. Version1 sealed markers remain recoverable;
version2 host markers require the original policy, workspace and delegated unit.
Unknown nonempty directories require operator inspection, not guessed deletion.
After terminal exit proof, `work_folder` is removed from the private journal;
assignment/VM tombstones remain. Existing terminal metadata is scrubbed atomically
when the new worker opens it. This permits rollback to the preceding worker after
a full drain/recovery **and restoration of the old compatible configuration/unit**;
active host records remain intentionally incompatible. An older binary cannot run
host_access on an ABI5/memory-pids-only host.
Do not delete tombstones to downgrade.

## Prerequisites and checking

Build the pinned runner bundle as in [Landlock setup](landlock-runners.md).
Use a pinned modern bubblewrap supporting all used flags, including `--bind-fd`,
`--ro-bind-fd`, `--disable-userns` and `--assert-userns-disabled`. An old installed
binary is not used as a fallback. Bubblewrap is resolved to an absolute path from
**the worker's PATH**, shared by preflight and launch; affinity cannot select a
second binary from the sandbox's minimal PATH. Preflight prints that path.

If the site package is too old, build unprivileged with GCC, pkg-config and Ninja
already available. This uses the official 0.11.0 source commit; no sudo/setuid install:

```sh
python3 -m pip install --user meson
export PATH="$HOME/.local/bin:$PATH"
git clone https://github.com/containers/bubblewrap.git bubblewrap-0.11.0
cd bubblewrap-0.11.0
git checkout --detach 9ca3b05ec787acfb4b17bed37db5719fa777834f
meson setup build --prefix="$HOME/.local" -Dman=disabled -Dbash_completion=disabled -Dzsh_completion=disabled
ninja -C build
ninja -C build install
bwrap --version
```

See the [upstream build instructions](https://github.com/containers/bubblewrap/blob/v0.11.0/README.md#installation).
Set that PATH in the actual worker service/scope, not just an interactive shell. Unprivileged user namespaces, openat2 no-symlink resolution and delegated
cgroup-v2 controls must work on the actual distribution/backported kernel.

The existing controller-leaf implementation avoids requiring DelegateSubgroup.
Actual delegation must be checked on each target system; the reported single-slot
ABI5 fixture used systemd252 successfully. Memory, pids, swap-zero and OOM-group
controls are mandatory. If cpu is not delegated, configure the existing `cpu_ids`
pool: a profile's `cpus` selects that many IDs per execution, and
`budget.max_cpus` must be no larger than `len(cpu_ids)`. The IDs must be distinct,
online and permitted by the worker's affinity. For example, a four-ID pool and
max_cpus=4 can admit two profiles with cpus=2, subject to VM/memory budgets. Allocation is disjoint within
this worker and inherited by descendants; it is not CPU quota or exclusive host
CPU ownership. Trusted jobs can change their affinity within the host's cpuset
permissions; this is starting placement, not a hostile-job CPU ceiling. Configure parent memory/tasks budgets to cover all executions,
per-execution overhead and controller headroom. Any explicitly requested I/O
weight/limits require a usable io controller.

If adapting [the sealed worker unit](../deploy/chickadee-landlock-worker.service),
add the precreated workspace paths to `ReadWritePaths`: its `ProtectSystem=strict`
otherwise makes shared disk paths read-only. Keep management directories private.
`ProtectHome` and `PrivateTmp` must also permit the operator-selected runtime and
workspace paths in the service's view. The host namespace means the worker's
network namespace, so do not enable systemd `PrivateNetwork` for host access.
On memory/pids-only sites request those delegated controllers and omit CPU/I/O
unit settings that the site cannot provide; retain bounded parent memory/swap/tasks.
Run the check through the final unit, not just an unrestricted shell.

Run inside the delegated service/scope:

```sh
chickadee-worker -config /private/config.json -check-landlock
```

This validates each profile's slot1 namespaces, V5 enforcement, service identity,
workspace permissions, configured socket connection and host namespace identity.
Config validation covers all slot paths. Generic `-check` runs the same per-profile
sandbox probes after validating listener/TLS settings. Both check modes are
read-only; normal startup still reconciles matching owned crash state first. Socket connection proves reachability,
not authentication, authorization or what the peer service can do. Site
acceptance must verify the intended identity and authority separately.
Reports expose no env/JIT values. Dirty workspaces fail this read-only check;
normal worker startup can reconcile matching owned markers first.

`job_timeout_seconds` covers the entire local Run, including time the workflow
spends waiting in an external service's queue and waiting for its work to finish.
Sites with long queue waits may need hours; the current configurable range is
1–14400 seconds (four hours). On timeout, Chickadee terminates the local tree,
then cleans its workspace. This does not cancel external work, which can be
orphaned and lose its working directory as described in the trust boundary above.

## GitHub control plane

The backend still needs a trusted JIT caller or an mTLS worker-API broker. It
contains no GitHub listener or hosted account dependency. The caller owns stale
runner-registration reconciliation; never replay a JIT after credential intent.

For standalone use, max_vms=1 and one profile. The caller knows the literal
workspace (or template slot1) before JIT generation. Supply that exact path as
GitHub's `work_folder`, and deliver only fresh encoded JIT through private stdin.
A caller running in the same delegated scope can capture GitHub output privately
and replace itself with the worker, leaving no unrelated scope process:

```python
# Run this trusted wrapper in the delegated scope. No credential output.
import json, os, subprocess
payload = {"name": "unique-one-job-runner", "runner_group_id": 1,
           "labels": ["site-actions"], "work_folder": "/shared/work/slot-1"}
r = subprocess.run(["gh", "api", "--method", "POST",
                    "repos/OWNER/REPO/actions/runners/generate-jitconfig",
                    "--input", "-"], input=json.dumps(payload), text=True,
                   capture_output=True, check=True)
response = json.loads(r.stdout)
# Preserve response['runner']['id'] in protected caller-owned registration state.
fd = os.memfd_create("fresh-jit", os.MFD_CLOEXEC)
os.write(fd, response["encoded_jit_config"].encode()); os.lseek(fd, 0, 0)
os.dup2(fd, 0); os.close(fd)
os.execv("/usr/local/bin/chickadee-worker",
         ["chickadee-worker", "-config", "/private/config.json", "-jit-stdin"])
```

Use a unique name every attempt. Check the result in GitHub: RUNNER_RETIRED only
confirms local retirement. Standalone mode exits nonzero when local Run returns
without a valid DONE sequence (EOF, protocol error or deadline), even if cleanup
succeeds; the host emits a credential-free warning with the execution ID.
A DONE frame, including the runner's exit code, is not a GitHub job-success assertion. If caller/runner fails before ephemeral deregistration,
reconcile local exit and remove only the exact owned GitHub runner registration.
The actual checkout/build path must be disk-backed, not a default `_work` in tmpfs.

For worker API operation: Reserve(profile) → Seal(assignment) → generate fresh
JIT with `record.request.work_folder` → Deliver(assignment,JIT) → Status. The
path is worker-selected response metadata, not a remote mount/path input. Worker
APIv1 adds optional `work_folder` in records; older strict clients must upgrade
before using host-integrated workers. Existing sealed/VM records omit the field.
Update the client's pinned core dependency first, drain the worker, upgrade it,
then enable only a separately authorized self-hosted queue. Roost is unchanged.
Older binaries cannot downgrade through unresolved populated host records.

## Acceptance

`make build test` includes normalization, 32-slot paths, policy separation,
private directory locks, substituted-directory/symlink refusal and API metadata.
Opt-in kernel suites run only in their own delegated units:

```sh
CGO_ENABLED=0 go test -c -o /tmp/host-access-tests ./internal/landlockrunner
# In an isolated delegated unit with bounded memory/tasks and a verified bundle:
CHICKADEE_HOST_ACCESS_SMOKE=1 CHICKADEE_TEST_RUNNER_BUNDLE=/private/bundle \
  /tmp/host-access-tests -test.v -test.run '^TestRealHostAccessConcurrentCleanupAndRecovery$'
go test -race -c -o /tmp/host-cgroup-tests ./internal/host
CHICKADEE_CGROUP_SMOKE=1 /tmp/host-cgroup-tests -test.v \
  -test.run '^TestRealHostAccessMemoryPidsOnly$'
```

Use the production-style non-race child for archive/bootstrap timing; normal
unit/lifecycle tests still run with Go race instrumentation. The kernel tests
verify two credential-free sandboxes, sibling-safe cleanup, recovery markers and
actual memory/pids-only delegation without relaxing sealed requirements.

Local real-job acceptance also passed: [workflow 38465413623](https://github.com/plover-digital/chickadee/actions/runs/38465413623)
completed checkout, native C build/run, disk-workspace verification, direct HTTPS,
an ephemeral-port loopback server and a leftover local descendant. Retirement
removed the execution runtime, left the workspace empty and retained, and removed
the ephemeral GitHub registration. This used a newer ABI6 kernel enforcing V5;
it does not replace target-host acceptance. A preceding attempt failed because
the fixture used an occupied host port; the workflow now allocates its own port.

A separate [controlled crash run 38465917203](https://github.com/plover-digital/chickadee/actions/runs/38465917203)
was interrupted after checkout/build wrote an owned workspace file. The dedicated
controller was killed, its delegated cgroup disappeared, and dirty contents/private
marker were retained. Restart in the same delegated unit with a nonsecret invalid
JIT fixture reclaimed the old execution, cleared the contents and retired the new
fixture execution; the workspace directory survived. This tests local restart,
not cancellation by an external execution service. GitHub cancellation/owned registration removal
is the trusted caller's separate responsibility; a dead runner may require
[force cancellation](https://docs.github.com/en/rest/actions/workflow-runs#force-cancel-a-workflow-run)
when ordinary cancellation cannot complete.

The target owner subsequently reported an ABI5 fixture run of this lifecycle
([PR review](https://github.com/plover-digital/chickadee/pull/19#issuecomment-6156403356)):
memory/pids-only delegation, Lustre same-path storage, Munge authentication,
interactive/batch Slurm, four-rank launch, orphan cleanup and whole-group OOM
cleanup passed after resolving the selected bubblewrap before affinity wrapping.
That report uses a substitute run.sh workload, not the actual GitHub runner/JIT
connection to GHE. The affinity, completion-reporting and drained-journal fixes
now have regression coverage; real local DONE/interrupted CLI fixtures also pass.

The target owner re-verified an unpatched f0535aa build and reported successful
single-slot functional/OOM cleanup plus GHE DNS, HTTPS with certificate verification,
and required .NET library visibility ([follow-up](https://github.com/plover-digital/chickadee/pull/19#issuecomment-6172775096)).
This removes known host-side obstacles; an actual runner-to-GHE fresh-JIT job
still has not been exercised there. Multi-slot ABI5 operation is also outstanding.

Site acceptance should use a real fresh-JIT checkout/build, verify the exposed
service identity/authority and external execution paths, and cover local path
denials, OOM/PID containment, cancellation, socket replacement, controller restart,
workspace cleanup and the observed external-work boundary. Local ABI6 results
and substitute runner fixtures do not certify those untested deployment paths.

## Worked example: HPC scheduler login host

[The HPC example](../examples/host-access-hpc-scheduler.json) illustrates the
reported fixture, not a backend dependency: read-only site tools/configuration,
a Munge authentication socket, and a same-path shared workspace. Slurm clients
can use `srun` or `sbatch --wait` to wait for dispatched work; queue wait consumes
the local timeout too. A prefix-installed slurm.conf needs its actual prefix path,
not an automatic `/etc/slurm` bind. Lustre and MPI were used in the validation
record above; other filesystems and external services follow the same generic
path, authority and lifecycle rules. No scheduler adapter is implemented.
