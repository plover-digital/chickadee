# GitHub authentication

Chickadee uses the GitHub App web authorization flow for **Continue with GitHub**,
then a separate GitHub App installation to grant access to selected repositories.
Users do not create a Chickadee password or provide a personal access token.

[Blacksmith describes the same GitHub-only sign-in model](https://www.blacksmith.sh/security).
Its publicly documented integration is organization-focused. Chickadee also supports
personal repositories; their runner-management API requires repository Administration
write permission. Organization runner management requires Self-hosted runners write.
We do not request email access or migration permissions for editing repository code.

## Identity and access

The site sends the browser to GitHub's `/login/oauth/authorize` endpoint with a random,
one-time state, a browser-bound nonce cookie, the configured callback URL, and S256
PKCE. The callback exchanges the code server-side using the App's OAuth client secret
and the PKCE verifier, then verifies the identity through `GET /user`.

The resulting user access token stays in memory in a one-hour session. Tokens are not
stored in enrollment JSON, browser storage, or guest VMs. HTTPS cookies use the
`__Host-` prefix, Secure, HttpOnly, and SameSite=Lax. Restarting the web service requires
users to sign in again. Refresh tokens are not retained.

The dashboard reads installations and repositories through GitHub using that user's
App token. Repository administration permission is required to select a repository;
a personal installation also requires the signed-in user to own the installation.
Repository access alone does not authorize service management. Enrollment and management
requests recheck GitHub access and require both a CSRF token and the exact site Origin.
GitHub setup callback parameters are not evidence of identity or installation ownership.

The website does not receive the runner controller's App private key. Installing or
authorizing the App does not immediately consume runner capacity: activation remains
operator-approved during the trusted beta. Host provisioning uses installation credentials,
and disposable guests receive only per-runner JIT configuration.

## App settings for a deployment

Use your own public GitHub App and configure:

- Callback URL: `https://YOUR-DOMAIN/auth/github/callback`.
- Setup URL: `https://YOUR-DOMAIN/setup`.
- Repository Administration read/write for personal repository runners.
- Organization Self-hosted runners read/write for organization runners.
- Leave callback wildcard matching and Device Flow disabled.
- Keep user access token expiration enabled.

Generate an **OAuth client secret** in the GitHub App settings and store it in a file
readable only by the web-service user. Set `CHICKADEE_OAUTH_SECRET_FILE` to that file.
This secret is different from the App's private PEM key and is required even when
using PKCE. Neither belongs in Git, workflow YAML, support tickets, or chat.
See [onboarding-site.md](onboarding-site.md) for the web-service configuration.

## Validation

Tests cover PKCE and callback binding, expired and cross-browser state rejection,
one-time callback replay rejection, safe cookies, upstream error handling without
credential reflection, forged repository enrollment rejection, and GitHub-admin access
checks. A live deployment still needs an owner-authorized browser sign-in to verify
its actual GitHub callback registration and client secret. REST App metadata does not
expose those callback settings.

Official references: [GitHub App user access tokens](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app),
[installation and accessible-repository APIs](https://docs.github.com/en/rest/apps/installations).
