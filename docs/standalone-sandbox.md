# Standalone GitHub sandbox runners

One persistent `chickadee-worker -github` process listens for GitHub Actions
scale-set demand and manages a local pool of disposable process runners. It
supports one queue and one Landlock profile, with configurable warm capacity
and up to32 slots within the existing CPU/memory budgets. No Roost account,
worker API listener, TLS certificate, database or Kubernetes is needed.

This is a **trusted self-hosted option**. chickadee.run remains VM-only.
[Sealed Landlock](landlock-runners.md) requires ABI6 and private networking;
explicit [host_access](host-access-runners.md) supports ABI5, selected site tools,
service authority and host networking. Their existing trust/limit distinctions
still apply. Use separate state directories and delegated units for VM and
sandbox controllers. Do not run another listener for the same GitHub scale set.

## Configure and start

1. Build/install `chickadee-worker` and the verified runner bundle using
   [Landlock setup](landlock-runners.md). This mode uses the existing worker
   JSON configuration, version1, plus the optional `github` section.
2. Copy [the standalone example](../examples/standalone-sandbox.json), replace
   the bundle digest, paths, CPU IDs, identity and resource budgets. The example
   selects host_access; remove it and add the required `landlock_deny_prefixes`
   to use sealed isolation. Use a dedicated scale-set name as the workflow label.
3. Set up a GitHub App as in [installation](install.md), with organization
   Self-hosted runners write or repository Administration write as appropriate.
   Install it in the configured scope. Store its private key outside the repo,
   mode0600, readable only by the controller account. Supply its client ID,
   installation ID and runner group ID in `github`. Public github.com is the default. For GHES, set `github_host` to the
   exact trusted host (and port when nondefault) and use its HTTPS org/repo URL.
   For example, `github_host: "ghe.example.net"` with
   `github_url: "https://ghe.example.net/example-org"`. Create/install the App
   on that appliance. Normal TLS verification remains mandatory; install its
   trusted CA on the host and expose the corresponding public CA bundle to the
   runner. There is no insecure-TLS option or implicit trust of arbitrary hosts.
4. Precreate all slot workspaces when using host_access. Keep writable workspace
   storage separate from controller journals/logs where possible. Keep configuration
   private and outside built-in runtime mounts. The App key path is added to the
   protected mount paths automatically.
5. Run under the same bounded, delegated systemd unit used for the process worker,
   changing ExecStart to:

```sh
/usr/local/bin/chickadee-worker -config /etc/chickadee-sandbox/config.json -github
```

Make sure the runner group allows the target repository and, when configured,
the exact workflow/ref. Public repositories require the group's public-repository
permission. A valid label alone does not override those GitHub access policies.

Use `-github -check` first under that unit to verify the configuration, key file
permissions, pinned bundle, cgroup budget and real namespace/Landlock probes,
without contacting GitHub. No `listen` or `tls` section is needed. Merely adding
`github` to the config does not activate this mode: `-github` is explicit, and
ordinary keyless worker/one-shot operation rejects the credential-bearing config.

The [example systemd unit](../deploy/chickadee-sandbox-github.service) has a bounded
sealed-runtime baseline. Adapt memory/CPU/tasks limits and bundle paths to your
profile. For host_access, add the precreated workspace paths to `ReadWritePaths`
and use the actual supported delegation, as described in the host-access guide.

A workflow targets the scale-set name:

```yaml
jobs:
  build:
    runs-on: chickadee-sandbox
    steps:
      - uses: actions/checkout@v5
      - run: make build test
```

## Lifecycle and recovery

Warm sandboxes have no GitHub credentials and are not registered. Demand comes
from `statistics.TotalAssignedJobs`, not individual job messages. Chickadee
reserves a warm slot, seals it, persists a private registration intent, generates
one fresh JIT with that slot's workspace and delivers it once. GitHub chooses
any matching job; a slot is not bound to a particular workflow job by Chickadee.
The runner exits after one job. The existing engine confirms process-tree exit,
cleans writable state and replenishes the pool.

Failed provisioning destroys the reserved sandbox. A timeout/ambiguous JIT
response is never retried against it. Failures back off before further admission.
The controller's App key or PAT stays on the host; only per-runner JIT enters the sandbox.
No JIT or credential values are logged. Startup failures include a fixed
`reason` category: config-invalid, app-key-unreadable, token-file-unreadable, github-auth-failed,
scale-set-lookup-failed, scale-set-create-failed, scale-set-policy-invalid,
engine-open-failed or engine-check-failed. SDK responses/raw errors are never
printed. For lookup/auth failures, check the configured App, installation and
permissions; engine-open includes local startup/recovery checks.

