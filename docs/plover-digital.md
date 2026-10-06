# plover-digital organization pool

The deployment scope is `https://github.com/plover-digital`, scale-set label
`chickadee`, warm pool 1, and maximum 2 VMs initially. The
[organization profile](../examples/plover-digital.json) contains only placeholders
for App IDs and the standard host key path. Keep real host settings and keys out
of Git. The generic example remains usable by any organization or repository.

## Organization-side prerequisites

1. Create/install an organization-owned GitHub App with **Organization permissions:
   Self-hosted runners read/write**. Record its client ID and installation ID, and
   store its private key locally as described in the install guide. No PAT is
   substituted into the guest or controller.
2. Choose a dedicated runner group where the organization plan permits it. Confirm
   the actual group ID before replacing `runner_group_id`; 1 in the example is not
   a verified group inventory. Do not silently change a shared/default group's
   policy, since that can affect other runners.
3. Restrict the group to selected repositories, starting with
   `plover-digital/chickadee`. If a public repository is deliberately admitted,
   explicitly enable public-repository access in that group. GitHub disables it
   by default and recommends avoiding self-hosted public-fork workloads.
4. Where supported, restrict the group to selected workflows and the reviewed ref:
   `plover-digital/chickadee/.github/workflows/chickadee-ci.yml@refs/heads/main`.
   Temporarily also allow
   `plover-digital/chickadee/.github/workflows/chickadee-smoke.yml@refs/heads/main`
   while performing the manual smoke test, then remove that allowance if unused.
   Keep fork pull requests on the default GitHub-hosted CI. A runner label alone
   is not an access-control boundary.

See GitHub's [group access documentation](https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/manage-access)
and [App permissions](https://docs.github.com/en/actions/how-tos/manage-runners/use-actions-runner-controller/authenticate-to-the-api).
Group/workflow policy features depend on the organization plan. If adequate
restrictions are unavailable, start with a trusted private test repository or
retain hosted CI rather than relaxing access for untrusted workloads.

## Host bootstrap and first push

Use the documented Ubuntu deployment or separately review the local distribution
adaptation. Copy the organization profile to an untracked config, supply actual
App/group values, build and boot-check the image, install, and explicitly configure
NAT before starting the controller. No organization runner or group has been
created by merely committing this profile.

After the real-job smoke check, deliberately install
`examples/plover-digital-ci.yml` as `.github/workflows/chickadee-ci.yml`. It targets
trusted pushes to `main` and manual dispatch. The controller creates job VMs on
demand; each job uses a fresh runner name. Publishing the source does not enable
that self-hosted workflow before the pool exists.

The current shared-UID VM launcher is a trusted-workflow prototype. Do not offer
hostile multi-tenant execution or a managed-service security guarantee before
addressing the process/filesystem isolation gaps in the [research audit](research-audit.md).
