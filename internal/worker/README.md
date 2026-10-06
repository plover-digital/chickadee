# Keyless host engine and reservation journal

This package implements the keyless host engine and its durable reservation
journal. The engine boots shared credential-free guests, enforces local physical
resource limits, accepts one JIT delivery and confirms QEMU exit before disk
cleanup. `cmd/chickadee-worker` authenticates callers through the public
`workerapi` protocol. No GitHub App credentials or listeners belong here.
Existing standalone execution remains available.

The command authenticates each caller, and the engine enforces its resource
budget, checks immutable profile content and reserves an actual compatible READY
VM before committing `Reserve`. The identity includes a stable worker/broker ID and
an operator-controlled positive numeric generation. Increasing the generation
fences all earlier requests; decreasing it is rejected. Generation allocation is operator-managed; automatic multi-broker failover is
not implemented. Transport pins the configured broker and worker identities.

`Seal` irrevocably consumes the reservation before JIT generation. Roost must
separately journal its quota, assignment and single JIT-generation attempt.
`DeliverIntent` commits permission to attempt serial delivery once; only its
first successful caller may write credentials. Repeated delivery attempts return
`ErrConsumed`, including after restart or a lost acknowledgement. The caller must
burn the assignment on ambiguous failure, never retry credentials because an ACK
was lost. Any journal write error returns `ErrUncertain` and blocks further
admission until reopening and inspecting durable state.

`Terminal` requires host-established QEMU exit and disk removal facts matching
the VM ID. A guest completion frame, disconnected broker, guardian exit or lease
expiry is insufficient. `RecoverTerminal` is exclusively a local recovery
operation for old generations; it must not become a remote broker assertion of
process exit. Journal transitions cannot establish these facts themselves.

Records contain only identities, profile digest, resource shape and state. Files
are private, strictly decoded, atomically replaced and fsynced with their parent
directory, under exclusive local ownership. Snapshots are bounded to 2 MiB and
1024 records. Terminal tombstones are retained and VM IDs cannot be recycled;
the record budget deliberately stops admission when full. Safe pruning after
central acknowledgement and durable fencing is a future integration task.

Race tests cover duplicate intent, lost ACK/restart, monotonic generation fencing,
immutable shape, persistence failures on either side of commit, confirmed cleanup
requirements, file ownership/symlinks, malformed state and exhaustion. Crash
injection tests model journal interruption; they do not certify power-loss
behavior of every host filesystem.
