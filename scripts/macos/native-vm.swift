// Experimental offline macOS VM installer/launcher. No guest networking or JIT.
import Foundation
import Virtualization
import Darwin

func fail(_ message: String) -> Never {
    FileHandle.standardError.write(Data((message + "\n").utf8))
    exit(1)
}
let arguments = Array(CommandLine.arguments.dropFirst())
guard arguments.count >= 1 else { fail("Usage: native-vm restore-info | install IPSW STATE | clone BASE STATE | boot STATE [--network-fd FD]") }
let operation = arguments[0]
let cpuCount = 2
let memoryBytes: UInt64 = 4 * 1024 * 1024 * 1024
let diskBytes: Int64 = 64 * 1024 * 1024 * 1024
var machine: VZVirtualMachine?
var installer: VZMacOSInstaller?
var guestDelegate: GuestDelegate?
var signalSources: [DispatchSourceSignal] = []
var hostLock: Int32 = -1
var networkHandle: FileHandle?
var lifetimeFD: Int32 = -1
var lifetimeSource: DispatchSourceRead?
var activeRoot: URL?
var stopping = false

@Sendable func finishStopped() -> Never {
    guard let vm = machine, vm.state == .stopped else { fail("VM stop unconfirmed; retain state") }
    if let root = activeRoot, FileManager.default.fileExists(atPath: root.appendingPathComponent("runner-control.json").path) {
        guard let metadata = try? JSONSerialization.jsonObject(with: readPrivate(root.appendingPathComponent("runner-control.json"))) as? [String: Any],
              let nonce = metadata["nonce"] as? String else { fail("Stop metadata unavailable; retain state") }
        let proof: [String: Any] = ["v": 1, "vm_id": root.lastPathComponent, "nonce": nonce, "stopped": true]
        guard let data = try? JSONSerialization.data(withJSONObject: proof, options: [.sortedKeys]) else { fail("Stop proof encoding failed") }
        writePrivate(data, root.appendingPathComponent("native-exit.json"))
    }
    print("VM_STOPPED")
    exit(0)
}
@Sendable func requestStop() {
    guard !stopping else { return }
    guard let vm = machine else { fail("Missing VM handle") }
    if vm.state == .stopped { finishStopped() }
    if vm.state == .starting {
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.1) { requestStop() }
        return
    }
    stopping = true
    vm.stop { error in
        guard error == nil else { fail("VM stop unconfirmed; retain all state") }
        finishStopped()
    }
}

