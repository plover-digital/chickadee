# Minimum push-to-build-and-test loop

A commit should need only `git push`. The one-time prerequisites are a deployed
host controller, a verified runner image, isolated outbound networking and a
GitHub App installation. No additional webhook receiver or deployment service is
needed. GitHub's scale-set queue supplies demand.

1. Complete the [host installation](install.md) and the real smoke acceptance.
2. Copy `examples/self-hosted-ci.yml` into the trusted repository as
   `.github/workflows/chickadee-ci.yml`. Use the installed scale-set name in
   `runs-on`. Commit it deliberately.
3. Push a trusted change. GitHub queues `build-test`; the existing controller
   acquires demand and reserves a prebooted guest. The guest gets fresh JIT,
   checks out that commit, builds and tests, exits, and is destroyed. The host
   replenishes the warm pool. Subsequent pushes repeat this automatically.

The example requests contents read-only, avoids persisting checkout credentials,
uses pinned actions and Go, and disables shared caches. The guest's ordinary
`GITHUB_TOKEN` belongs to the workflow; it is distinct from the host's App key.
The runner image includes Git and `build-essential`: make runs the build and GCC
supports Go's race detector. Go is installed inside the disposable guest by setup-go.

The controller must already run before the first self-hosted workflow can run:
a queued job cannot provision the controller that it needs to execute. This is a
small one-time bootstrap, not a circular self-deployment. The public repository's
default CI uses GitHub-hosted Ubuntu so fresh clones, forks and contributor pull
requests do not require an installed chickadee host. Self-hosted CI is an explicit
opt-in after deployment.

This loop provisions **job VMs** automatically. It does not update the host
controller or rebuild/install the base image on every application commit. Those
are privileged deployment operations. Keep host management credentials and
filesystem access out of build guests. For the first prototype, review and install
controller/image updates manually using the same public source and build scripts.
A later host-side release updater could install an approved, verified artifact;
it should not run arbitrary workflow-produced scripts with host privileges.

A managed service could hide the one-time host bootstrap from customers while
exposing the same scale-set label. Billing and customer-host management are future
work; the job execution path and self-hosted deployment remain public and usable
without a commercial service.
