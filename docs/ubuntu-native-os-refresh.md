# Ubuntu native OS security refresh

Review date: 2026-10-25 UTC. This is source and input preparation, not a built,
booted or deployed image. A trust/runtime refresh does not update the guest's
native libraries or kernel. This path updates a new immutable copy of a reviewed
Ubuntu developer bundle; it never edits a live image or the sealed parent.

The full Ubuntu builders now use snapshot `20261025T000000Z`. Imported root
release/hash/signature provenance stays pinned independently. Incremental bundles
retain their original `snapshot` and `build-inputs.json`; the supplemental native
transaction records the actual later snapshot and new kernel. It does not relabel
the old imported root as a newly downloaded image.

## Prepare a finite authenticated transaction

The policy pins the exact signed Ubuntu base, security and updates releases for
24.04 and 26.04. Check all four components: main, restricted, universe and
multiverse. Verify each InRelease with the official archive key and the pinned
fingerprint, then each compressed index against that signed release. Use the
complete sealed parent `package-provenance.json`, not a shortlist of interesting
packages. Preparation selects every newer installed native package, preserves
higher installed versions, and closes required/pre-required dependencies from
those authenticated indexes. Exact-version index records prove installed virtual
providers, so an installed `initramfs-tools` does not spuriously require `dracut`.
Unresolved/unsupported dependencies fail preparation.

```sh
python3 scripts/prepare-ubuntu-os-inputs.py \
  --family 26.04 --metadata /path/to/verified-index-cache \
  --keyring /path/to/ubuntu-archive-keyring.gpg \
  --inventory /path/to/accepted/package-provenance.json \
  --output /path/to/native-plan.json
nice -n 19 python3 scripts/prefetch-ubuntu-os-inputs.py \
  /path/to/native-plan.json /path/to/fresh-deb-cache
```

Establish and review package counts/bytes before prefetch. Downloads are serial,
limited to 4 MiB/s, ten minutes and the exact signed size/hash for each deb. The
source rejects transactions above 512 packages/1 GiB. This is an input ceiling,
not permission to consume a build resource window. Existing mismatching files
fail verification; they are not overwritten or substituted. Keep signed metadata,
key/signature proof, the complete plan and every downloaded hash privately with
the build receipt. Parent package inventories and deployment paths are not public
source fixtures.

Static dependency closure is not native APT solver acceptance. Inside the later
offline copy, APT must simulate exactly the locked package/version set. Ambient
repositories/lists, recommendations and downloads are disabled. Removals, extra
installs, downgrades, a different parent inventory or an unconfigured/held package
state stop the transaction before installation. Before simulation or service-policy
changes, query every dpkg state and require a clean `dpkg --audit`. Only `ii `
installed rows and explicitly non-installed `rc ` (residual configuration), `un `,
`rn ` and `pn ` rows are accepted; half-installed/unpacked/half-configured,
trigger-pending/awaiting, held, error and malformed states fail closed. Retain
query/audit stdout, stderr and exit status, including on failure. APT's `Inst` and
`Conf` sets must both equal the exact planned package/version set; configuration
of an unpacked package outside the plan cannot be hidden by installed inventory.
Actual APT/dpkg execution is pending the coordinator's separate resource GO.

## Kernel and protected image state

```sh
scripts/refresh-ubuntu-os-bundle.sh \
  /path/to/accepted-ubuntu-developer /path/to/fresh-output \
  EXACT_PARENT_SHA256SUMS_DIGEST /path/to/native-plan.json /path/to/deb-cache
```

The wrapper validates the sealed exact entry set and aliases, reviewed
CA/bootstrap baseline, complete native parent cohort and every input before
copying. It requires a new output and refuses an already refreshed parent without
a separately reviewed revision. Ubuntu 26.04 retains q35; Ubuntu 24.04 retains
microvm. Raw Rocky is not an Ubuntu/native-runtime parent.

