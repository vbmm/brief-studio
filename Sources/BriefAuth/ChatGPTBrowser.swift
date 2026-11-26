import AppKit
import Foundation

/// Hands the OpenAI request to a normal system browser, outside Brief Studio.
@MainActor
public enum ChatGPTBrowser {
    private static let chromeID = "com.google.Chrome"

    public static var actionLabel: String {
        chromeURL == nil ? "Ouvrir dans le navigateur" : "Ouvrir dans Chrome"
    }

    public static func open(_ url: URL) async throws {
        guard isAuthorizationURL(url) else { throw ChatGPTError.invalidAuthorization }
        let workspace = NSWorkspace.shared
        let configuration = NSWorkspace.OpenConfiguration()
        configuration.activates = true
        configuration.addsToRecentItems = false
        if let chromeURL {
            _ = try await workspace.open([url], withApplicationAt: chromeURL, configuration: configuration)
        } else {
            _ = try await workspace.open(url, configuration: configuration)
        }
    }

    nonisolated static func isAuthorizationURL(_ url: URL) -> Bool {
        url.scheme == "https" && url.host == "auth.openai.com" && url.port == nil
            && url.user == nil && url.password == nil && url.fragment == nil
            && url.path == OAuthProtocol.authorization.path
    }

    private static var chromeURL: URL? {
        if let registered = NSWorkspace.shared.urlForApplication(withBundleIdentifier: chromeID) {
            return registered
        }
        let applications = FileManager.default.urls(for: .applicationDirectory, in: [.localDomainMask, .userDomainMask])
        return applications.lazy.map { $0.appendingPathComponent("Google Chrome.app", isDirectory: true) }
            .first { Bundle(url: $0)?.bundleIdentifier == chromeID }
    }
}
