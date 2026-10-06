# Optional managed beta

The public controller remains self-hostable without this website or service.
A hosted operator can use the optional site and reconciler on separate hosts.
No App private key is placed on the web host; only the OAuth client secret.

## User flow

1. Continue with GitHub, then install the App on selected repositories.
2. Choose a repository you administer. Personal installations require the owner.
3. Request activation. Only `chickadee` is included by default; explicitly request
   additional size/OS queues and wait for their approval before using their labels.
4. An approved request becomes active after the controller's queues are ready.
   Copy a workflow from the enabled queue list and inspect job logs in GitHub.
5. Pause/resume/disconnect controls submit desired state. The dashboard separately
   shows applied state. Existing jobs finish before a controlled update applies.

GitHub OAuth and installation are separate authorizations, as in Blacksmith's
GitHub-only sign-in/App-install model. See [authentication](authentication.md).
The beta does not implement Blacksmith's entire team-management feature set.

## Operator setup

Enable `CHICKADEE_WEB_ADMIN=1` on the site. This serves operator metadata updates
on a mode-0600 Unix socket at `/var/lib/chickadee-web/admin.sock`. The public
HTTP handler has no operator endpoints. Never proxy the Unix socket publicly.
Use pinned SSH host keys and a dedicated private SSH key to reach that host.

Copy `scripts/reconcile-site.py`, `scripts/admit-installation.py` and
`scripts/setup-app.py` to `/usr/local/lib/chickadee/` on the runner host.
Keep a root-owned mode-0600 policy at `/etc/chickadee/managed-policy.json`, based
on `examples/service-policy.json`. Approve numeric GitHub user IDs and exact
additional queues. Approval alone does not enable an unrequested queue.
New scopes default to one concurrent VM across all their enabled queues.
Credential-free warm VMs are shared across customers and aliases with exactly
the same immutable image, machine, CPU, memory and disk. The operator profile
provides the host warm-capacity hint; customer profiles have warm_pool=0. A
matching READY guest is bound to the requesting GitHub scope only at reservation.
Credentials are generated then, and a spent guest is always destroyed.
Global host limits still apply. The operator's primary scope is preserved.

Put the private site SSH key in `/etc/chickadee/site-ssh.pem` (0600) and pinned
host keys in `/etc/chickadee/site-known-hosts`. Configure root-owned 0600
`/etc/chickadee/managed.env` with `CHICKADEE_SITE_HOST=YOUR-SITE-HOST`.
Install the optional `deploy/chickadee-managed.service` and timer, then enable
`chickadee-managed.timer`. Review systemd timeout against your job timeout;
4200 seconds is for the default 3600-second job limit plus controlled startup.
Only use a drain-aware controller; older releases cannot receive SIGUSR1.

The reconciler verifies current App permissions and repository selection,
prepares and validates config, drains active work, atomically installs it and
waits for fresh controller status before acknowledging activation. Paused scopes
retain ownership metadata; their queues do not poll. Removed/suspended access
stops assignments. When GitHub no longer permits registration cleanup, durable
intents remain in private `revoked-records` for operator recovery. Transient
upstream failures do not erase scopes. Website outages use cached approved
request metadata for access checks; new requests wait for website recovery.

This initial bridge admits one enrollment per GitHub scope. Organization queues
are shared by the group's selected repository/workflow policy, not private to
one workflow job. Conflicting scope ownership requires operator review. It
uses no database or public admin API and never edits customer workflow files.

## Usage

A private local ledger records completed credentialed VM reservation intervals;
unregistered warm VMs are excluded. The dashboard displays the last seven UTC
days of reserved VM minutes, including GitHub connection, execution and cleanup.
These are not job-execution minutes or invoices. In-flight VMs and jobs before
instrumentation are absent; abrupt controller/host failure can leave intervals
unrecorded. Thirty days of bounded private records are retained. Organization
usage describes its shared runner scope; it cannot be attributed to individual
repositories/jobs from provisioning demand. Do not bill from these counters.

Billing, per-VM host isolation, App-key/emulator identity separation, long-term
accounting and unattended hostile multi-tenant admission remain separate work.
