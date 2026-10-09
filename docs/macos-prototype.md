# Apple Silicon macOS feasibility

macOS workers are not implemented or admitted to the hosted fleet. The existing
Linux/QEMU worker and v1 machine inventory cannot launch macOS guests. Track
[the native worker proposal](https://github.com/plover-digital/chickadee/issues/14).

On an Apple Silicon Mac with Command Line Tools installed, run:

```sh
./scripts/macos/check-host.sh
```

This compiles an ad-hoc-signed native probe with the per-application
`com.apple.security.virtualization` entitlement and queries Apple's supported
restore-image metadata. It prints the supported restore version, image-specific
minimum CPU/RAM, and hardware-model support. It downloads no IPSW, starts no VM,
alters no host networking, and includes no management credentials. Exit status
is nonzero if metadata is unavailable or no supported configuration exists.
The generic framework minima are not macOS image minima; use the image-specific
requirements returned by this probe.

An initial small profile targets 2 vCPUs, 4 GiB guest RAM and one VM on an 8 GiB
host, subject to restore-image requirements and measured host overhead. A warm
VM consumes the same slot as a running VM: do not add a second guest while a job
runs or while an uncertain VM is being retired. No suspension or memory snapshots.

The native implementation must first prove offline installation/boot, then an
isolated bootstrap transport, per-runner JIT, one-job exit/destruction, and restart
recovery. New networking needs a reviewed plan and explicit operator approval.
Do not expose a queue merely because the metadata probe succeeds. OS/architecture
capability negotiation, disk/auxiliary-storage cloning, platform resource limits,
launchd integration and hosted policy remain work to implement. See Apple's
[installation documentation](https://developer.apple.com/documentation/virtualization/installing-macos-on-a-virtual-machine).
