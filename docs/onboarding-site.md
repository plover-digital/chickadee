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

Keep request metadata private. `scripts/admit-installation.py --config CONFIG
--request REQUEST --output CANDIDATE --trusted-workflows` rechecks the GitHub
installation, repository identity, permissions and selected-repository access.
For personal repositories the request user must own the account. Organization
groups restrict selected repositories and main-branch workflows. New scopes
start with no warm guests and share the existing host limits. Review the beta
trust model before admitting workflows; the website cannot grant admission.

Validate with `chickadee -config CANDIDATE -check`. On a controller with drain
support, `systemctl kill --kill-whom=main --signal=SIGUSR1 chickadee` stops new
assignments, retires credential-free guests and lets active jobs finish. Wait
until the service exits successfully, install the reviewed configuration, then
start it. SIGTERM remains an immediate shutdown. Back up the previous config
and binary for rollback. Never send SIGUSR1 to an older controller.
