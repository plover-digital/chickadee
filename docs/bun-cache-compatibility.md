# Bun and cross-runner caches

Chickadee uses `/home/runner/work/<repository>/<repository>` as its job workspace,
matching GitHub-hosted Linux runners. This is part of cache compatibility, not
just a cosmetic directory choice.

The official Actions cache toolkit stores archive members relative to
`GITHUB_WORKSPACE`. For Bun, a hosted job caches
`/home/runner/.bun/bin/bun` as `../../../.bun/bin/bun`. Restoring that archive
from the original Chickadee workspace under `/opt/actions-runner/_work` placed
the binary in `/opt/actions-runner/.bun/bin/bun`. Cache extraction reported
success, but `setup-bun` could not find the executable under HOME.

The controller now requests `/home/runner/work` in each fresh JIT configuration.
The guest images also provide `unzip`, GNU tar and zstd: `setup-bun` needs unzip
for a fresh download, and the Actions cache toolkit uses tar/zstd for Linux
caches. Bun itself can be installed with the version requested by the workflow.

The public [Bun compatibility workflow](../.github/workflows/bun-compatibility.yml)
first installs Bun 1.3.14 on a hosted Ubuntu runner, then restores its cache on
Chickadee Ubuntu 24.04, Rocky 10.2 and the default Ubuntu 26.04 queue. Each job uses a fresh disposable guest.

The initial reproduction is retained at
[Actions run 37516442214](https://github.com/plover-digital/chickadee/actions/runs/37516442214).
It confirmed both the misplaced cached executable and missing unzip in the
original minimal images. A successful run against the rebuilt images is
required before treating the compatibility fix as deployed and validated.

Upstream implementation references:
[Bun setup and revision check](https://github.com/oven-sh/setup-bun/blob/0c5077e51419868618aeaa5fe8019c62421857d6/src/action.ts),
[Actions cache relative paths](https://github.com/actions/toolkit/blob/main/packages/cache/src/internal/cacheUtils.ts),
and [tar archive creation and extraction](https://github.com/actions/toolkit/blob/main/packages/cache/src/internal/tar.ts).
