# Runner platform research: selection, images and caches

Research checkpoint: 2026-10-06. Primary product documentation was discovered
and retrieved with Exa, with official GitHub/QEMU documentation used for
platform constraints. This is a design comparison, not an independent speed,
security or price benchmark. Provider claims of faster builds, instantaneous
provisioning and unlimited concurrency are not measurements of Chickadee.

The user requested several selectable resource sizes and other Linux images.
The recurring model is **a simple workflow selector backed by a named runner
profile**, where compute, image and cache policy can evolve separately.

## Relevant systems

| System | Documented approach | Useful lesson for Chickadee |
| --- | --- | --- |
| [Blacksmith](https://docs.blacksmith.sh/blacksmith-runners/overview) | Labels identify OS/version, CPU family/architecture and size. Standard environments follow GitHub runner images, with accelerated caches. | Make common choices a one-line `runs-on` change. Keep hardware selection distinct from image contents. |
| [Depot](https://depot.dev/docs/github-actions/overview) | Named OS/size labels, ephemeral standby instances, repository-scoped cache, own-AWS-account options and Business-plan custom AMIs. | Separate runner lifecycle from cache lifetime. Its custom-image and own-cloud-account features do not imply bare-metal installation. |
| [Namespace](https://namespace.so/docs/solutions/github-actions) | Named profiles select platform and resource shape. Profile-level customization and cache controls sit behind that name. | A profile is a stable interface; the operator can manage its implementation centrally. |
| [RunsOn](https://runs-on.com/docs/runners/custom/) | Reusable runner shapes and separately named images. Flex uses repository config; Fleet uses an operator-owned catalog. | Separate image definitions from resource shapes and decide who can approve each selection. |
| [Actuated](https://docs.actuated.com/) | Managed scheduling of isolated, disposable microVM builds on user-owned KVM-capable servers. | Bare-metal density and VM isolation are relevant; a cloud account is not required for the concept. |
| [Fireactions](https://fireactions.io/latest/reference/configuration/) | Pool definitions contain runner labels, root image, kernel and vCPU/memory settings. | A small, explicit multi-pool configuration is viable without building a general cloud platform. |
| [Ubicloud](https://www.ubicloud.com/docs/github-actions-integration/runner-types) | Labels combine size, Ubuntu version and architecture; fresh job VMs and accelerated cache integrations. | Keep resource selections predictable, and distinguish a runner product from its larger cloud infrastructure. |
| [WarpBuild](https://www.warpbuild.com/guides/custom-runner-images-github-actions) | BYOC custom VM images are registered, attached to named runners and selected through workflow labels. | Treat an image as a versioned artifact; moving a tool install into an image is a user's image decision. |
| [BuildJet](https://buildjet.com/for-github-actions/docs/getting-started/run-your-first-workflow) | Runner tags choose compute; a separate cache-action integration is available. | Runner selection and cache adoption need not be one feature. |

[Fireactions upstream](https://github.com/hostinger/fireactions) is an open-source
implementation worth inspecting, and [Ubicloud upstream](https://github.com/ubicloud/ubicloud)
shows a much broader open cloud design. This comparison does not import code,
licenses, managed-service guarantees or infrastructure from either project.

## What selectors actually buy

Some systems use explicit compound labels; others expose a short profile name
and keep details in configuration. RunsOn also supports per-job constraints and
cloud-instance selection. That freedom is supported by its cloud provisioning
architecture, not by a magical GitHub YAML feature.

For Chickadee's next implementation, use **operator-approved named profiles**.
A single label maps to one known image/resource/policy combination, for example
`chickadee-medium-ubuntu-2404`. Do not start with a parser that accepts arbitrary
image URLs, CPU allocations, shell hooks or network changes from workflow labels.

The pinned [official scaleset client](https://github.com/actions/scaleset/blob/v0.4.0/README.md)
explains that a scale-set name is a workflow label, GitHub chooses a matching
runner, and assigned statistics drive demand. One scale set per profile gives
unambiguous resource/image routing. A single VM remains at most one job; it is
not bound to the particular workflow-job event that prompted provisioning.

The proposed configuration is in [selection-design.md](selection-design.md).
It is **not implemented** by this research commit; the current binary still
supports one scale set and image/resource profile.

## Images, distributions and capabilities

A useful image catalog records OS/version, architecture, artifact identity,
runner/bootstrap versions and tested capabilities. Root filesystem, kernel and
initrd must form a compatible boot bundle. An OCI base image or distro label is
not by itself a bootable microVM image.

[QEMU microvm](https://www.qemu.org/docs/master/system/i386/microvm.html)
requires host-supplied kernel/initrd boot and has its own device limitations.
Other prepared Linux distributions are a plausible extension of the bootstrap
contract; each requires actual image-build and lifecycle validation. ARM is
separate host/VM-backend work for this currently Linux-amd64 implementation.
Do not present Windows or macOS as additional rootfs choices on the existing
x86 microvm backend just because providers offer them.

Provider OS families commonly track GitHub's supported runner environments,
not every Linux distribution. Custom tools can live in a versioned operator
image without becoming hard-coded controller defaults. GitHub itself now
[documents custom images for larger runners](https://docs.github.com/en/actions/how-tos/manage-runners/larger-runners/use-custom-images),
including version selection and platform matching. Those hosted facilities
are separate from Chickadee's local qcow2/kernel/initrd artifacts.

Capability selection is also possible: a Docker-capable image, container-job
support or nested virtualization could be explicit, tested capabilities. The
current reference guest does not provide Docker/sudo/service containers, and
no such capability is enabled by this investigation. A future Docker-capable
guest would run its own daemon inside the VM, never use the host Docker socket.

## Caches are a separate selection

[Namespace cache documentation](https://namespace.so/docs/solutions/github-actions/caching)
describes separate cache types, profile/repository isolation, generic tool-cache
download persistence and restrictions on which branches may commit updates.
That is particularly relevant: users can choose tool versions in setup actions
without Chickadee preinstalling every language/version. But persistent tool
binaries are still executable inputs whose writers must be trusted.

[Depot](https://depot.dev/docs/github-actions/overview) documents repository
cache scoping. [Ubicloud](https://www.ubicloud.com/docs/github-actions-integration/ubicloud-cache)
documents transparent cache integration alongside a clean VM per job. These
show that disposable compute and persistent cache data can coexist. Product
pages do not alone establish how to safely implement them on our host.

A future Chickadee cache policy needs a size/retention budget, authorization,
compatibility rules and permitted writers. Merely placing a repository name in
a user-provided key is not an access-control boundary. Trusted branches may
publish cache updates while untrusted PRs use isolated or non-publishing copies.
Caches must not include runner registration/JIT files or management secrets.

A critical routing constraint: **provisioning demand does not bind a VM to a
repository/job**. An org-level runner may get another matching job. Private
cache access must use the actual assigned job's verified context, or a queue
whose runner group is restricted to the intended repository/trust boundary.
Do not mount a private cache based only on an earlier demand event.

Shared writable host directories/sockets remain excluded. Candidate designs
include bounded artifact restore/save or per-job writable copies of approved
cache seeds. Direct shared writable mounts, disk-conflict resolution, cache
poisoning and quota enforcement require separate design and tests. Whole-machine
snapshot offerings from providers are not adopted: no memory snapshots, VM
suspension or credentialed-VM/root-disk reuse is planned for this prototype.

## Performance interpretation

Provider pages sometimes advertise VM boot in a few seconds. That is not the
same metric as workflow creation to a connected runner or first command.
Chickadee's [measured warm-startup breakdown](benchmarks/2026-10-06-startup-overlap.md)
includes GitHub demand delivery, JIT generation, local listener launch,
authenticated session establishment and assignment. Do not compare a vendor's
boot-only claim with our end-to-end timing and infer a hypervisor speed gap.

Performance investigations should retain separate milestones for boot-to-READY,
demand receipt, reservation, JIT/ACK, connected/listening, job start and first
command. Claims of a connected/listening milestone must have a reliable signal;
local RUNNING currently proves only listener process launch.

## Recommended implementation order

1. Add image bundles, reusable CPU/RAM classes and named profiles/scale sets.
   Keep one controller and enforce global weighted host budgets as well as
   per-profile maxima; test routing, fairness and mixed-resource exhaustion.
2. Start with versioned Ubuntu 24.04 and Rocky Linux 9.8 selectors. Keep the
   verified Ubuntu builder; add Rocky only after boot/job/restart validation.
   Validate manifests, compatibility and artifact digests before exposing a queue.
3. Add tested guest capabilities, if needed, without exposing host services.
4. Design cache authorization and writer isolation separately, then implement a
   bounded optional cache mechanism. Keep ordinary jobs cache-free by default.

For a later managed offering, use the same open controller/profile/image model.
Tenant authorization, host App-key versus emulator identities, per-VM host
isolation, usage accounting and operational guarantees remain separate work.
Nothing in this comparison removes the prototype's shared-UID trust limitation.
