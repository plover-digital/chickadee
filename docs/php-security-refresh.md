# PHP security refresh

[USN-8948-1](https://ubuntu.com/security/notices/USN-8948-1) addresses
CVE-2026-109618 and CVE-2026-109619. Supported Ubuntu developer images contain
PHP CLI; its built-in development server can process untrusted HTTP. Command
presence alone does not establish remediation. No exploit is part of acceptance.

The narrow vendor minimums are Ubuntu 24.04 PHP 8.3
`8.3.6-0ubuntu0.24.04.13` and Ubuntu 26.04 PHP 8.5 `8.5.4-0ubuntu1.5`.
`guest/images/php-security.json` locks complete source-binary cohorts from both
main and universe: 35 PHP 8.3 packages and 33 PHP 8.5 packages, amd64/all.
Each deb has its exact official URL, size and SHA256. Signed security InRelease
hashes, archive signing fingerprint and SHA256-bound Packages indexes record
supplemental authentication independently of each image's original OS snapshot.
The signed metadata was independently verified with the Ubuntu archive keyring.
Retain those public signed files/indexes with private build evidence; replay:

```sh
python3 scripts/verify-php-security-metadata.py --policy guest/images/php-security.json --evidence /path/to/retained-metadata --keyring /path/to/ubuntu-archive-keyring.gpg
```

The verifier requires the exact locked releases/indexes, signer, complete source
cohort and all package identities/digests. Updating a lock requires resolving and
reviewing new signed vendor metadata; changing only a minimum is insufficient.
No broad apt update, dependency resolver, repository change or full OS snapshot
refresh is performed. Downloads are bounded below 64 MiB. Prefetch and verify:

```sh
python3 guest/refresh-php-security.py --policy guest/images/php-security.json --download /path/to/php-inputs
CHICKADEE_PHP_INPUTS=/path/to/php-inputs scripts/refresh-image-php.sh --prepare /path/to/temporary-input-work
```

All four Linux builders run the PHP helper after their CA refresh and seal its
report. Inside an offline private image, it updates only already installed
packages from the matching PHP source, including any installed SAPI and extension
cohort members. It verifies deb identity, fixed Debian versions, uniform cohort
version, package database health and preservation of all unrelated package
versions/names. Newer uniform fixed cohorts are retained; mixed/newer cohorts are
never downgraded. Unknown installed PHP sources/packages fail closed. Maintainer
scripts cannot start services while the offline package transaction runs; the
prior image-owned service policy is restored exactly.

Images without PHP receive an explicit not-installed report. Supported Rocky
source/package inventories contain no PHP. If Rocky PHP or an unmanaged PHP
executable appears, require its native vendor fix rather than treating an Ubuntu
deb as remediation. Native macOS and NixOS image inventories are coordinated
separately; this helper does not modify those images.

For an existing sealed CA candidate, create an independent immutable bundle:

```sh
CHICKADEE_PHP_INPUTS=/path/to/php-inputs scripts/refresh-php-bundle.sh /path/to/sealed-ca-bundle /path/to/new-combined-bundle
```

This starts trusted libguestfs appliances and requires a separate resource GO.
Validate exact sealed entries/hashes/kernel aliases and verify supplemental
inputs before the large copy. The original candidate is never edited. Retain
its existing bootstrap, CA report, tool caches, lock inventories and full OS
snapshot; save replaced inventories under `pre-php-` and bind the new source
commit, supplemental policy hash and parent seal in `php-build-provenance.json`.
The helper supports one retained PHP parent generation and refuses a collision.
Reserve at most three times the measured source size plus 2 GiB; run one family
at a time in the separately reviewed CPU/memory/swap/time/disk budget. Source
changes here do not require rebuilding an unchanged accepted guest bootstrap.

Normal `developer-replacement-smoke.sh` now runs
`scripts/check-php-security.py`: every installed PHP source member must meet the
exact vendor minimum and share one version; the expected CLI/common/intl/
mbstring/readline cohort and Ubuntu 24.04 opcache must exist. It checks actual
`php -v`, `php -m`, resolved package-owned PHP binary, required loaded extensions
and offline Composer version with plugins/scripts disabled. Existing profile,
kernel, toolcache and isolation checks remain. The same PHP check can run in the
combined candidate's disposable offline acceptance overlay before CA consumer
checks; it does not open the PHP development server. Separate cold READY and
real-job acceptance remain necessary before immutable revision deployment.

Run targeted source regressions with:

```sh
python3 -m unittest discover -s scripts -p test_php_security.py
```

Tests exercise old deployed cohort rejection versus fixed cohort acceptance,
mixed versions, missing extensions, unsupported sources/packages and tampered or
missing inputs. Native Debian version comparison and actual PHP/Composer commands
must additionally execute in the candidate; development hosts need not have PHP
or dpkg installed. No image or runtime acceptance is claimed by these tests.
