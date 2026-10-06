# Open development roadmap

The same public implementation should be usable by someone operating their own
host and by an operator charging for managed hosting. The MIT license permits
both. A paid service can provide operations, capacity, support and reliability;
self-host deployment must remain independent of a vendor API, account, database
or control plane. Keep implementation and design discussion public, while keeping
secrets, customer information and coordinated security reports private.

## Work tracking

Track public implementation, bugs, designs and release acceptance in
[Chickadee Issues](https://github.com/plover-digital/chickadee/issues) for host work
and [Roost Issues](https://github.com/plover-digital/chickadee-roost/issues) for service work. Each issue
should state the expected behavior and acceptance evidence. Keep deployment
inventories, customer details and commercial planning in internal tracking;
never place credentials in either tracker.

## First deployable release

- [x] Go pool controller, direct QEMU management and bounded serial bootstrap.
- [x] Irreversible credential state and durable registration intent.
- [x] Image-build recipe with signed/pinned inputs and versioned kernel/initrd.
- [x] Reference Ubuntu installation, explicit NAT/TAP setup, systemd and uninstall.
- [x] Example configuration, manual smoke workflow and lifecycle tests.
- [x] Public architecture, limitations, troubleshooting and verification status.
- [x] Online build against the pinned scale-set module and retained `go.sum`.
- [x] Build the reference image and boot two real KVM microvms to READY, confirming exit and overlay deletion.
- [x] Inject fresh JIT, execute two real jobs, verify destruction and pool replacement.
- [x] Automatically build and run the full test suite on a fresh runner after a trusted main push.
- [x] Verify recovery after an idle warm-VM failure and an idle controller crash.
- [ ] Verify firewall isolation, concurrency limits, timeout/failure and restart.
- [ ] Test installation, reboot restoration and uninstall on clean Ubuntu 24.04.
- [x] Publish the source at `plover-digital/chickadee` with build/deployment instructions.
- [ ] Complete verified real-host release acceptance and tag the first deployable release.

Mark acceptance evidence in [validation](validation.md). Recipe/source completion
is distinct from deployed behavior. Share scrubbed results and reproducible steps;
keep private host inventories and customer data out of the source repository.

## Possible managed service, after the prototype

The current trust model fits one trusted operator's dedicated host. A first paid
pilot could operate dedicated customer hosts using this public code. It must not
silently turn this prototype into a shared hostile multi-tenant service.

Before charging for a reliability/isolation promise, publish the proposed service
boundaries, threat model, data retention, capacity limits and operational testing.
Billing, production tenant isolation, separate App-key/QEMU identities, per-VM filesystem/process isolation and
resource cgroups, stronger VM sandboxing, fleet scheduling, upgrades,
incident response and service-level guarantees are separate future work. They do
not belong in the first one-host vertical slice. Shared customer caches, snapshot
cloning, multi-host scheduling and HA remain
deferred. The separately deployable public [Chickadee Roost](https://github.com/plover-digital/chickadee-roost)
provides an optional GitHub App onboarding dashboard, usage graph and approved
queue reconciliation; they do not provide billing or a hardened
shared-tenant service. See the [multi-host proposal](multi-host.md). The user has
now requested multiple resource/image selections; see the
[profile design draft](selection-design.md) and [platform comparison](runner-platform-research.md).
The shared profile scheduler and image catalog are implemented; new image
release support requires real boot/job validation. See [profiles](profiles.md).

Next image/profile targets: Ubuntu 24.04/26.04 and Rocky Linux 9.8/10.2, with labels
`chickadee-{size}-{os}-{version}` (for example,
`chickadee-small-ubuntu-2404`). Ubuntu 26.04 and Rocky 10.2 builders use stock-kernel q35;
Rocky 9.8 remains unimplemented, while Ubuntu 24.04 retains microvm; see [selection design](selection-design.md).
