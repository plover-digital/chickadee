# Chickadee: host software

Read [docs/repositories.md](docs/repositories.md) before changing repository
boundaries. Chickadee is the public self-hostable VM/runner engine. Keep it usable
without a Chickadee Roost account, website, vendor API or external database.

## Ownership

- This repository owns QEMU/KVM, guest bootstrap and serial protocol, immutable
  image builders, local VM scheduling, resource limits, cleanup and the standalone
  GitHub adapter. A future versioned host API also belongs here.
- Chickadee Roost owns hosted accounts, OAuth/dashboard, customer admission,
  service policy, aggregate usage and fleet routing. Do not add billing or
  customer-account dependencies to the VM engine.
- Private deployment inventories, customer identities/policy and runbooks belong
  in chickadee-ops and internal Notion. Credentials belong outside every repo.

Website and admission code have moved to the independent public Roost module.
Check current files and the repository map; do not assume proposed fleet commands
or remote APIs exist. The current host adapter is still single-host.

## Work and verification

1. Read the relevant lifecycle and trust documentation and inspect the current
   worktree. Preserve changes belonging to the user or another agent.
2. Implement in the owning repository. Cross-repository changes must state the
   protocol/config version, compatibility requirements and deployment order.
3. Run `make build test` for controller/lifecycle changes. Go race tests and the
   Python image/installer checks are part of that command. Use targeted checks
   for isolated changes; documentation needs link/consistency checks.
4. Keep generated images, overlays, keys, runtime state and private fixture data
   out of commits. Publish only explicitly reviewed source files.
5. Record public implementation work in GitHub issues; keep customer/host details
   in private tracking. Update the repository map when ownership changes.

## Lifecycle invariants

- Warm VMs have no GitHub credentials. Never reuse a VM after credential intent.
- GitHub matches jobs; reservation is not a VM-to-specific-job binding.
- Enforce CPU, memory, disk, VM and customer limits. Retiring/uncertain VMs count.
- Confirm QEMU has exited before deleting its disk. Preserve bounded private
  diagnostics where possible; guest messages and logs are untrusted.
- Queue-only SIGHUP updates preserve jobs and compatible warm VMs. Image/global
  changes and binary upgrades require controlled draining, not an abrupt restart.
- Do not duplicate GitHub listeners by copying deployment config to another host.
  Multi-host support is proposed and not implemented in the current release.

## Deployment boundaries

Use authorization already present in the session. Do not publish a new repository
or change host networking without the user's authorization. Prepare and validate
the concrete result before asking for any approval still needed. Do not copy
private operations information into public docs, issues, logs or examples.
