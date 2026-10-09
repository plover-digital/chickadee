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

## Native offline installation prototype

`scripts/macos/native-vm.swift` is an experimental Apple-native installer and
headless boot/stop helper. It is not a fleet worker, runner image or production
sandbox. Build on the Mac with:

```sh
./scripts/macos/build-native.sh /tmp/chickadee-native-vm
/tmp/chickadee-native-vm restore-info
/tmp/chickadee-native-vm install /path/to/verified-apple.ipsw /private/new-vm-state
/tmp/chickadee-native-vm boot /private/new-vm-state
```

The helper creates a private 64 GiB sparse installation disk, uses 2 vCPUs and
4 GiB guest RAM, and checks the restore image's hardware and memory requirements.
It reserves full disk growth plus 20 GiB free storage at installation time. A
process-held flock in the worker user's private state allows only one prototype
install/boot at a time for that account, even across different guest directories.
It does not prevent another administrator or application from running their own
VM. No NIC, shared host directory, serial port, or GitHub credential is attached.
TERM/INT on the boot helper requests VM stop, confirms the stopped state, and
retains all disks. No automatic destructive cleanup is provided for this offline
prototype. Keep failed installation state for diagnosis; never reuse a later
credentialed guest. Startup demonstrates VM running state, not bootstrap READY.

Guest user provisioning, tool installation, bootstrap transport and fleet API
integration remain pending. In particular, macOS setup cannot be assumed complete
when the native install callback returns. No customer queue is active yet.

## GitHub software target

The guest targets GitHub's macOS 26 ARM64 image. `scripts/macos/baseline.lock.json`
pins the official runner-images commit, source path and SHA256 along with macOS
26.6.2 and default Xcode 26.6 / build17F113. This lock is a parity target, not a
claim that tools are installed. Use that commit's complete manifest to verify
Xcode/SDKs, simulators, package managers, runtimes, cached tools, utilities,
browsers and environment settings. A latest Homebrew upgrade on the host does
not establish guest parity. The final software image's disk size will be assessed
separately from this initial installation disk.

Proposed hosted label: `chickadee-small-macos-26`, explicitly opt-in. Linux's
`chickadee` default stays unchanged. Customer selection/admission is gated on a
platform-aware worker contract and real one-job lifecycle/isolation acceptance;
do not add the label to accepted dashboard requests ahead of backend support.

On a headless Mac, native macOS installation can fail with `VZErrorDomain -9`
and a nested HostKey creation error while the login keychain is inaccessible.
Sign into the worker account's desktop to establish its security context and
unlock the login keychain before retrying. This is separate from sudo access.
Do not send account/keychain passwords in chat, disable SIP, or expose the host
keychain to guests. A reviewed headless service/keychain strategy remains needed
for automatic recovery after host reboot. Related upstream
[Virtualization framework diagnosis](https://github.com/openai/tart/issues/1146).
