# Optional GitHub App onboarding site

The Go website in `cmd/chickadee-web` is optional. The controller remains usable
without it, any hosted service, or an external database. Public code includes
the website; operator infrastructure, customer metadata and credentials remain
outside this repository.

## App settings

Create an operator-owned GitHub App or use the existing one:

- Public installation if other GitHub accounts should install it.
- Repository **Administration: read/write** for personal/repository-scoped
  runner management; organization **Self-hosted runners: read/write** for org
  pools. Metadata read is provided by GitHub. No contents, workflows or email
  permission is needed for this onboarding flow.
- User authorization callback: `https://YOUR-DOMAIN/auth/github/callback`.
- Setup URL: `https://YOUR-DOMAIN/setup`; leave OAuth-during-installation and
  wildcard callbacks disabled. The site starts its own browser-bound PKCE flow.
- Keep user access token expiration enabled. The site does not retain refresh
  tokens, and browser sessions expire after one hour or a server restart.

GitHub requires the App owner to edit registration settings and generate an
OAuth client secret in the browser. Store that secret in a private file on the
web host, never in Git or a URL. It is distinct from the runner controller's
App private key. The website does not need that private key.

Sources: [GitHub App user access tokens](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app),
[App registration settings](https://docs.github.com/en/apps/maintaining-github-apps/modifying-a-github-app-registration).

## Run

Build with `make build test`. Serve behind an HTTPS reverse proxy such as Caddy.
The binary listens only on `127.0.0.1:8080`. Configure:

```sh
CHICKADEE_PUBLIC_URL=https://YOUR-DOMAIN
CHICKADEE_APP_SLUG=YOUR-APP-SLUG
CHICKADEE_APP_ID=123456
CHICKADEE_APP_CLIENT_ID=YOUR-CLIENT-ID
CHICKADEE_OAUTH_SECRET_FILE=/etc/chickadee-web/oauth-client-secret
CHICKADEE_WEB_STATE=/var/lib/chickadee-web
```

The secret must be a regular mode-0600 file readable by the web service account.
The state directory must be dedicated and private (0700). Enrollment metadata
is written atomically to a mode-0600 local JSON file. Back it up privately;
do not include tokens or customer metadata in source commits.

For local preview, `CHICKADEE_PUBLIC_URL=http://127.0.0.1:8080` is allowed.
Public deployments require HTTPS. Without the OAuth secret, the landing page
and installation button work but sign-in is explicitly unavailable.

## Beta behavior and limits

The flow is sign in, install the App on selected repositories/account, verify
access through GitHub, choose a repository, and request beta activation. The
site uses the authenticated user's GitHub App token to enumerate accessible
installations/repositories. A callback `installation_id` query is not trusted
as proof of ownership. The enrollment POST rechecks access, requires CSRF and
Origin checks, and stores no OAuth credentials on disk.

**A pending request does not create runners or change controller config.** The
first hosted beta requires operator admission and a configured GitHub scope.
Use `examples/scopes.json` to serve multiple authorized org/repo scopes in one
controller. Each scope has independent scale sets and JIT credentials while
sharing the host CPU, memory, concurrency and TAP budgets. Never launch multiple
independent controllers against shared TAPs. Personal repos need repository-scoped
pools; org pools can use repository/workflow-restricted runner groups.

The site is not a billing system, tenant-isolation guarantee, workflow editor,
or general control plane. It never executes job commands, submits GitHub code
changes or exposes an unauthenticated pool-admin endpoint. It stores at most
500 activation requests and caps session/state maps. More than 100 installations
or repositories in a returned listing requires operator handling rather than
silent truncation. OAuth tokens remain in transient server memory only; logs
must not include request queries, authorization headers or raw upstream bodies.

## Operator activation

The private request uses GitHub numeric IDs, for example:

```json
{
  "installation_id": 123456,
  "user": {"id": 111},
  "queues": ["chickadee", "chickadee-medium-ubuntu-2404"],
  "account": {"id": 111, "type": "User"},
  "repository": {"id": 222, "full_name": "EXAMPLE-USER/EXAMPLE-REPO"}
}
```

Keep request metadata private. `scripts/admit-installation.py --config CONFIG
--request REQUEST --output CANDIDATE --trusted-workflows` rechecks the GitHub
installation, repository identity, permissions and selected-repository access.
For personal repositories the request user must own the account. Organization
groups restrict selected repositories and main-branch workflows. New scopes enable only `chickadee` by default. Pass repeated `--queue LABEL`
options or request metadata `queues` for explicit additional queues. Existing
queue grants are preserved by this admission helper; the managed reconciler
applies the operator's exact approved list. New scopes start with no warm guests
and one concurrent VM, sharing the existing host limits. Review the beta
trust model before admitting workflows; the website cannot grant admission.

Validate with `chickadee -config CANDIDATE -check`. On a controller with drain
support, `systemctl kill --kill-whom=main --signal=SIGUSR1 chickadee` stops new
assignments, retires credential-free guests and lets active jobs finish. Wait
until the service exits successfully, install the reviewed configuration, then
start it. SIGTERM remains an immediate shutdown. Back up the previous config
and binary for rollback. Never send SIGUSR1 to an older controller.

## Customer dashboard

New activation requests include only `chickadee` by default: medium, Ubuntu 26.04.
The optional size/OS queues are opt-in requests and are not usable until the
operator reports them enabled. The dashboard distinguishes requested queues,
enabled queues, applied state, and a pending pause/resume/disconnect request.
Only active, enabled queues appear in its copyable first-job workflow selector.

Customers can review job execution in their repository's GitHub Actions page.
An active queue means GitHub can assign matching jobs; it is not a claim that
an idle VM is available. This prototype does not expose live queue depth,
running-job counts, billing, or an availability SLA. Profile CPU/RAM shown in
the selector describe the configured resource classes, not current host usage.

See [authentication.md](authentication.md) for the GitHub sign-in model and
permission checks. Personal installations require their owner; managing an
organization repository requires GitHub repository administration permission.

The small dashboard usage graph shows up to seven UTC days of **completed,
reserved VM time**, supplied by the operator's private status relay.
Values include runner connection, execution and cleanup and exclude currently running VMs and
credential-free warm-pool VMs. They describe compute usage, not GitHub job
execution time or billable minutes. Exact daily values are available below
the graph in an accessible table; no history produces an empty-state message.
Usage is visible only alongside that signed-in user's activation requests.

See [managed beta](managed-beta.md) for the optional approval/status bridge,
pause/disconnect behavior and usage measurement limits.

Repository setup forms are collapsed by default. Existing requested queues are
preselected; submitting the form replaces the selection, so unchecking an
optional queue requests its removal. The default `chickadee` queue remains
included. Enabled queues show the applied configuration until the operator's
reconciliation and graceful drain complete. Removing one optional queue does
not request removal of the repository or its other queues.

An operator may import an already admitted tester's verified metadata through
`POST /import-enrollment` on the private Unix admin listener. This route is
absent from the public website. Imports deduplicate by user, installation, and
repository numeric IDs; they never overwrite an existing customer's queue or
pause/disconnect request. The operator must verify GitHub identity, selected
repositories, and deployed queues before importing metadata. The site stores
no credentials in these records.

Each dashboard visit also checks current GitHub repository administration
access. If that access cannot be verified, the site retains the user's request
metadata and explains the permission problem, while hiding usage, enabled
workflow examples, and management controls. This does not mutate deployed
operator state; the private reconciler handles actual access removal.

Organization runner queues and usage appear once per GitHub installation,
with currently authorized repositories listed in a collapsed access section.
Repeated per-repository copies of the same organization usage snapshot are
not added together; the newest authorized snapshot is used. Personal-account
repositories retain separate services and queue editors. The beta dashboard
still requires the activation-request creator and current repository-admin
access; an organization row does not grant organization-owner privileges.

Organization activation forms require the exact main-branch workflow file path,
for example `.github/workflows/build.yml`. The site rejects URLs, traversal,
nested workflow directories, and alternate ref suffixes. It builds the displayed
allowlist reference from the GitHub-verified repository and `refs/heads/main`.
The App does not request repository Contents access, so operators must verify
that the file exists before approving it. Requests store `workflow_path`; the
private status relay reports `enabled_workflow_path` after applying access.
Existing records with no path remain readable for legacy operator handling.

Pending requests say **awaiting approval**, not processing. The private operator
may report `approved` only after review; that state describes activation being
processed. `active` remains the applied state. Requested workflow changes do
not replace the previously applied workflow access until approved and reported.

An operator can explicitly approve repository-wide workflow access for a trusted
organization repository, including pull-request jobs. The private relay records
`enabled_workflow_access: "repository"` only after the selected-repository runner
group policy has been applied. This is distinct from the default exact main-branch
workflow restriction (`"workflow"`). New requests do not grant this broader mode;
the dashboard describes the actual approved mode without requiring a main-only
file path for a repository that already has repository-wide approval.
