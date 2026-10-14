# Investigating live workspace cleanup

[Issue22](https://github.com/plover-digital/chickadee/issues/22) reports live
workspace deletion in three-slot ABI5/Lustre worker-API operation. It is an open
multi-slot deployment blocker. The trace/regression changes here do **not** claim
to fix it. Local ABI6/V5 API reproductions kept all three live workspaces and
runtime identities intact past40seconds, then cleaned only after completion.
Target evidence is needed to identify the path responsible for the reported loss.

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