Install the native transaction without starting image services, then restore the
original service-start policy. Preserve existing service masks, custom initramfs
module configuration, bootstrap ELF and runner trust environment. Require the
complete post-install cohort to equal the prepared transaction with every other
package unchanged. The accepted runtime/toolcache locks and payloads are retained;
a native package update does not claim to update downloaded runtimes.

The new generic kernels are `7.0.0-38-generic` for Ubuntu 26.04 and
`6.8.0-147-generic` for Ubuntu 24.04. Keep superseded kernel packages in the copied
root; no autoremove/purge transaction is authorized. Check the new generic config
for the existing profile's virtio support, including microvm's
`CONFIG_VIRTIO_MMIO_CMDLINE_DEVICES=y`, then generate the exact new initrd using
the existing initramfs-tools configuration. Extract only that locked kernel and
initrd, verify their report hashes, update the manifest and relative aliases,
compact and reseal the new bundle. Old kernel artifacts and the old effective
manifest/inventory/checksum list remain parent provenance. Every sealed entry,
including old artifacts, is hashed in the new seal.

Native package hooks can regenerate Java trust. Run the already reviewed exact
incident-SPKI Java correction after the native transaction; record that audit
separately and update effective Java store hashes. Do not reinstall Python wheels,
Go, Node, .NET, PHP or the runner. Require post-change actual CA/PHP/runtime client
acceptance against the finished stores, not a claim based on package installation.

## Resource and acceptance gates

Use the existing accepted builder, read-only source/inputs/parent, network none,
1 CPU/2 GiB/swap0 heavy container and 128 MiB/10%/swap0 manager. The appliance uses
one CPU/1024 MiB. Reserve at most `3 * source physical bundle size + 2 GiB` for the
single family's new output, input tar and temporary/cache work. Keep the original
aggregate/free-floor policy; if the measured reserve does not fit, the coordinator
must provide a fresh disk plan. Do not silently reclaim accepted images or grow
caps. The coordinator owns resource GO, availability, draining, task deadlines and
any later image switch; the executor proves owned QEMU/container/cgroup exit.

After a candidate is sealed, require native inventory/dpkg audit, all fixed PHP
cohorts and extensions/Composer, every actual Java/.NET/Python/Node/NSS trust path,
actual runner-private runtime identity and bootstrap retention, then cold READY
and cleanup on the existing profile/shapes and a real job under separate
controlled deployment authorization. Kernel config compatibility is not boot
proof. Browser Rust/default trust, OneCRL/Chrome Root Store/CT, code-signing,
credentialed Listener and EOL Node20 limitations remain explicitly scoped.

Ubuntu's available bubblewrap versions remain `0.11.1-1ubuntu0.3` (26.04) and
`0.9.0-1ubuntu0.3` (24.04). [USN-8779-2](https://lists.ubuntu.com/archives/ubuntu-security-announce/2026-September/011208.html)
reverted the CVE-2026-87766 fix. No fixed vendor version is asserted at these pins;
this transaction neither disables sandboxing nor describes all vulnerabilities as
fixed. Inventory actual installation and retain that finding separately.

## Other families

Ubuntu 24.04 needs its own complete parent-bound plan and resource GO; one U26
run never authorizes a second family. Its CA/PHP baseline can receive this native
transaction before or after the separately reviewed runtime refresh, but all
required runtime versions and consumer checks must be satisfied before deployment.

Rocky's older accepted parent has neither the combined trust-aware bootstrap nor
the vendor Node tuple. Do not feed it directly to runtime refresh. First prepare
an immutable CA/Go/bootstrap baseline with the reviewed combined binary and native
vendor CA package, then the official runtime refresh and a newly resolved signed
Rocky 10.2 native RPM/kernel transaction, or use a coherent full builder. Resolve
the complete installed-package/dependency cohort with native DNF and review it;
the October 19 RPM lock is not evidence of current October 25 package coverage.
No Rocky transaction, image launch or module substitution is implemented here.
NixOS remains independently owned; do not edit a user's uncommitted NixOS work.
