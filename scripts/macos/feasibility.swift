import Foundation
import Virtualization
import Darwin

VZMacOSRestoreImage.fetchLatestSupported { result in
    switch result {
    case .failure(let error):
        let e = error as NSError
        print("restore_metadata=unavailable domain=\(e.domain) code=\(e.code)")
        exit(1)
    case .success(let image):
        guard let config = image.mostFeaturefulSupportedConfiguration else {
            print("supported_macos_configuration=unavailable")
            exit(1)
        }
        let version = image.operatingSystemVersion
        print("supported_restore_version=\(version.majorVersion).\(version.minorVersion).\(version.patchVersion)")
        print("minimum_macos_vcpus=\(config.minimumSupportedCPUCount)")
        print("minimum_macos_memory_bytes=\(config.minimumSupportedMemorySize)")
        print("hardware_model_supported=\(config.hardwareModel.isSupported)")
        exit(0)
    }
}
DispatchQueue.main.asyncAfter(deadline: .now() + 30) {
    print("restore_metadata=timeout")
    exit(1)
}
RunLoop.main.run()
