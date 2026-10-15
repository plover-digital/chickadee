# Linux single-host state ownership

Linux controllers bind state to a unique host before opening the worker journal,
initializing cgroups, or recovering processes/disks. This prevents accidental
recovery by a second cluster node when state is on a shared filesystem. It does
not turn shared state into a distributed scheduler, and it is not protection
against a malicious service user or host administrator.

Prefer node-local state, runtime disks and workspaces. Pin the service to its
intended host; never copy a live deployment to start a second listener.

## Identity and on-disk compatibility

`host-owner-v1/identity.json` contains ownership schema version 1, a SHA-256 hash
of the independently read `/etc/machine-id`, and the boot ID read from
`/proc/sys/kernel/random/boot_id`. A configured worker ID, PID, hostname, and
local `flock` are not host identity. Machine IDs must be unique: regenerate them
when cloning host installations. Changing a machine ID requires verified manual
migration. Running the engine inside containers that share or substitute host
identity is unsupported by this safeguard.

An atomic directory creation elects the first owner. The controller never
replaces or steals this claim. An incomplete claim (including a crash before
writing or syncing its identity) fails closed and requires operator inspection.
The filesystem must provide globally atomic `mkdir`, coherent directory/file
visibility and durable file/directory `fsync`. Filesystems that cannot provide
these semantics are unsupported; local-only `flock` is not a substitute.

The same host may reopen state on the same boot or after a reboot without
rewriting the claim. Landlock runtime markers now use version 3 and include both
host and boot identity. All runtime host markers are checked before recovery.
Same-boot recovery still requires whole-cgroup exit proof. A previous-boot
execution cannot survive the reboot, but its exact cgroup must be absent before
its workspace is cleared; an unexpected existing cgroup causes refusal.
Legacy version 1/2 sandbox markers are not adopted.

Each host_access workspace also has a sibling claim, for example
`.slot-1.chickadee-owner-v1/identity.json` beside `slot-1`. This metadata is outside
the guest-writable workspace and remains after job cleanup. A different state
directory therefore cannot bypass foreign-host workspace ownership. Ordinary
workspace cleanup still preserves the workspace directory itself.

No worker API, config, or reservation journal version changes. The Linux VM
controller and worker use the ownership claim; the experimental macOS backend
is unchanged. Old binaries do not enforce the claim and must not run against
state/workspaces used by this version.

## Upgrade and recovery

1. Drain every controller using the old state or workspace directories on every
   node. Confirm all execution process trees have exited and workspaces are empty.
   Do not use a missing local cgroup as proof for a remote node.
2. Preserve old state, registration intents and bounded diagnostics privately.
   Complete old-binary GitHub registration cleanup before retiring that state;
   do not discard registration intents or consumed-assignment tombstones.
3. Configure a **fresh empty state directory** and fresh empty workspace slots.
   Where a reservation journal is used, assign a fresh worker ID and retire the
   old worker identity at the broker before using a fresh journal, so old consumed
   assignments cannot be replayed.
   For worker API deployments, issue a worker certificate with the matching new
   worker URI identity and update the broker TLS peer identity/configuration too.
   Start only the intended host with the new binary. It claims these directories
   automatically. Do not copy a populated unbound state tree into the new one.
4. Start other nodes with their own directories and distinct worker identities;
   keep one GitHub listener per queue. Roll out engine ownership enforcement
   before enabling any new nodes. No Roost protocol upgrade is required.

Existing unbound state, foreign owner, legacy runtime markers or incomplete
claims stop startup with a bounded reason, leaving runtime contents intact.
There is intentionally no `force`, timeout-based takeover, or automatic adoption.
For an incomplete claim, first prove every old controller/execution is stopped
and then choose a new empty directory. For ambiguous live state, retain it and
investigate on its owning host rather than deleting markers to bypass the fence.

Workspace slots must be ordinary directories directly under their storage parent.
The kernel's `openat2(RESOLVE_NO_XDEV)` check rejects a slot that is itself a
filesystem or bind mount: its sibling ownership claim could otherwise live on a
different host-local filesystem. Mount shared storage at the parent directory
and create ordinary slot directories underneath it. Symlink aliases are also
rejected. Filesystem aliases or cloned machine identities must not be used to
present one workspace as independently owned slots.
