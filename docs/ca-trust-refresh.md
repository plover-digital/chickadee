# eMudhra guest trust remediation

The [primary incident report](https://emudhra.we-have-a-problem.com/),
[Mozilla advisory](https://groups.google.com/a/mozilla.org/g/dev-security-policy/c/0R3XLJL6FuE)
and [Go advisory](https://groups.google.com/g/golang-announce/c/66pojueaQek)
require independently reviewing OS roots and embedded runtime roots. This source
change is not evidence that any immutable image has been updated or deployed.
Public implementation tracking: [#39](https://github.com/plover-digital/chickadee/issues/39).

`guest/images/ca-trust.json` records the exact four root SPKI/certificate hashes,
eight compromised intermediate SPKI hashes, public chain archive hash, and fixed
official package/wheel downloads. The eight proof signatures were checked against
the primary public certificates. No private key or deployment fixture is needed
or committed. Six intermediate keys are TLS issuing keys; two are code signing
only. Their direct code signing root is identified separately. The fallback
matches exact public keys, never subject strings or every eMudhra certificate.
Removing a root also affects legitimate sibling chains under that key; this is
the scoped vendor-recommended pending-fix fallback, not an intermediate-only
block. TLS's 12 chains do not test code signing verification.

## Inputs and behavior

- Ubuntu 24.04/26.04: install official `ca-certificates`
  `20260816ubuntu0.24.04.1` / `20260816ubuntu0.26.04.1`. The signed October 21
  archive's InRelease, package index and package SHA256 were verified. The
  supplemental lock pins those exact packages. Existing full-image October 20
  package snapshots remain unchanged; an incremental refresh does not upgrade
  unrelated OS packages or describe the whole image as an October 21 rebuild.
- Rocky 10.2: the reviewed current BaseOS metadata still contains
  `2025.2.80_v9.0.305-102.el10`. Fedora's `2026.2.92_v9.0.319` fix is not a
  Rocky package. While a native Rocky fix is pending, install all four primary public root certificates into the image's p11-kit
  blocklist after verifying both certificate SHA256 and SPKI SHA256, then
  regenerate its extracted stores. Extract the native blocklist without any
  purpose filter and require all four keys to be distrusted, including the
  code-signing root that a TLS-only bundle can omit. Existing blocklist files
  are preserved. This verifies native purpose-independent distrust; the supplied
  TLS chains still do not establish code-signing client acceptance. Replace this
  fallback with an independently resolved, signature-verified native vendor
  package when available; never transplant a Fedora RPM into Rocky.
- Node: the [upstream fix](https://github.com/nodejs/node/pull/66871) was merged,
  but the primary incident page still listed the release as pending during
  review. Preserve official runner/cache binaries and configure the official
  `--use-openssl-ca` option plus exact vendor `SSL_CERT_FILE` and `SSL_CERT_DIR`.
  These settings replace default bundled roots; `NODE_EXTRA_CA_CERTS` would
  merely append and is insufficient. Bootstrap accepts only complete exact
  vendor-store tuples. Runner.Listener and inherited Node subprocesses receive
  the settings through its existing clean image-owned environment. The compiled
  built-in root list remains old; explicit application CA overrides, deliberate
  option overrides, downloaded runtimes and newly installed custom trust are
  outside this default-store mitigation. Require official Node/runner updates
  when available.
- Python: install checksum-pinned official pip `26.2.2` and certifi
  `2026.10.19` without dependency resolution or network access into every
  installed toolcache interpreter. For the distribution interpreter, place
  those two packages in `/usr/local`, preserving distribution-owned package
  files. Refresh ensurepip's embedded pip wheel CA data from the exact official
  certifi bundle and regenerate RECORD, preserving its original filename and
  version constants. Record each modified wheel's before/after SHA256; new venvs
  must not resurrect an older vendored CA bundle. This is a recorded trust-data
  customization, not a claim that ensurepip itself has a newer pip version.
- Java: Ubuntu's fresh CA update runs the Java store hook; Rocky's extraction
  regenerates its Java store. Final acceptance must inspect each installed
  JDK's actual `cacerts` path and validate all chains with its CertPathValidator.
  Arbitrary application keystores are not modified.
- .NET: Linux's system trust consumer must be tested using the runner's actual
  runtime and each installed SDK's X509Chain with downloads/revocation disabled.
  An OpenSSL check alone is not .NET acceptance. Existing .NET/runner archive
  versions are retained; no separate root-bundle update is asserted.
- Go/toolcache: the incremental edit installs exact authenticated Actions
  archives 1.26.10 and 1.27.3 from the combined source lock, removes older cached
  Go versions and updates the default link to 1.27.3. This is the same supported
  cache/default contract as the full builder and updated smoke workflows.
- Go/bootstrap: combine the separately reviewed Go update with this change,
  rebuild using Go 1.27.3, and retain the exact combined source commit, build
  information and binary hash. A pre-change bootstrap rejects the new trust
  environment keys. Do not deploy that intermediate binary. Refresh the Go
  toolcache lock through the separate Go change as appropriate.

NixOS source remains independently owned and is not edited here. Its pinned
nixpkgs/NSS/CA package needs the vendor fix, or the primary report's narrowly
scoped `security.pki.caCertificateBlacklist` entries while pending. Its runner
Node, Python, Java and bootstrap require the same effective-store acceptance;
updating a system CA alone is not sufficient.

## Bounded incremental candidate build

First combine [Go PR #54](https://github.com/plover-digital/chickadee/pull/54)
and this trust change, then rebuild the bootstrap from that exact
clean source. The following command starts a trusted libguestfs appliance; it
requires the deployment coordinator's resource GO before execution:

```sh
scripts/refresh-trust-bundle.sh /path/to/accepted-bundle /path/to/new-bundle /path/to/combined-source-chickadee-guest /path/to/bootstrap-manifest.json
```

Use the previously accepted builder environment, one family at a time, with an
external 1-CPU / 2-GiB memory / zero-swap limit. Each appliance requests one vCPU
and 1024 MiB; allow the remaining memory for its host tools. Reserve at most
three times source physical bundle size plus 2 GiB for input/output/copy work;
measure physical size, available space and the allocator's quotas before GO.
No full developer reinstall, source image write, runner VM boot, admission change
or live bundle switch is performed. Parent coordination owns availability,
sustained headroom, OOM/time/disk budgets and abort decisions; ordinary paging
alone is not an abort condition.

The helper validates the exact sealed file set, every digest and the two
relative aliases to the manifest's sealed regular kernel/initrd targets before
copying. It also requires the binary's actual `go version -m` compiler to equal
the manifest's Go 1.27.3. It prefetches/checks every supplemental
input before making an independent qcow2 copy. Set `CHICKADEE_TRUST_INPUTS`
to a prepared read-only input cache to require local files and avoid network
dependency during staging. It installs
only pinned trust inputs offline, replaces the bootstrap and both authenticated Go caches (default 1.27.3), refreshes package
inventory/provenance, and seals the candidate. It preserves parent inventories,
manifest and checksum list, plus the original kernel and initrd. Old tool locks
and compatibility inventory remain under `parent-` names; effective inventories
replace only Go entries/default and preserve every other tool input. The new manifest
retains its original full OS snapshot and adds a parent-bundle seal digest and
trust revision. `ca-trust.json` records exact input policy, effective system CA
hash, Python interpreters and ensurepip wheel modifications.
`trust-build-provenance.json` binds source commit, parent seal, bootstrap and
policy hashes. The bootstrap manifest must bind its exact source commit,
compiler and binary SHA256 and is copied into the output. Retain the builder digest and resource log externally too.

The source tests and verified downloads do not validate this whole libguestfs
transaction. Require its first bounded execution, store/runtime audit, cold
READY/cleanup and real-job acceptance before deployment. Follow the controlled
drain and immutable revision switch in [image security](image-security.md).

## Verification

Download `chains.zip` from the primary report into private temporary evidence.
The checker requires its pinned hash and exactly the 12 reviewed PEM chains.
It disables default OpenSSL stores, verifies hostname and server purpose, and
counts only issuer/trust failures as remediation: expiry or malformed data is
not a pass.

```sh
python3 scripts/check-ca-chains.py --chains-zip /tmp/chains.zip --ca-bundle /tmp/still-trusting-roots.pem --expect accept
python3 scripts/check-ca-chains.py --chains-zip /tmp/chains.zip --ca-bundle /tmp/candidate-roots.pem --expect reject
python3 -m unittest discover -s scripts -p test_ca_trust.py
```

The primary four-root control accepts all 12; both fixed Ubuntu package bundles
and the official pip/certifi bundles reject all 12. One older cached bundle
already omitted AAA and accepted only six; it is not an all-12 positive control.
The unit/integration suite also verifies exact-key scope against two certificates
with identical subjects, wheel RECORD consistency, tampered input rejection,
rejection of unsealed files/wrong kernel aliases and a mismatched actual Go
compiler, complete purpose-independent Rocky root distrust, and actual
Node-parent to Node-child TLS trust selection using a generated local
canary. Set `CHICKADEE_TEST_NODE_BINARIES` to colon-separated actual runner/cache
Node paths. The canary fails with bundled roots and succeeds with the specified
OpenSSL file; it does not pretend to perform a complete TLS handshake using the
12 supplied leaf certificates, whose private keys are not supplied.

Final artifact acceptance additionally requires each installed Java/.NET runtime,
actual guest Node environment/identity, Python import/default and vendored stores,
and all 12 offline chains; retain exact runtime versions and outputs. These checks
are pending until candidate image access is authorized. A source PR must not be
reported as completion of those artifact or deployment gates.

For the independent narrow PHP fix and combined immutable candidate path, see
[PHP security refresh](php-security-refresh.md). Existing CA-only candidates remain
unchanged; PHP and CA consumer acceptance must use the final combined candidate.

## Independent Java and NSS acceptance

A successful CA package install is insufficient evidence for Java or NSS.
Offline consumer checks found a stale shared Java store and an NSS builtin
module still accepting direct incident chains. These are release blockers.
The immutable candidate must remain unchanged; apply corrections only to a new
copy and retain the old result as a negative regression control.

Ubuntu refresh now explicitly completes pending package triggers after the
vendor fresh-store hook. It then discovers every installed JDK's actual cacerts,
deduplicates resolved stores, and inspects aliases with that JDK's keytool.
While Java's [upstream update remains pending](https://ops.java/security/),
remove only aliases containing one of the four authenticated incident root
SPKIs. Before editing, validate the primary certificate DER and SPKI hashes.
Edit a temporary copy, require every unaffected alias and DER to remain identical,
and atomically replace the store. Record removals, participating JDK homes and
final store hashes. Unexpected entries, invalid policy or failed edits stop the
refresh. Custom application keystores are outside this discovery contract.

`python3 scripts/check-independent-trust.py` now runs in developer acceptance.
It inspects every actual JDK store and explicitly loads each distro NSS builtin
module into a fresh disposable NSS database. It exports native trusted anchors
and rejects any authenticated incident root key. Missing modules, failed loads,
empty enumerations and invalid certificate exports fail acceptance. This guard
is an anchor inventory, not a substitute for all twelve chain checks, browser
policy validation or credentialed runner acceptance.

Mozilla identifies [NSS 3.131.1 as its upstream fix](https://blog.mozilla.org/security/2026/10/20/protecting-firefox-users-following-the-compromise-of-emudhra-intermediate-certificates/).
Use an authenticated native Ubuntu backport when available. The signed indexes
reviewed on October 21 still list Noble `2:3.98-1ubuntu0.2` and Resolute
`2:3.120-1ubuntu2.1`; these versions must not be cleared merely because the
system bundle is patched. This change detects the NSS blocker and does not
replace its module or assert that all guest trust remediation is complete.

The [upstream-supported p11-kit module interface](https://p11-glue.github.io/p11-glue/p11-kit/manual/trust-nss.html)
is a possible fallback requiring separate review. Ubuntu's module reads the
system CA bundle; replacing the builtin module can therefore change unrelated
anchors and purposes. Before considering a replacement, enumerate both modules'
certificate DER, SPKI, native trust objects and purpose-specific trust bits.
Require exact preservation for every unrelated object; report any difference as
a blocker rather than silently adopting a different provider's root policy.
Verify exact incident-key distrust, fresh NSS database module loading, all twelve
incident chains against a still-trusting positive control, and unrelated positive
chains. No module substitution or browser/profile policy change is implemented
here. Firefox's bundled NSS, OneCRL and Chrome's independent root/revocation
components remain separate acceptance work.

For an already patched Ubuntu private working image, the bounded Java-only
appliance helper avoids reinstalling Python, Go, PHP or the full OS:

```sh
scripts/refresh-image-java-trust.sh /path/to/private-working.qcow2 /path/to/new-java-trust.json
```

This helper completes pending Ubuntu triggers and applies the same audited
store correction. It does not copy/seal/promote an immutable bundle or fix NSS;
the coordinator must provide the independently validated copy, resource limits,
new supplemental provenance/seal and full client acceptance. It requires no new
package downloads or bootstrap rebuild.