func safeDirectory(_ url: URL, create: Bool) {
    if create {
        do { try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700]) }
        catch { fail("Cannot create private state directory") }
    }
    var info = stat()
    guard lstat(url.path, &info) == 0, (info.st_mode & S_IFMT) == S_IFDIR,
          info.st_uid == geteuid(), info.st_mode & 0o077 == 0 else { fail("State directory must be owned, private and not a symlink") }
}
func writePrivate(_ data: Data, _ url: URL) {
    let fd = open(url.path, O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW | O_CLOEXEC, 0o600)
    guard fd >= 0 else { fail("Refusing to overwrite existing VM metadata") }
    let file = FileHandle(fileDescriptor: fd, closeOnDealloc: true)
    do { try file.write(contentsOf: data); try file.synchronize(); try file.close() }
    catch { fail("VM metadata write failed") }
}
@Sendable func readPrivate(_ url: URL) -> Data {
    let fd = open(url.path, O_RDONLY | O_NOFOLLOW | O_CLOEXEC)
    guard fd >= 0 else { fail("Private VM metadata unavailable") }
    let file = FileHandle(fileDescriptor: fd, closeOnDealloc: true)
    var info = stat()
    guard fstat(fd, &info) == 0, info.st_mode & S_IFMT == S_IFREG,
          info.st_uid == geteuid(), info.st_mode & 0o077 == 0, info.st_size <= 65536 else { fail("Invalid VM metadata ownership or size") }
    return file.readDataToEndOfFile()
}
func acquireHostLock() {
    let root = FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent(".local/state/chickadee-macos")
    safeDirectory(root, create: true)
    hostLock = open(root.appendingPathComponent("vm.lock").path, O_RDWR | O_CREAT | O_NOFOLLOW | O_CLOEXEC, 0o600)
    var info = stat()
    guard hostLock >= 0, fstat(hostLock, &info) == 0, info.st_uid == geteuid(),
          info.st_mode & S_IFMT == S_IFREG, info.st_mode & 0o077 == 0,
          flock(hostLock, LOCK_EX | LOCK_NB) == 0 else { fail("Another prototype VM is active; single-VM lock retained") }
}
func configure(_ root: URL) -> VZVirtualMachineConfiguration {
    guard let model = VZMacHardwareModel(dataRepresentation: readPrivate(root.appendingPathComponent("hardware-model"))), model.isSupported,
          let identifier = VZMacMachineIdentifier(dataRepresentation: readPrivate(root.appendingPathComponent("machine-id"))) else { fail("Unsupported VM identity") }
    let platform = VZMacPlatformConfiguration()
    platform.hardwareModel = model
    platform.machineIdentifier = identifier
    platform.auxiliaryStorage = VZMacAuxiliaryStorage(url: root.appendingPathComponent("auxiliary-storage"))
    let config = VZVirtualMachineConfiguration()
    config.platform = platform
    config.bootLoader = VZMacOSBootLoader()
    config.cpuCount = cpuCount
    config.memorySize = memoryBytes
    let graphics = VZMacGraphicsDeviceConfiguration()
    graphics.displays = [VZMacGraphicsDisplayConfiguration(widthInPixels: 1024, heightInPixels: 768, pixelsPerInch: 80)]
    config.graphicsDevices = [graphics]
    config.keyboards = [VZUSBKeyboardConfiguration()]
    config.pointingDevices = [VZUSBScreenCoordinatePointingDeviceConfiguration()]
    // Deliberately no NIC, shared directories, host sockets, serial or credentials.
    config.networkDevices = []
    config.directorySharingDevices = []
    if let handle = networkHandle {
        let device = VZVirtioNetworkDeviceConfiguration()
        device.macAddress = VZMACAddress(string: "02:cc:aa:00:00:02")!
        let attachment = VZFileHandleNetworkDeviceAttachment(fileHandle: handle)
        attachment.maximumTransmissionUnit = 1500
        device.attachment = attachment
        config.networkDevices = [device]
    }
    do {
        var diskInfo=stat()
        let diskPath=root.appendingPathComponent("disk.raw").path
        guard lstat(diskPath,&diskInfo)==0,diskInfo.st_mode & S_IFMT == S_IFREG,diskInfo.st_uid==geteuid(),diskInfo.st_mode & 0o077==0,diskInfo.st_size==diskBytes else {fail("Invalid private guest disk")}
        let disk = try VZDiskImageStorageDeviceAttachment(url: root.appendingPathComponent("disk.raw"), readOnly: false)
        config.storageDevices = [VZVirtioBlockDeviceConfiguration(attachment: disk)]
        let controlURL = root.appendingPathComponent("build-control.raw")
        if FileManager.default.fileExists(atPath: controlURL.path) {
            var controlInfo = stat()
            guard lstat(controlURL.path, &controlInfo) == 0,
                  controlInfo.st_mode & S_IFMT == S_IFREG, controlInfo.st_uid == geteuid(),
                  controlInfo.st_mode & 0o077 == 0, controlInfo.st_size == 1024 * 1024 else { fail("Invalid private build control disk") }
            let attachment = try VZDiskImageStorageDeviceAttachment(url: controlURL, readOnly: false)
            let device = VZVirtioBlockDeviceConfiguration(attachment: attachment)
            device.blockDeviceIdentifier = "CHICKADEE_BUILD"
            config.storageDevices.append(device)
        }
        let runnerURL = root.appendingPathComponent("runner-control.raw")
        if FileManager.default.fileExists(atPath: runnerURL.path) {
            var info = stat()
            guard lstat(runnerURL.path, &info) == 0, info.st_mode & S_IFMT == S_IFREG,
                  info.st_uid == geteuid(), info.st_mode & 0o077 == 0, info.st_size == 1024 * 1024 else { fail("Invalid runner control disk") }
            let attachment = try VZDiskImageStorageDeviceAttachment(url: runnerURL, readOnly: false, cachingMode: .uncached, synchronizationMode: .full)
            let device = VZVirtioBlockDeviceConfiguration(attachment: attachment)
            device.blockDeviceIdentifier = "CHICKADEE_RUNNER"
            config.storageDevices.append(device)
        }
        try config.validate()
    } catch { fail("Native VM configuration validation failed") }
    return config
}
class GuestDelegate: NSObject, VZVirtualMachineDelegate {
    func guestDidStop(_ virtualMachine: VZVirtualMachine) {
        finishStopped()
    }
    func virtualMachine(_ virtualMachine: VZVirtualMachine, didStopWithError error: Error) {
        fail("VM_STOPPED_WITH_ERROR")
    }
}
func handleSignals() {
    for number in [SIGTERM, SIGINT] {
        signal(number, SIG_IGN)
        let source = DispatchSource.makeSignalSource(signal: number, queue: .main)
        source.setEventHandler {
            requestStop()
        }
        source.resume()
        signalSources.append(source)
    }
}
if operation == "restore-info" {
    guard arguments.count == 1 else { fail("Unexpected arguments") }
    VZMacOSRestoreImage.fetchLatestSupported { result in
        guard case .success(let image) = result, let requirements = image.mostFeaturefulSupportedConfiguration else { fail("Supported restore metadata unavailable") }
        let version = image.operatingSystemVersion
        let info: [String: Any] = ["schema_version": 1, "os_version": "\(version.majorVersion).\(version.minorVersion).\(version.patchVersion)", "build": image.buildVersion,
          "url": image.url.absoluteString, "minimum_cpus": requirements.minimumSupportedCPUCount, "minimum_memory_bytes": requirements.minimumSupportedMemorySize]
        guard let data = try? JSONSerialization.data(withJSONObject: info, options: [.sortedKeys]), let text = String(data: data, encoding: .utf8) else { fail("Restore metadata encoding failed") }
        print(text)
        exit(0)
    }
    DispatchQueue.main.asyncAfter(deadline: .now() + 30) { fail("Restore metadata timed out") }
} else if operation == "install" {
    guard arguments.count == 3 else { fail("Usage: native-vm install IPSW STATE") }
    acquireHostLock()
    let ipsw = URL(fileURLWithPath: arguments[1])
    let root = URL(fileURLWithPath: arguments[2], isDirectory: true)
    guard !FileManager.default.fileExists(atPath: root.path) else { fail("Installation requires a new state directory") }
    safeDirectory(root, create: true)
    guard let attributes=try? FileManager.default.attributesOfFileSystem(forPath:root.path),let free=attributes[.systemFreeSize] as? NSNumber,free.uint64Value>=UInt64(diskBytes)+20*1024*1024*1024 else {fail("Insufficient full-disk growth reserve")}
    VZMacOSRestoreImage.load(from: ipsw) { result in
      DispatchQueue.main.async {
        guard case .success(let image) = result, let requirements = image.mostFeaturefulSupportedConfiguration,
              requirements.hardwareModel.isSupported, requirements.minimumSupportedCPUCount <= cpuCount,
              requirements.minimumSupportedMemorySize <= memoryBytes else { fail("Restore image exceeds the small profile or is unsupported") }
        writePrivate(requirements.hardwareModel.dataRepresentation, root.appendingPathComponent("hardware-model"))
        writePrivate(VZMacMachineIdentifier().dataRepresentation, root.appendingPathComponent("machine-id"))
        do { _ = try VZMacAuxiliaryStorage(creatingStorageAt: root.appendingPathComponent("auxiliary-storage"), hardwareModel: requirements.hardwareModel, options: []) }
        catch { fail("Auxiliary storage creation failed") }
        let fd = open(root.appendingPathComponent("disk.raw").path, O_RDWR | O_CREAT | O_EXCL | O_NOFOLLOW | O_CLOEXEC, 0o600)
        guard fd >= 0, ftruncate(fd, diskBytes) == 0, fsync(fd) == 0 else { fail("Sparse guest disk allocation failed") }
        close(fd)
        machine = VZVirtualMachine(configuration: configure(root))
        handleSignals()
        installer = VZMacOSInstaller(virtualMachine: machine!, restoringFromImageAt: ipsw)
        print("INSTALLING_OFFLINE_GUEST")
        installer!.install { result in
          DispatchQueue.main.async {
            if case .failure(let error)=result {
                var current: NSError?=error as NSError
                for _ in 0..<5 { guard let e=current else{break};FileHandle.standardError.write(Data("install_error_domain=\(e.domain) code=\(e.code) reason=\(e.localizedDescription.prefix(512))\n".utf8));current=e.userInfo[NSUnderlyingErrorKey] as? NSError }
                fail("macOS installation failed; retain state for diagnostics")
            }
            guard let vm=machine else {fail("Missing installed VM handle")}
            if vm.state == .stopped {print("INSTALL_COMPLETE");exit(0)}
            vm.stop { error in
                guard error == nil, vm.state == .stopped else {fail("Installed VM stop unconfirmed; retain state")}
                print("INSTALL_COMPLETE");exit(0)
            }
          }
        }
      }
    }
} else if operation == "clone" {
    guard arguments.count == 3 else { fail("Usage: native-vm clone BASE STATE") }
    acquireHostLock()
    let base = URL(fileURLWithPath: arguments[1], isDirectory: true)
    let root = URL(fileURLWithPath: arguments[2], isDirectory: true)
    safeDirectory(base, create: false)
    guard !FileManager.default.fileExists(atPath: base.appendingPathComponent("credential-intent.json").path),
          !FileManager.default.fileExists(atPath: root.path) else { fail("Clone requires a credential-free base and new state") }
    safeDirectory(root, create: true)
    guard let attributes = try? FileManager.default.attributesOfFileSystem(forPath: root.path),
          let free = attributes[.systemFreeSize] as? NSNumber,
          free.uint64Value >= UInt64(diskBytes) + 20 * 1024 * 1024 * 1024 else { fail("Insufficient full-disk growth reserve") }
    for name in ["disk.raw", "auxiliary-storage"] {
        let src = base.appendingPathComponent(name)
        let dst = root.appendingPathComponent(name)
        var info = stat()
        guard lstat(src.path, &info) == 0, info.st_mode & S_IFMT == S_IFREG,
              info.st_uid == geteuid(), info.st_mode & 0o077 == 0,
              clonefile(src.path, dst.path, 0) == 0, chmod(dst.path, 0o600) == 0 else { fail("Private APFS image clone failed; retain state") }
    }
    writePrivate(readPrivate(base.appendingPathComponent("hardware-model")), root.appendingPathComponent("hardware-model"))
    writePrivate(VZMacMachineIdentifier().dataRepresentation, root.appendingPathComponent("machine-id"))
    print("VM_CLONED")
    exit(0)
} else if operation == "boot" {
    guard arguments.count >= 2, arguments.count <= 6, arguments.count % 2 == 0 else { fail("Invalid boot arguments") }
    var index = 2
    while index < arguments.count {
        guard let fd = Int32(arguments[index+1]), fd >= 3 else { fail("Invalid inherited descriptor") }
        if arguments[index] == "--network-fd" && networkHandle == nil {
            var kind: Int32 = 0
            var length = socklen_t(MemoryLayout<Int32>.size)
            guard getsockopt(fd, SOL_SOCKET, SO_TYPE, &kind, &length) == 0, kind == SOCK_DGRAM else { fail("Network descriptor must be a connected datagram socket") }
            networkHandle = FileHandle(fileDescriptor: fd, closeOnDealloc: true)
        } else if arguments[index] == "--lifetime-fd" && lifetimeFD < 0 {
            var info = stat()
            guard fstat(fd, &info) == 0, info.st_mode & S_IFMT == S_IFIFO else { fail("Lifetime descriptor must be a pipe") }
            lifetimeFD = fd
        } else { fail("Unknown or duplicate boot descriptor") }
        index += 2
    }
    acquireHostLock()
    let root = URL(fileURLWithPath: arguments[1], isDirectory: true)
    safeDirectory(root, create: false)
    activeRoot = root
    machine = VZVirtualMachine(configuration: configure(root))
    guestDelegate = GuestDelegate()
    machine!.delegate = guestDelegate
    handleSignals()
    machine!.start { result in
        guard case .success = result else { fail("Native VM failed to boot") }
        print(networkHandle == nil ? "VM_RUNNING_OFFLINE" : "VM_RUNNING_FILTERED_NETWORK")
    }
    if lifetimeFD >= 0 {
        let source = DispatchSource.makeReadSource(fileDescriptor: lifetimeFD, queue: .main)
        source.setEventHandler {
            var byte: UInt8 = 0
            let count = read(lifetimeFD, &byte, 1)
            if count == 0 { source.cancel(); requestStop() }
        }
        source.resume()
        lifetimeSource = source
    }
} else { fail("Unknown operation") }
RunLoop.main.run()
