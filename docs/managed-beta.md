# Optional managed beta

The public controller remains self-hostable without this website or service.
A hosted operator can use the optional site and reconciler on separate hosts.
No App private key is placed on the web host; only the OAuth client secret.

## User flow

1. Continue with GitHub, then install the App on selected repositories.
2. Choose a repository you administer. Personal installations require the owner.
3. Request activation. Only `chickadee` is included by default; explicitly request
   additional size/OS queues. Once your account is approved, configured host queues
   can activate automatically; wait until the dashboard shows them enabled before
   using their labels.
4. An approved request becomes active after the controller's queues are ready.
   Copy a workflow from the enabled queue list and inspect job logs in GitHub.
5. Pause/resume/disconnect controls submit desired state. The dashboard separately
   shows applied state. With live reload, queue and pause changes apply while
   existing jobs continue; credentialed guests finish and are destroyed.

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
on `examples/service-policy.json`. Approve numeric GitHub user IDs once. Set `auto_queues: true` for approved
customers to permit their explicitly requested queues from the configured host
catalog without per-label approval. Unknown accounts remain pending, unknown
labels cannot become profiles, and approval does not enable unrequested queues.
For tighter operator control, omit `auto_queues` and list allowed labels in
`queues`. The example uses placeholder IDs and contains no secrets.
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
`chickadee-managed.timer`. The timer checks again 15 seconds after a
reconciliation finishes (with two-second timer accuracy); GitHub verification
and provisioning add processing time. Review systemd timeout against your job timeout;
4200 seconds is for the default 3600-second job limit plus controlled startup.
Use a controller supporting `SIGHUP` scope reload and `SIGUSR1` graceful drain.
Set `live_reload: true` only after deploying that controller. Older releases must
keep this policy flag false; sending them SIGHUP may terminate them.

The reconciler verifies current App permissions and repository selection,
prepares and validates config, atomically installs scope changes, and signals
only the controller main process with SIGHUP. Activation waits for a fresh
`reload.json` acknowledgement matching the exact configuration SHA256 after
new scope pollers are installed. Existing job VMs keep running; new scopes have
no dedicated warm VMs. A rejected update restores the previous file. A missing
acknowledgement triggers a rollback reload; if rollback is also unacknowledged,
`admission-uncertain.json` stops further admission until an operator verifies
the runtime and clears the marker. The site shows approved/provisioning before
active, rather than implying that App installation alone enables runners.

Image, resource, host-limit, authentication and global settings require a
controlled restart. Revoked-registration quarantine and offline updates also
use the guarded drain path: stop new assignments, let existing jobs finish,
confirm QEMU exit and disk removal, then install/start and verify fresh status.
Paused scopes
retain ownership metadata; their queues do not poll. Removed/suspended access
stops assignments. When GitHub no longer permits registration cleanup, durable
intents remain in private `revoked-records` for operator recovery. Transient
upstream failures do not erase scopes. Website outages use cached approved
request metadata for access checks; new requests wait for website recovery.

For organizations, new workflow-restricted enrollment must provide an exact
`.github/workflows/name.yml` or `.yaml` path on the main branch. Previously
configured scopes can retain their legacy operator workflow path; a new
organization cannot silently inherit a guessed path. GitHub's selected-workflow
restriction accepts branch/tag/SHA refs and rejects `refs/pull/.../merge`.

An operator can explicitly authorize every workflow, including PRs, in one
selected **private** organization repository using `--repository-only` and
`repository_workflow_access: {"987654321": "repository"}` in the private policy.
The key is the exact numeric repository ID, not an organization-wide grant.
The reconciler preserves this mode; it does not reapply a main-only restriction.
Public repositories and groups containing other selected repositories cannot
use this override. Personal repository scopes already operate at repository
scope. Review the beta workload trust model before authorizing a customer.

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
