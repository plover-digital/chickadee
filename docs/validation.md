# Validation status and deployment acceptance

The controller and guest compile against the actual pinned scale-set client.
`go.mod` and `go.sum` retain the verified dependency graph. All package tests pass
with the race detector. The Unix socket subprocess simulator also passed on the
underlying host in an uncached run. The [reference image build](https://github.com/plover-digital/chickadee/actions/runs/37404427019)
succeeded with verified inputs and output checksums. Its hosted KVM check was
skipped because the builder lacked KVM access; this was not counted as a boot pass.

The downloaded artifact passed all checksums locally. Two independently booted
KVM microvms reached READY with the real guest bootstrap, Ubuntu kernel
`6.8.0-142-generic`, runner image `2.337.0`, and fresh overlays. After each READY,
the controller confirmed QEMU exit and deleted its VM directory/disk. The second
boot exercised the same creation path after destruction. This was the
credential-free `-boot-check` mode with restricted QEMU user networking, not a
GitHub-connected pool or a real job. No App key was read or JIT delivered.

The initial boot failure exposed QEMU default ACPI behavior; pinning `acpi=off`
allowed direct-kernel boot. The old image's resolver symlink prevented its network
service starting; writing a regular resolver after libguestfs customization fixed
that failure. Native lifecycle tests pass after the launcher change. No host
TAP/firewall/NAT changes or installation were made during this acceptance.

GitHub App installation, live scale-set/JIT behavior, real jobs, production
network isolation, and deployment/restart acceptance remain unfinished.

## Checks available without deployment

`make test-offline` runs protocol, pool, host and configuration tests under Go's
race detector. Lifecycle tests use an in-memory serial connection and fake
infrastructure, exercising irreversible credential intent, completion/failure,
overlay deletion, replacement, shutdown and registration cleanup retries. Host
tests use a real child process to check deletion waits for exit. Protocol tests
reject malformed/unbounded messages and offer a fuzz target. `go vet` covers these
packages. Shell scripts are checked with `bash -n`.

A separate lifecycle test uses real subprocesses and a Unix socket **simulator**;
it runs on an unrestricted Linux host and skips if Unix binding is denied. It
still does not test QEMU/KVM or a GitHub runner. `make build test` adds compilation
against the actual pinned client and all package tests. The pinned-client build has passed and `go.sum` is retained. Inspect CI and
complete real-host acceptance before calling the checkout release-ready.

## Real-host acceptance

Perform on the documented Ubuntu host with explicit approval for network setup
and GitHub operations. Record results outside the public checkout if they contain
host paths, account names, job details or other private infrastructure information.

1. Build the image and verify signed inputs, output checksums, package manifest,
   kernel version, virtio-mmio support and runner version.
2. Start with warm pool 1, max VMs 2. Observe READY without any runner registered.
   Check QEMU is KVM-accelerated, resources match configuration, and the overlay
   refers to the immutable base image.
3. Install and dispatch the manual smoke workflow. Confirm both real jobs succeed,
   their runner names differ, and GitHub chooses jobs without a VM-to-job binding.
4. Correlate host events with runner names. Confirm original QEMU exited, original
   VM directory/overlay was deleted, diagnostics were saved, and a fresh warm guest
   reached READY. Confirm the old GitHub registration disappeared.
5. From an approved guest test job check public HTTPS/DNS work; host-local, private
   networks and other guest addresses are blocked. Confirm no host Docker socket,
   writable share, sudo privilege or App credential is exposed.
6. Queue more jobs than max VMs. Check total QEMU count and active disk count never
   exceed the configured maximum, including boots and retirements. Verify systemd
   aggregate quotas and per-QEMU file size limits.
7. Kill a warm QEMU and then a credentialed QEMU. Both should be replaced; the
   latter's name must never be reused. Trigger job timeout and malformed serial
   input in a disposable test image; verify destruction and bounded logs.
8. Interrupt/restart the controller while a job runs, then simulate an abrupt
   controller crash. Confirm all owned children exit, disks are removed only after
   exit confirmation, journaled registrations reconcile and pool replenishes.
   Check behavior with GitHub temporarily unavailable and stale message sessions.
9. Reboot the host and verify TAPs/firewall restore before pool startup. Test
   uninstall on the dedicated host; check other firewall tables remain intact and
   the original forwarding setting returns.

Do not label the real vertical slice complete until steps 1–4 have succeeded.
For a public self-deployment release, also complete the isolation, failure,
resource-limit and restart checks. A green unit-test suite alone cannot establish
these properties.

## Official API evaluation

The integration is pinned to `github.com/actions/scaleset v0.4.0`. Its
[go.mod](https://github.com/actions/scaleset/blob/v0.4.0/go.mod) declares Go 1.25.3.
The root client uses JWT, UUID and retryable HTTP dependencies; the module also
lists Docker/Cobra/example and tool dependencies. Chickadee imports the root client
only and requires no Docker daemon or Kubernetes runtime. The dependency graph has been resolved and compiled online; verified checksums
are retained in `go.sum`.

The pinned [client source](https://github.com/actions/scaleset/blob/v0.4.0/client.go)
provides App authentication, scale-set lookup/create, JIT generation and runner
lookup/removal. The [message-session source](https://github.com/actions/scaleset/blob/v0.4.0/session_client.go)
provides polling, acquisition, acknowledgment, token refresh and Close. We use those
primitives directly; v0.4's listener convenience package has different scaler and
acknowledgment behavior from current main. Demand uses aggregate statistics, never
individual message counts. Latest main has different Go/dependency requirements
and listener interfaces; replacing the pin requires a deliberate review.
