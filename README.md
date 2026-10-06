# chickadee

A small, self-hosted GitHub Actions runner pool for one Linux host. Each job gets an
independently booted QEMU VM with KVM and a fresh
qcow2 overlay. Warm guests have no GitHub credentials and no runner registration.

**Working one-host prototype.** The real vertical slice has passed: preboot to
READY, fresh serial JIT, two real GitHub jobs on distinct runners, confirmed
process exit and disk deletion, and credential-free warm replacement. A trusted
main-branch push also built both binaries and passed the full test suite inside
an ephemeral VM. Warm-VM failure and idle controller-crash recovery were checked.
Clean Ubuntu install/reboot/uninstall and fuller adversarial network/restart
acceptance still remain. See [validation status](docs/validation.md).

Build with Go 1.26.3 on Linux amd64:

```sh
make build test
make image
```

Install on a dedicated Ubuntu 24.04 amd64 host by following
[installation](docs/install.md), including GitHub App setup. Installation only
copies files; applying networking and starting the service are explicit steps.
No repository publication or network changes are performed by building or testing.

The host uses the official [actions/scaleset Go client](https://github.com/actions/scaleset/tree/v0.4.0)
for GitHub App authentication, demand polling, acquisition, and JIT configuration.
The controller uses no Kubernetes, database, webhook receiver, Docker daemon,
snapshot, or VM suspension. Storage, serial sockets, logs, and the ownership
journal are local. One controller owns all profiles, with a scale set per workflow
label and shared host resource limits. Legacy single-profile config still works.

```mermaid
flowchart LR
  GH[GitHub scale sets] -->|demand statistics| C[Go controller]
  C -->|boot| W[Warm VM: no credentials]
  W -->|READY over serial| C
  C -->|fresh one-runner JIT over serial| R[Reserved VM]
  R -->|outbound NAT| GH
  R -->|one job, diagnostics, DONE| C
  C --> D[Kill QEMU, wait, delete overlay]
  D --> W
```

GitHub chooses a matching job for an idle runner. VM creation does not bind a
particular workflow job. A JIT runner handles at most one job; a workflow with
several jobs uses several guests. Once credential generation begins, even an
ambiguous failure destroys that VM. Guest messages never authorize reuse.

See [architecture and trust model](docs/architecture.md),
[troubleshooting](docs/troubleshooting.md), and the manual
[two-job smoke workflow](examples/smoke.yml). A manual smoke workflow is installed for the plover-digital pilot; it runs only
on deliberate dispatch. The portable example can be copied into another target
repository after its pool is installed.

Development is open and commercially usable under [MIT](LICENSE). See
[contributing](CONTRIBUTING.md), the [public roadmap](docs/roadmap.md), and
[security status](SECURITY.md). Self-hosting has no dependency on a hosted service;
a future managed offering can operate the same public components.

For the smallest automatic loop, install once and opt into the
[push-to-build-and-test workflow](docs/push-to-test.md). Each trusted push then
requests a fresh job VM through GitHub's scale-set queue.

The [Exa primary-source audit](docs/research-audit.md) tracks design guidance and
remaining security gaps. The [plover-digital organization profile](docs/plover-digital.md)
documents our pilot without including credentials or private host details.

[Runner profiles](docs/profiles.md) select immutable OS images and CPU/RAM
classes. The example default `runs-on: chickadee` uses medium (4 vCPU / 8 GiB)
and Ubuntu 26.04; explicit labels such as `chickadee-small-ubuntu-2404` select
other approved combinations. Ubuntu 24.04 uses microvm; Ubuntu 26.04 and Rocky 10.2 use q35
with stock kernels. Rocky retains enforcing SELinux. New image support requires actual
boot/job validation; see the validation log. The [Ubuntu 26.04 builder](docs/ubuntu-2604.md) provides the developer default;
Rocky 9.8 remains tracked work. The [provider comparison](docs/runner-platform-research.md)
and [design rationale](docs/selection-design.md) explain the model.

An optional [GitHub App onboarding site](docs/onboarding-site.md) provides
sign-in, selected-repository installation and approved-beta activation requests.
It is separate from the runner controller; self-hosting does not require it.
