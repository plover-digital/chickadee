# Ubuntu 26.04 runner image

The Ubuntu 26.04 recipe uses a stock Ubuntu generic kernel and QEMU q35 with
KVM. It does not build a custom kernel or use memory snapshots. The 26.04
profiles use the compact version suffix `2604`.

On a supported Ubuntu 24.04 amd64 build host with the standard image build
dependencies installed:

```sh
# Small image for boot and serial protocol validation (16 GiB virtual disk).
./scripts/build-ubuntu-2604-image.sh images/ubuntu-2604-minimal

# Developer image with the common GitHub tool baseline (48 GiB virtual disk).
./scripts/build-ubuntu-2604-image.sh images/ubuntu-2604 developer
```

Use a fresh output directory. The build checks Canonical's signed checksum
document against the pinned root image hash and signing fingerprint, uses
signed APT packages from a dated Ubuntu snapshot, and checks every runner,
toolcache and extra SDK download. Kernel and initrd filenames include the
actual package version. The bundle records its package inventory and input
locks alongside `SHA256SUMS`; install it as an immutable revision.

Inputs are pinned in [ubuntu-2604.json](../guest/images/ubuntu-2604.json),
[github-ubuntu-2604.json](../guest/images/github-ubuntu-2604.json), and
[github-extra-ubuntu-2604.json](../guest/images/github-extra-ubuntu-2604.json).
The Canonical base is the
[20260927 release](https://cloud-images.ubuntu.com/releases/resolute/release-20260927/),
with package snapshot `20260930T000000Z`. The comparison target is GitHub's
[Ubuntu 26.04 image inventory 20260927.149.1](https://github.com/actions/runner-images/blob/5f7588b285eccc2edbeb1cd79d65ee0b577e4b4a/images/ubuntu/Ubuntu2604-Readme.md).

The developer profile provides Go 1.24/1.25/1.26 toolcache versions, Node 22/24,
and Python 3.10–3.14 archives built specifically for Ubuntu 26.04. Ubuntu 24.04
Python archives are not copied into this image. Defaults are Go 1.26.8,
Node 24.21.0 and Java 17; Java 11, 21 and 25 are also available. Native Python
follows Ubuntu's Python 3.14 package, and setup-python can select the pinned
toolcache versions. The profile includes compilers, Docker inside the guest,
Compose/Buildx, Podman, Rust, .NET SDKs, Firefox, Chrome and geckodriver.

This is core developer compatibility, not full hosted-image parity. Java is
Ubuntu OpenJDK rather than GitHub's Temurin distribution. Android tooling,
additional browser drivers, cloud CLIs, CodeQL, Homebrew and several language
toolchains remain outside this recipe. Consult the bundle's compatibility
report and the [general compatibility notes](image-compatibility.md).

Changing `chickadee` to this image requires a controlled configuration update
after READY and real-job validation. It does not happen automatically during
image construction. The
[Ubuntu 26.04 smoke workflow](../.github/workflows/ubuntu-2604-smoke.yml) exercises
the toolcache, setup actions, Bun, compilers and the guest-local Docker daemon.
