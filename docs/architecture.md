# Architecture and trust model

The controller owns one dedicated GitHub runner scale set and one local image
profile. A single event loop owns pool state. One worker per VM owns its process,
serial socket and disk. QEMU runs as an unprivileged service user, with KVM and
an existing TAP interface. Hypervisor subprocesses receive a minimal PATH/locale environment rather than
inheriting controller credentials. The controller needs no network administration
capability; the separately reviewed root network setup creates the TAPs and NAT.

## Lifecycle

1. Create `vms/<random-id>/disk.qcow2` against the immutable base. Start QEMU
   microvm with a versioned host kernel and initrd, fixed vCPU/RAM and the TAP slot.
2. Connect the private Unix serial socket and await `READY` within
   the boot timeout. The root bootstrap has disabled terminal echo and canonical
   input, and owns `/dev/ttyS0`. No getty or kernel console writes to this channel.
   The guest waits indefinitely without GitHub credentials.
3. Poll the scale set, advertise maximum capacity, acquire available requests,
   and use `Statistics.TotalAssignedJobs` as demand. Messages are acknowledged
   after acquisition and delivery of the desired count to the local event loop.
   Demand describes the scale set, not a specific VM-to-job binding.
4. Reserve a READY VM, mark it irreversibly spent, and fsync a registration intent
   containing its unique runner name. Ask GitHub for fresh JIT configuration.
   Do not persist or log the JIT string. Even failure before delivery retires the VM.
5. Send `CONFIG`, await `ACK`, then `RUNNING`. Bootstrap invokes `Runner.Listener
   run --jitconfig ...` as UID/GID 1000, without sudo or host mounts. Scale-set JIT
   registrations are ephemeral, so the listener exits after at most one job.
6. Preserve bounded runner diagnostics as private serial LOG chunks; accept DONE
   or retire on timeout, EOF, malformed messages or VM exit. Kill QEMU, wait for
   process exit, then remove its entire VM directory. Create a new warm guest
   subject to maximum total capacity. Every replacement boots independently.

`max_vms` counts booting, warm, reserved and retiring VMs. Active runners are never
stopped solely because demand falls. Surplus warm guests can be retired. Credentialed
runners awaiting work are bounded by the job timeout even if demand becomes stale.
Transient statistics can provision an extra idle JIT runner; it still receives at
most one matching job and is never reused. There is no snapshot or suspend path.

## Serial protocol

Every newline-delimited JSON frame has `v: 1` and a fixed `type`. READY,
ACK and RUNNING carry no fields; CONFIG carries a nonempty base64 `jit`; LOG carries
base64 `data`; DONE carries an exit `code`. Unknown versions, types, fields, trailing
JSON, oversized frames and invalid payloads are rejected without logging raw input.
Frames are limited to 64 KiB, JIT to 48 KiB, diagnostic chunks to 8 KiB base64, and
saved diagnostics to 8 MiB including framing. ACK has a 15-second deadline; the
subsequent runner/status stream has a fixed total job deadline, never reset by
messages. Host pool state validates ordering independently of the decoder.

QEMU's serial socket uses `wait=on`: the host connects before the guest boots.
Bootstrap disables terminal echo, sends READY once, and waits for configuration.
This avoids losing initial bytes during UART/TTY initialization. There is no
unbounded console parsing or repeating READY stream in the warm pool. Guest status
is advisory and cannot mark a spent VM reusable. Runner exit code is an infrastructure
signal; inspect GitHub for the authoritative workflow job result.

Diagnostic files are untrusted and may contain job secrets. They are saved with
mode 0600, never forwarded to journald, and contain only bounded base64 frames.
Regular diagnostic files are opened without following symlinks. QEMU stderr is
separately capped at 16 KiB. The controller retains at most 64 historical files,
excluding files for current guests; use restricted external archival if needed.
Abrupt crashes, timeouts and a full guest disk can prevent guest log export.

## Restart and cleanup

