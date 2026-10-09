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

Experimental offline guest account/Xcode provisioning is available below.
Bootstrap transport, remaining software and fleet API integration remain pending.
macOS setup cannot be assumed complete when the native install callback returns.
No customer queue is active yet.

### Experimental offline image provisioning

`scripts/macos/provision-offline.py` installs an expanded Apple-signed Xcode and
a build-only first-boot LaunchDaemon into the Data volume of a stopped, trusted
installation image. It requires Python 3, passwordless sudo for disk operations,
and an explicit `--trusted-never-credentialed-build-image` assertion. It holds
the native helper's single-VM lock while attached and selects only a Data volume
belonging to that attached image. Never run it on a customer/credentialed disk.

```sh
python3 scripts/macos/provision-offline.py /private/new-vm-state \
  /private/staging/Xcode.app --trusted-never-credentialed-build-image
```

Verify the archive with `pkgutil --check-signature` before expansion. This tool
does not change host Xcode selection or host networking. Guest provisioning
creates an administrative `runner` account with a randomly generated local
password, accepts the guest Xcode license, initializes Xcode, and records
build status/version/SDK files in `/var/db/chickadee-build`. It does not register
with GitHub. `PROVISIONED_OFFLINE` is a build milestone, not bootstrap READY.
GUI login, simulator runtimes, remaining software, isolated credential transport,
and one-job lifecycle are separate acceptance requirements. Remove the image-build
service from the final immutable runtime image once those build checks complete.

An Apple-native offline acceptance boot created the guest account and reported
macOS 26.6.2/build25G83, Xcode26.6/build17F113 and working `xcodebuild -showsdks`.
The guest completed first-launch installation. The VM then stopped successfully
before build-only diagnostics were inspected. A concurrent attempt to attach the
image was refused by the process lock. This does not verify GUI/simulator execution,
the complete GitHub software manifest, runner registration or customer isolation.

### Build-only live status channel

The native helper optionally attaches an owned/private 1 MiB `build-control.raw`
as a dedicated Virtio block device. It exposes no host directory or network
service. This experimental build transport is separate from the runner serial
protocol and worker API; Linux workers and the v1 worker contract are unchanged.
It supports only `PROVISIONING` and `PROVISIONED_OFFLINE`, not READY or credentials.

```sh
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath \
  -o /private/staging/chickadee-build-report scripts/macos/guest/build-report.go
codesign --force --sign - /private/staging/chickadee-build-report
python3 scripts/macos/control-device.py create /private/new-vm-state
python3 scripts/macos/provision-offline.py /private/new-vm-state \
  /private/staging/Xcode.app --trusted-never-credentialed-build-image \
  --reporter /private/staging/chickadee-build-report
```

During a bounded offline boot, `control-device.py read /private/new-vm-state`
reads one fixed 4096-byte response page. The parser rejects malformed JSON,
duplicate/extra fields, invalid status, stale nonce, unsafe file ownership and
symlinks. The expected nonce lives in host-only `build-control.json`, outside the
guest device. A nonce is session correlation, not proof of guest honesty.
Every new test needs a newly created control pair; create refuses existing files.
Never accept an old response page as evidence that a new guest boot succeeded.
Remove/replace the build-control pair only after confirmed VM exit. This is not
a production credential transport or restart-reconciliation implementation.

A fresh-channel native acceptance boot reported `PROVISIONED_OFFLINE` in about
14.5 seconds and then confirmed VM stop, without mounting the guest filesystem
while running. This measures this build-service test, not job startup latency.

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
