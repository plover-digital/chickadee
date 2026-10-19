# Immutable guest security refresh

Review date: 2026-10-19 UTC. This is a source baseline and release procedure,
not evidence that images have been built or deployed. Follow issue
[#39](https://github.com/plover-digital/chickadee/issues/39) for artifact acceptance.
Host updates do not patch a guest kernel or its embedded runtimes.

## Reviewed inputs and outstanding vendor status

Ubuntu 24.04 and 26.04 use signed archive snapshot `20261019T000000Z`.
The builders upgrade the entire imported root before installing the profile;
installing only selected packages can leave vulnerable inherited packages behind.
The dated cloud roots remain pinned separately. Keep their original provenance:
a newer package snapshot does not mean a newer cloud root was downloaded.

[USN-8917-1](https://ubuntu.com/security/notices/USN-8917-1) lists
`6.8.0-147.147` for Noble's generic kernel. Check the actual installed kernel
package and extracted kernel against the notice; do not use the host's kernel
version. Resolute and Rocky need their own kernel advisories; this Noble notice
is not evidence for those distributions.

Bubblewrap needs special care. Upstream fixes
[CVE-2026-87766](https://github.com/containers/bubblewrap/security/advisories/GHSA-pxhw-h44j-8pfx)
in 0.12.0. Ubuntu's
[USN-8779-1](https://lists.ubuntu.com/archives/ubuntu-security-announce/2026-September/011207.html)
backport was **reverted** by
[USN-8779-2](https://lists.ubuntu.com/archives/ubuntu-security-announce/2026-September/011208.html).
Noble `0.9.0-1ubuntu0.3` and Resolute `0.11.1-1ubuntu0.3` must not be
reported patched: their source changelogs remove the fix, consistent with the
[Ubuntu CVE status](https://ubuntu.com/security/CVE-2026-87766).
Record whether bubblewrap is actually present in each guest, its full vendor
revision and source patch status. A missing guest package does not assess the
host QEMU sandbox. Do not weaken host sandboxing or disable SELinux to pass tests.

The Rocky RPM lock was resolved natively on 2026-10-19 from the pinned Rocky
10.2 cloud base, including inherited package upgrades and developer additions.
All 514 downloaded RPM signatures were verified. A separate fresh copy of that
base successfully replayed the lock with networking disabled and local package
GPG checks enabled: all 514 exact RPM identities and all requested profile
package names were installed. The resulting inventory contains 829 packages;
bubblewrap is absent in this package replay. This is not a complete runner image,
boot, job or deployment acceptance. Kernel 6.12.0-211.66.1.el10_2, glibc
2.39-142.el10_2 and OpenSSL 3.5.8-2.el10_2 still require vendor advisory review.
Resolve the full transaction again when refreshing: include updates inherited
from the cloud root, not only new developer packages.
Use `guest/resolve-rocky-inputs.py` inside that scratch guest with
`guest/images/rocky-102-developer-packages.txt` and a fresh output directory. It
uses native DNF against canonical BaseOS/AppStream/CRB, resolves inherited package
upgrades together with profile additions, downloads and signature-checks RPMs,
and emits proposed `rpm-inputs.json` plus removal evidence. It does not install
the transaction. Preserve the cached repository metadata and review removals.
Record exact URLs, SHA256, RPM NEVRA/source RPM and repository metadata with the
resolved transaction, then replay offline with `localpkg_gpgcheck=1`. Consult
[Rocky errata](https://errata.rockylinux.org/) and vendor source patches;
[Red Hat's CVE record](https://access.redhat.com/security/cve/cve-2026-87766)
is supporting evidence, not proof that a particular Rocky RPM includes a fix.
Do not manufacture a new lock from version guesses or call an image patched
merely because its guest bootstrap or RPM lock was refreshed. Runner component
findings and remaining kernel/vendor fixes tracked in issue #39 still gate
security and deployment acceptance.

## Runner availability and toolchains

[Runner 2.338.0](https://github.com/actions/runner/releases/tag/v2.338.0) was
published on October 6, but its release notes specify a progressive rollout.
On October 19 the authenticated repository runner-download endpoint still
advertised Linux x64 **2.337.0**. Builders retain that currently advertised
version and its verified checksum. Version 2.338.0 is a future candidate, with upstream Linux
x64 SHA256 `af4b794c1bc41d73d40535e3fe092a39f9679cd8d965954c2aca25a05ca41d32`.
Recheck the target repository/organization download instructions before updating
all Linux builder pins together. Never store registration tokens in a lock.

GitHub's [update policy](https://docs.github.com/en/actions/reference/runners/self-hosted-runners)
requires updating within 30 days of availability when automatic updates are
disabled; critical security updates can block jobs sooner. Record observed
availability and the resulting deadline separately from the release publication
date. No deadline is inferred here. Validate Node 24 internal runner operations
and `hashFiles()` when accepting 2.338.0.

Guest Go caches now contain supported **1.26.9 and 1.27.2**, defaulting to 1.27.2,
from exact Actions release assets and published SHA256 metadata, cross-checked
against [Go's supported downloads](https://go.dev/dl/?mode=json).
Unsupported Go 1.24/1.25 caches are removed; jobs requiring those versions must
explicitly install them and own that risk. This is a guest compatibility change.
Other language/SDK archives retain their existing pins and still require artifact
scanning; refreshing Go does not certify the complete developer image.
Guest bootstrap compilation explicitly selects `GOTOOLCHAIN=go1.27.2`.

To generate a proposed Go lock without overwriting the reviewed input:

```sh
python3 scripts/refresh-image-go-lock.py guest/images/github-ubuntu-2404.json /tmp/proposed-go.json \
  --release 1.26.9-37875104533 --release 1.27.2-37875086547
# Repeat for github-ubuntu-2604.json, review the diff, then verify actual downloads.
python3 scripts/fetch-image-tools.py /tmp/proposed-go.json /tmp/image-tool-downloads
```

## Build, acceptance and controlled rollout

Use a dedicated checkout and fresh output directories, never a production image
or runtime directory. Build one OS at a time with enforced CPU/memory limits,
low CPU/I/O priority, no swap growth, and a monitored workspace budget. Retain
builder container digest, package inventory, source commit, Go build information,
input locks, `package-provenance.json`, `guest-build.txt` and output `SHA256SUMS`.
A successful download or source test is not a successful image build. Package inventories are provenance, not a clean scan.

For every candidate bundle:

1. Verify signed source checksums, package signatures, archive hashes and the
   resulting bundle's checksums. Compare complete installed package inventory
   against vendor advisories, including kernel and bubblewrap patch status.
2. Scan the extracted guest bootstrap and embedded tool/runtime artifacts;
   retain scanner version, database timestamp, findings and any reviewed
   exceptions. A host binary scan does not cover guest toolcache binaries.
3. Use fresh private state and credential-free cold boots to verify READY,
   repeated warm readiness, resource accounting and cleanup after QEMU exit.
4. Run a real one-job workflow for each supported profile, including setup-action
   cache use, default Go version, Node and guest-local containers. Verify runner
   destruction and replacement. Retain prior immutable bundles for rollback.
5. Coordinate one exclusive rollout: stop admission, drain credentialed jobs and
   retire warm guests, confirm QEMU exits, then switch configuration to the new
   immutable revision and restart through the controlled drain procedure.
   Image/global changes are not queue-only SIGHUP updates. Preserve one GitHub
   listener per queue. Verify authenticated inventory and a real job afterward.
6. On failure, drain the candidate in the same way and restore the retained
   bundle/configuration. Never overwrite either revision or remove a backing
   file while any overlay/QEMU still references it.

No worker API or configuration schema change is intended. Bundle manifests keep
the current fields and disk/machine profiles; standalone operation requires no
hosted account. Source review, artifact acceptance and deployed acceptance are
separate gates. Keep host inventory and exact deployment paths in private ops.

### Developer fresh-runner acceptance

After the existing medium Ubuntu26 queue has the reviewed developer bundle,
run [developer-replacement-smoke.yml](../.github/workflows/developer-replacement-smoke.yml)
on `main`. Its two sequential jobs require different runner identities and check
Ubuntu26, four CPUs, 8 GiB allocation, developer toolcache, defaults and guest
isolation. Docker is required for this profile; its socket must be on the guest
root or runtime filesystem and the probe uses that explicit Unix socket.
Correlate the logged kernel and bootstrap hash with private worker records and
the immutable bundle digest. Different runner names alone do not prove QEMU
exit or disk deletion: verify those worker lifecycle records separately.

This complements `ubuntu-2604-smoke.yml` runtime coverage. It does not authorize
queue remapping or deployment, and it cannot validate the minimal profile.
The generic `chickadee-smoke.yml` rejects Docker sockets and must not be treated
as developer-profile acceptance.

## Rocky profile acceptance

After staging the accepted Rocky bundle through a controlled worker drain, use
`rocky-replacement-smoke.yml` on `main` with `size=small` and then `size=medium`,
and `expected_kernel` set to the candidate's exact kernel release. Each dispatch
runs two sequential jobs on the existing native Rocky label, verifies the actual
CPU/memory shape, enforcing SELinux, guest-reported kernel, native tools and
Go/Node caches, and requires different runner identities. It does not require a
Docker daemon or claim Ubuntu developer parity. The toolcache default is Go
1.27.2; callers needing older unsupported releases must explicitly install them.

Correlate both runner identities with worker records and the exact immutable
bundle; routing to another host's old image is not candidate acceptance. Verify
actual QEMU exit, disk/cgroup cleanup and replacement in worker evidence. A
standalone 1-vCPU/2-GiB boot check does not establish the live small (2/4096) or
medium (4/8192) worker profile, networking or real-job behavior. Dispatch only in
the coordinated rollout slot, preserving the single existing GitHub listener.
