# Investigating live workspace cleanup

[Issue22](https://github.com/plover-digital/chickadee/issues/22) was traced by
the target operator to a second controller on another cluster node sharing the
same state and workspaces. Host-local filesystem locks (`localflock`) did not
exclude the second controller. Its recovery cleared the first host's live state.
The process-attributed logs in this PR exposed that path. After pinning the user
units to one host, the target operator reports successful three-job concurrency
and a passing 80.49-second ABI5/Lustre regression. These changes add diagnostics
and regression coverage; they do not yet implement cross-host recovery fencing.

Use node-local `state_dir` and one controller per owned workspace set. When home
is shared, enabled lingering systemd user units can start on compute nodes as
soon as a job starts that user's session. Pin both worker and any broker units
with `ConditionHost=<runner-host>` in their `[Unit]` sections, or install units
outside the shared home. Do not rely on host-local locks as distributed ownership.
Host-identity fencing for recovery is tracked in
[issue #25](https://github.com/plover-digital/chickadee/issues/25); retaining unfamiliar
state must take precedence over automatically deleting it.

The opt-in real-kernel regression uses the existing authenticated worker API,
three credential-free warm sandboxes, three noncredential fixture configurations,
80-second shell workloads and a40-second reservation expiry. It verifies capacity,
original runtime identities and workspace sentinels for50seconds; then drains,
requires a retained diagnostic completion marker emitted only after the entire
80-second sentinel loop succeeds, waits for terminal proof and verifies empty
retained workspaces. Terminal state alone does not establish fixture success.
It creates only its private temporary state, generated test TLS files and three local slots.
It does not register GitHub runners or dispatch external jobs. Its API listener
binds loopback only; it changes no interfaces, routes, firewall rules or NAT.
Failed cleanup retains its private fixtures for inspection.

Build and run in an isolated, unprivileged delegated unit. The unit needs a5GiB
memory ceiling, swap-zero, at least three allowed CPUs and adequate PID headroom:

```sh
CGO_ENABLED=0 go test -c -o /tmp/chickadee-worker-concurrency.test ./cmd/chickadee-worker
systemd-run --user --scope -p Delegate=yes -p MemoryMax=5G -p MemorySwapMax=0 -p TasksMax=640 \
  env CHICKADEE_HOST_ACCESS_SMOKE=1 \
  /tmp/chickadee-worker-concurrency.test \
  -test.run '^TestRealConcurrentHostAccessViaWorkerAPIKeepsLiveWorkspaces$' -test.v
```

To exercise the site's shared filesystem, additionally set
`CHICKADEE_TEST_WORKSPACE_ROOT` to an operator-owned parent on that filesystem.
The test creates a new private child directory; it never uses an existing job
workspace. A system unit with `User=...` is an alternative where user delegation
is unavailable. Do not change global host security policy to bypass a failed check.

Startup/READY/clear logs now include the host controller PID and execution ID;
READY also includes the scheduler slot. Clear records include whether startup and
credential intent occurred. They add no paths, secrets or guest-controlled text.
Correlate these with inventory and the owned marker before concluding which
process cleared a workspace. Share sanitized structure/counts in the public issue;
keep private host/customer paths and credentials out of public artifacts.
