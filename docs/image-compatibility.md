# GitHub runner image compatibility

Chickadee's original images contained the Actions runner and a minimal build
baseline. They did **not** contain the software inventory of GitHub-hosted
`ubuntu-24.04`. Workflows relying on those implicit tools can fail.

The compatibility builder adds a broader credential-free baseline. Its reference
is the official [`actions/runner-images` inventory](https://github.com/actions/runner-images/blob/5f7588b285eccc2edbeb1cd79d65ee0b577e4b4a/images/ubuntu/Ubuntu2404-Readme.md)
and [toolset](https://github.com/actions/runner-images/blob/5f7588b285eccc2edbeb1cd79d65ee0b577e4b4a/images/ubuntu/toolsets/toolset-2404.json),
pinned to commit `5f7588b285eccc2edbeb1cd79d65ee0b577e4b4a`, image
`20260927.320.1`. The reference is a comparison target, not a claim of complete parity.

## Build

First build the minimal Ubuntu image using `scripts/build-image.sh`, then run
this in the same Ubuntu 24.04/libguestfs build environment:

```sh
scripts/build-github-compatible-image.sh images build/github-compatible/image
scripts/audit-image-compatibility.py build/github-compatible/image
```

The output is a fresh **48 GiB** ext4/qcow2 image. Update configured image/resource
`disk_gib` values to match its manifest before installing it. Existing guests
must finish and be destroyed before switching the immutable image revision.
The builder edits a local offline image with libguestfs; it does not change host
networking or mount host credentials into a guest.

Included:

- The official toolset's vital/common/command APT package groups, including
  `unzip`, GNU `tar`, `gzip`, `zstd`, `sudo`, Git LFS, and common build libraries.
- GCC/G++/Fortran 12/13/14; Clang/format/tidy 16/17/18; CMake and Ninja.
- Go 1.24.13, 1.25.14, 1.26.8; Node 22.23.3 and 24.21.0; Python 3.10.21,
  3.11.16, 3.12.14, 3.13.15, 3.14.7 in `/opt/hostedtoolcache` with completion
  markers understood by `actions/setup-*`.
- Java 17/21, Maven, Ant, Ruby, PHP/Composer, and Python pip/venv/pipx.
- Guest-local Docker, Compose, Buildx, Podman, Buildah, and Skopeo. The guest
  runner can use Docker and passwordless sudo. No host Docker socket is shared.
- PostgreSQL, MySQL, Apache, and Nginx, disabled until a workflow starts them.
- Rust 1.98.1 and Rustup 1.29.1; all eleven .NET SDK versions in the pinned
  hosted inventory; Firefox 156.0 and Geckodriver 0.37.1.
- Chrome 155.0.8059.39 from a pinned official Google APT package. Its version
  differs from the reference Chrome 154.0.8037.57 because that older package
  checksum is no longer published in the current vendor repository. The lock
  records this difference.

Tool archives use fixed official release URLs and SHA256 or SHA512 digests from
GitHub release metadata and Microsoft/Rust/Mozilla/Google published checksums;
cache hits are reverified. APT resolves against the existing
signed dated Ubuntu snapshot, with all four archive components enabled.
Downloaded scripts are not used to fetch mutable dependencies; Python's
upstream setup script would upgrade pip from a live registry, so the builder
installs the verified archive directly instead.

The output includes `packages.txt`, `tool-lock.json`, `extra-tool-lock.json`,
`compatibility.json`, and
`SHA256SUMS`. The audit verifies the pinned reference checksum, lists missing APT
packages and different package versions, and reports remaining capability gaps.
It exits unsuccessfully while parity gaps remain. Package versions from the
Ubuntu snapshot can differ from the Azure-hosted reference even when the
capability exists. The kernel remains Chickadee's tested stock microvm kernel,
not the Azure kernel.

## Remaining work

This layer does not yet supply the full Android SDK/NDK matrix, Edge and all
browser drivers, Swift/Julia/Kotlin/Haskell, Ruby/PyPy tool caches, additional Java
versions, PowerShell modules, CodeQL, Homebrew, or all cloud/Kubernetes CLIs. Those need independently pinned inputs and guest tests;
the builder and website must not advertise full GitHub-hosted parity meanwhile.

GitHub does not publish a hosted Rocky Linux image. Rocky 10.2 compatibility must
map tool capabilities and use supported Rocky-native packages rather than copy
Ubuntu packages or Ubuntu-specific Python toolcache binaries. Go and Node
archives are candidates for sharing after runtime validation. Keep Rocky SELinux
enforcing and preserve the stock q35 kernel path.

Before rolling out a new revision, boot it to READY and run the manual
`image-compatibility.yml` workflow, including setup-action cache consumption and
guest-local Docker. Successful construction alone is not job compatibility.

All tool caches in this layer are immutable image inputs copied into disposable
VM disks. They are not shared customer caches. Job writes disappear when the VM
is destroyed, and images never contain GitHub credentials.
