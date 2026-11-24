import Foundation
import Security

/// The Developer ID team is stable between official releases. An ad hoc binary only
/// has its own code-directory hash, so its local credential entry is per build.
enum KeychainNamespace {
    static func service(bundleID: String, teamID: String?, codeHash: Data?) -> String? {
        let base = bundleID + ".siwc"
        if let teamID, !teamID.isEmpty {
            return base + ".team." + teamID
        }
        let hex = Array("0123456789abcdef".utf8)
        if let codeHash, !codeHash.isEmpty {
            let hash = String(decoding: codeHash.flatMap { [hex[Int($0 >> 4)], hex[Int($0 & 15)]] }, as: UTF8.self)
            return base + ".beta." + hash
        }
        return nil
    }

    static func current(bundleID: String) -> String? {
        var running: SecCode?
        var executable: SecStaticCode?
        guard SecCodeCopySelf([], &running) == errSecSuccess, let running,
              SecCodeCopyStaticCode(running, [], &executable) == errSecSuccess, let executable else { return nil }
        return service(bundleID: bundleID, executable: executable)
    }

    static func service(bundleID: String, executable: SecStaticCode) -> String? {
        var attributes: CFDictionary?
        let flags = SecCSFlags(rawValue: UInt32(kSecCSSigningInformation))
        guard SecCodeCopySigningInformation(executable, flags, &attributes) == errSecSuccess,
              let details = attributes as? [String: Any] else { return nil }
        return service(bundleID: bundleID,
                       teamID: details[kSecCodeInfoTeamIdentifier as String] as? String,
                       codeHash: details[kSecCodeInfoUnique as String] as? Data)
    }
}