Runtime directories are checked for owner-only permissions, correct ownership and
absence of symlinks. Image and runtime paths must not overlap. A filesystem lock prevents concurrent controllers using the same state directory.
Systemd kills the service cgroup; QEMU also has a parent-death SIGKILL. Linux
parent-death signaling follows the creating OS thread; the systemd cgroup remains
the primary shutdown boundary. Before
starting the pool, recovery scans `/proc` for QEMU executables using the owned
overlay directory and uses pidfds to avoid killing a reused PID. It polls the pidfd to confirm that
all process threads have exited
before deleting old VM directories. If exit cannot be confirmed, it retains disks
and fails closed. Other VM managers' processes are not intentionally matched.

Registration intents bind to the GitHub URL, runner group and scale-set name;
changing these settings fails closed before old state is discarded. Intents are
fsynced **before** a JIT API request, so a lost response
can be reconciled by runner name without having its ID. Only registrations in the
configured scale set are removed. Removal errors retain the intent and are retried.
Intents are retained for at least ten minutes from creation and checked again after
retirement to catch delayed registration after an ambiguous request; routine retry
runs every 30 seconds. This is best-effort recovery of an external API, not an atomic
transaction with GitHub. Registrations arriving after that grace period need manual
inspection. Preserve state and the original scale-set configuration across restart.

Local ownership and process/disk recovery precede App initialization or polling.
If GitHub cannot be reached, no new VMs are created and registration intents remain
for the next successful restart. Message sessions are closed
on normal shutdown; abrupt death can leave a session until GitHub expires it. A
session conflict may therefore delay restart. This prototype does not bypass session
ownership by forcibly resetting another listener.

## Resource and network boundaries

Guest vCPUs and memory are fixed QEMU arguments. Each overlay's virtual size equals
the built filesystem size; a per-QEMU file-size rlimit caps its physical file at
that size plus 1 GiB for metadata. The service disables swap. The generated systemd drop-in caps aggregate
CPU time, memory (including QEMU overhead) and process/task count. These are bounds
for this service, not a reservation against other host workloads. CPU limits do
not account for all host kernel networking work caused by guests. A dedicated
host/filesystem quota gives stronger operational isolation. Restart when changing
limits, after regenerating the systemd drop-in.

Each VM has a routed TAP and static IPv4 /30 subnet. nftables checks its exact
source IP, drops host-local/private/reserved destinations, cross-guest traffic,
IPv6 and unsolicited inbound packets, and NATs public outbound traffic through the
selected interface. No host TCP service handles guest control messages. Guests
receive no host Docker socket, writable host directory, App key or management token.
Network policy restricts destinations; it does not prevent internet exfiltration,
DNS misuse or traffic abuse. Existing firewall chains can still block valid traffic.

## Intended trust and limitations

The operator, host kernel, QEMU, signed Ubuntu inputs, runner release and controller
are trusted. Guests and serial messages are treated as untrusted. VM isolation
reduces cross-job persistence; it does not make this prototype a hardened hostile
multitenant service. QEMU/KVM escapes, side channels, disk snapshots/backups,
privileged host users and storage exhaustion remain relevant. Use it for trusted
repositories and reviewed workflows. Publishing this source repository does not
mean its runners should accept arbitrary public pull requests.

The image has Git, curl and build-essential for Go race tests, but no Docker or
preinstalled Go/language toolchain bundles. Container
jobs, Docker actions, service containers and sudo-dependent workflows are unsupported.
No shared caches, multi-host coordination, multiple profiles, dashboard, HA,
secure erase or offline filesystem forensic recovery are included. Auto-update is
disabled; rebuild with reviewed new runner/kernel/Ubuntu pins. GitHub may stop
assigning jobs if the runner is outdated; follow its
[runner update policy](https://docs.github.com/en/actions/reference/runners/self-hosted-runners#runner-software-updates-on-self-hosted-runners).

QEMU and the controller currently share a host UID. They do not share host files
with the guest, but a successful escape into QEMU could read the App key or other
VM state. Seccomp does not provide a per-VM filesystem boundary. Stronger UID,
namespace and MAC isolation, plus per-VM host resource control, must precede a
managed-service security claim. See the [Exa primary-source audit](research-audit.md).