Registration intents live under `state_dir/github/records`, separately from
local engine state. Cleanup removes the exact owned GitHub registration only
when the local assignment is terminal. Unknown/uncertain local state is retained.
A ten-minute NotBefore guard keeps the intent for repeated removal in case a late
JIT request registers after an ambiguous response. A GitHub outage retains the
intent for retry. Restart first reaps owned local executions, then reconciles
registrations; it never replays JIT. Restore the original queue/scope/installation
configuration to clean older intents before changing that identity.

SIGTERM, SIGINT and SIGUSR1 stop polling/provisioning and drain delivered
jobs. SIGHUP preserves the listener and all executions; it does not apply
configuration changes. There is no live configuration reload in this mode. Keep systemd
`KillMode=mixed` and a stop deadline long enough for the configured job timeout
plus cleanup. Drain before binary/image/policy changes. For recovery without
warm creation or demand polling, run in the same delegated unit:

```sh
chickadee-worker -config /etc/chickadee-sandbox/config.json -github -cleanup-github
```

Cleanup does not create a missing scale set. If the late-registration guard has
not expired, it reports pending cleanup; retry later. This does not cancel external
work launched through exposed host services, nor force-cancel GitHub workflow runs.

The existing engine journal has a bounded1024-assignment lifetime and deliberately
stops admission when full. Safe tombstone pruning remains separate work: do not
manually delete engine records while registration intents remain. This prototype
adds no billing, usage dashboard, multi-profile routing or general scheduler.

The integration reuses the pinned official [scaleset client v0.4.0](https://github.com/actions/scaleset/blob/v0.4.0/README.md)
and its JIT/message-session APIs. Older worker binaries reject this optional
configuration; upgrade the binary before enabling it. Worker API v1 is unchanged.

## Acceptance evidence

Two real standalone-demand jobs passed: [38621672450](https://github.com/plover-digital/chickadee/actions/runs/38621672450)
and [38621885608](https://github.com/plover-digital/chickadee/actions/runs/38621885608).
They ran on distinct runners through the same persistent one-slot controller:
checkout, C compilation, private writable state, HTTPS, disk workspace and a
leftover descendant. The first used sandbox retired and its empty retained
workspace was replaced by a distinct credential-free READY execution before the
second job. No manual JIT input or worker API was used. A dedicated temporary
runner group allowed only this trusted acceptance workflow. This validates the
new GitHub adapter on local ABI6 enforcing V5, not target GHES/ABI5 acceptance. Target-owner reports in
[issue21](https://github.com/plover-digital/chickadee/issues/21) now establish real
ABI5/GHES runner jobs and multi-slot broker operation, while
[issue22](https://github.com/plover-digital/chickadee/issues/22) was traced by the
target operator to duplicate controllers on different nodes sharing state through
a filesystem with host-local locks. After pinning services to one host, the target
operator reports successful three-job concurrency. Keep state node-local and pin
shared-home user services with `ConditionHost=`; filesystem locks alone cannot
fence another host using `localflock`. Integrated standalone GHES acceptance
still requires the target authentication and workflow test.

## Repository PAT authentication

For a trusted self-hosted repository controller that cannot install an App,
replace all three `app_*` fields with `token_file: "/private/chickadee-pat"`.
The modes are mutually exclusive; PAT mode accepts a repository URL only.
Use a classic PAT with `repo` scope and repository administrator access, subject
to enterprise/SSO policy. [GitHub documents the required runner permissions](https://docs.github.com/en/actions/how-tos/manage-runners/use-actions-runner-controller/authenticate-to-the-api).
An App remains preferred when available: a PAT carries broader personal
repository authority and is not a least-privilege runner credential.

Store the token alone in a regular mode-0600 file outside this repository and
sandbox mounts. The controller reads it at startup, protects its path just like
the App key, and sends only fresh per-runner JIT into jobs. It does not log token
contents or raw authentication errors. Rotate the token with a controlled drain
and restart. `token-file-unreadable` distinguishes private-file failures from
`github-auth-failed`; remote permission failures may appear at scale-set lookup.
No PAT option is added to the VM JSON configuration or hosted service.

For GHES 3.16, the target operator verified that `app_client_id` must contain the
numeric App ID as a string, rather than the OAuth Client ID. The pinned client
accepts an App ID in this field; consult your GHES version's requirements.
