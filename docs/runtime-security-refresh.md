# Linux runtime and native trust refresh

The October 25 inputs update the official complete runner, supported Node 22/24
caches, existing Python cache lines and certifi, native Ubuntu NSS and Rocky CA packages. Developer extras
use current official .NET security releases, Rust/rustup and browser packages.
Original OS snapshots remain parent provenance; this is a supplemental transaction,
not an operating system upgrade. The guest bootstrap and kernel are retained.

[Node's October security release](https://nodejs.org/en/blog/vulnerability/october-2026-security-releases)
fixes the supported Node lines. The runner archive still contains EOL Node 20;
its exact limitation and source provenance are in `runner-linux-x64.json` and
[image security](image-security.md). No individual vendor Node directory is replaced.

Signed Ubuntu archive indexes pin NSS 2:3.98-1ubuntu0.3 on 24.04 and
2:3.120-1ubuntu2.2 on 26.04. Their vendor patches remove the compromised native
builtins. Only already installed members of the authenticated NSS source cohort
are updated. No p11-kit provider substitution is needed for this route. The Rocky
CA update is a vendor-signed RPM; its repository primary metadata has a checksum,
which is distinct from a signed repository metadata claim.

Java trigger completion and exact SPKI removal use the existing reviewed helper.
Every real JDK store is checked, and unaffected alias/certificate identities must
remain equal. Python refresh reuses the same implementation as full builds,
without reinstalling Go. Extra-tool replacement removes old SDK/shared runtime
roots before extraction; adding new archives over existing roots leaves old
vulnerable runtimes executable. Six currently serviced SDK bands replace eleven
previous bands. Workflows pinned to removed bands must select a current SDK.

Prepare inputs without launching a container, appliance or guest:

```sh
python3 scripts/prefetch-runtime-inputs.py /path/to/private-inputs
python3 scripts/prefetch-runtime-inputs.py --verify-only /path/to/private-inputs
```

After a separate bounded resource authorization, the trusted builder runs:

```sh
CHICKADEE_RUNTIME_INPUTS=/inputs scripts/refresh-runtime-bundle.sh \
  /accepted/parent /fresh/private-output EXACT_PARENT_SEAL 26.04
```

Use `24.04` for the other Ubuntu family. **Raw older Rocky bundles are not
valid runtime-refresh parents:** first produce a trusted CA/PHP baseline with
the reviewed combined bootstrap and vendor Node tuple, or use the coherent full
builder. A runtime refresh preserves its parent bootstrap; native CA installation
does not add the parser or environment inheritance. Parent provenance and the
actual bootstrap ELF/trust tuple are checked before any runtime edit. Only after
that baseline exists may `rocky` be used. No raw-Rocky incremental command is
accepted by this path. The helper verifies
the complete parent seal, exact flat entry set and expected kernel aliases before
copying. It verifies supplemental hashes before the large copy, retains replaced
metadata as `pre-runtime-*`, derives effective tool inventories and seals a new
bundle. It never installs the candidate into live configuration. A new bootstrap
build is unnecessary because no Go source or parser behavior changes.

Admission requires the updated normal smoke checks and actual client controls:
PHP version/cohort/extensions/Composer first, then all twelve compromised chains
with valid positive controls for Java, native NSS, runnable Node, Python and .NET.
Reject empty PEM exports as setup failures. A missing musl interpreter is an
unavailable executable, not trust rejection. Transport success does not establish
assertion success. Browser profile/OneCRL/Chrome Root Store and runner Listener
behavior require their own evidence; native NSS/package inventory cannot clear them.
