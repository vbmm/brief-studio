import AppKit
import Foundation
import Security

public struct ChatGPTProfile: Codable, Sendable, Identifiable {
    public let id: String
    public let name: String?
    public let email: String?
    public let canUsePlan: Bool
}

public struct ChatGPTModel: Sendable, Identifiable {
    public let id: String
    public let displayName: String
}

public enum ChatGPTSignInStage: Sendable {
    case starting
    case waitingForBrowser(URL)
    case browserOpenFailed
    case verifying
}

public typealias ChatGPTSignInProgress = @Sendable (ChatGPTSignInStage) async -> Void

public enum ChatGPTError: LocalizedError, Equatable {
    case busy, signedOut, planNotGranted, authorizationDeclined, authorizationTimedOut
    case invalidAuthorization, invalidIdentity, localListener, providerTimedOut
    case sessionExpired, revocationUnconfirmed, keychain(OSStatus), provider(Int)
    case usageLimit, modelUnavailable, incompleteResponse, emptyResponse

    public var errorDescription: String? {
        switch self {
        case .busy: "Une action ChatGPT est déjà en cours."
        case .signedOut: "Connectez-vous à ChatGPT pour utiliser cette fonction."
        case .planNotGranted: "Ce compte n’a pas autorisé l’utilisation de son forfait ChatGPT."
        case .authorizationDeclined: "La connexion ChatGPT a été refusée."
        case .authorizationTimedOut:
            "Le navigateur n’a pas renvoyé l’autorisation. Recommencez et terminez la connexion dans le navigateur."
        case .invalidAuthorization: "La réponse de connexion ChatGPT est invalide."
        case .invalidIdentity: "Impossible de vérifier l’identité du compte ChatGPT."
        case .localListener: "Impossible de recevoir la réponse du navigateur sur ce Mac."
        case .providerTimedOut: "OpenAI n’a pas répondu à temps. Vérifiez votre connexion puis réessayez."
        case .sessionExpired: "La session ChatGPT a expiré. Reconnectez-vous."
        case .revocationUnconfirmed:
            "Connexion effacée sur ce Mac. La révocation n’a pas été confirmée par OpenAI. Vous pouvez déconnecter l’app dans ChatGPT."
        case let .keychain(status) where status == errSecInteractionNotAllowed || status == errSecAuthFailed:
            "Le Trousseau macOS bloque l’accès à la session. Déverrouillez le trousseau de session puis réessayez."
        case .keychain: "Le Trousseau macOS n’a pas pu enregistrer ou lire la connexion."
        case let .provider(status): "OpenAI a renvoyé une erreur (HTTP \(status))."
        case .usageLimit: "Le forfait de ce compte est indisponible ou sa limite d’utilisation est atteinte."
        case .modelUnavailable: "Ce modèle est indisponible pour le compte sélectionné."
        case .incompleteResponse: "La réponse de ChatGPT est incomplète."
        case .emptyResponse: "ChatGPT n’a renvoyé aucun texte."
        }
    }
}

/// The sole session owner for the app. Use one instance per running app.
public actor ChatGPTClient {
    public static let shared = ChatGPTClient()

    private let appName: String
    private let keychain: Keychain
    private let session: URLSession
    private let accountSession: URLSession
    private var busy = false

    public init(appName: String = "Brief Studio") {
        self.appName = appName
        let bundleID = Bundle.main.bundleIdentifier ?? "com.briefmac.local"
        keychain = Keychain(service: KeychainNamespace.current(bundleID: bundleID))
        session = Self.makeSession(requestTimeout: 75, resourceTimeout: 600)
        accountSession = Self.makeSession(requestTimeout: 15, resourceTimeout: 25)
    }

    private static func makeSession(requestTimeout: TimeInterval, resourceTimeout: TimeInterval) -> URLSession {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.httpShouldSetCookies = false
        configuration.urlCache = nil
        configuration.requestCachePolicy = .reloadIgnoringLocalCacheData
        configuration.waitsForConnectivity = false
        configuration.timeoutIntervalForRequest = requestTimeout
        configuration.timeoutIntervalForResource = resourceTimeout
        return URLSession(configuration: configuration, delegate: NoRedirects(), delegateQueue: nil)
    }

    public func signedInProfile() async throws -> ChatGPTProfile? {
        let record = try readState().active
        return record?.tokens == nil ? nil : record?.profile
    }

    /// Includes previous registrations so signing out does not discard their client identity.
    public func savedProfiles() async throws -> [ChatGPTProfile] {
        try readState().accounts.map(\.profile)
    }

    /// Signs in with the current or most recent registration if there is one.
    public func signIn(progress: @escaping ChatGPTSignInProgress = { _ in }) async throws -> ChatGPTProfile {
        try await authorize(selection: .current, progress: progress)
    }

    /// Prompts for a separate account or workspace registration.
    public func addAccount(progress: @escaping ChatGPTSignInProgress = { _ in }) async throws -> ChatGPTProfile {
        try await authorize(selection: .new, progress: progress)
    }

    /// Selects an existing registration and reauthorizes it if needed.
    public func useProfile(id: String, progress: @escaping ChatGPTSignInProgress = { _ in }) async throws -> ChatGPTProfile {
        try begin()
        let saved: Account
        do {
            var state = try readState()
            guard let record = state.accounts.first(where: { $0.clientID == id }) else {
                throw ChatGPTError.signedOut
            }
            if record.tokens != nil {
                state.selected = id
                try keychain.save(state)
                busy = false
                return record.profile
            }
            saved = record
        } catch {
            busy = false
            throw error
        }
        busy = false
        return try await authorize(selection: .saved(saved.clientID), progress: progress)
    }

    private enum SignInSelection {
        case current, new, saved(String)
    }

    private func authorize(selection: SignInSelection, progress: @escaping ChatGPTSignInProgress) async throws -> ChatGPTProfile {
        try begin()
        defer { busy = false }

        await progress(.starting)
        try Task.checkCancellation()
        var state = try keychain.load() ?? SessionState(hostID: Self.localHostID())
        let clientID: String? = switch selection {
        case .current: state.selected
        case .new: nil
        case let .saved(id): id
        }
        let old = state.accounts.first(where: { $0.clientID == clientID })
        let draft = try OAuthProtocol.Attempt(port: 0)
        let listener = try Loopback(state: draft.state)
        defer { listener.cancel() }
        let port = try await listener.start()
        let attempt = OAuthProtocol.Attempt(port: port, state: draft.state, nonce: draft.nonce, verifier: draft.verifier)
        // The URL can be reopened or copied by the user. Omit credential/identity hints.
        let browserURL = try attempt.authorizationURL(appName: appName, hostID: state.hostID,
                                                       clientID: old?.clientID, identityHint: nil)
        await progress(.waitingForBrowser(browserURL))
        try Task.checkCancellation()
        let launch = Task { @MainActor in
            guard !Task.isCancelled else { return }
            do {
                try await ChatGPTBrowser.open(browserURL)
            } catch {
                // Keep the listener available for the explicit retry or copy action.
                if !Task.isCancelled { await progress(.browserOpenFailed) }
            }
        }
        defer { launch.cancel() }
        let callback = try await listener.callback()
        try Task.checkCancellation()
        await progress(.verifying)
        try Task.checkCancellation()
        let issuedID = try callback.resolveClientID(existing: old?.clientID)
        let reply: TokenReply = try await formPost(OAuthProtocol.token, [
            ("grant_type", "authorization_code"), ("client_id", issuedID), ("code", callback.code),
            ("code_verifier", attempt.verifier), ("redirect_uri", attempt.redirect), ("resource", OAuthProtocol.resource)
        ])
        let keys: VerifiedIdentity.KeySet = try await get(OAuthProtocol.jwks)
        guard let idToken = reply.id_token else { throw ChatGPTError.invalidIdentity }
        let identity = try VerifiedIdentity.validate(idToken, keys: keys, clientID: issuedID, nonce: attempt.nonce)
        let known = state.accounts.first(where: { $0.clientID == issuedID })
        guard known == nil || known?.subject == identity.subject else { throw ChatGPTError.invalidIdentity }
        let record = Account(clientID: issuedID, subject: identity.subject, name: identity.name,
                             email: identity.email, tokens: try reply.tokens())
        try Task.checkCancellation()
        state.replace(record)
        state.selected = record.clientID
        try keychain.save(state)
        return record.profile
    }

    public func signOut() async throws {
        try begin()
        defer { busy = false }
        var state = try readState()
        guard var record = state.active, let tokens = record.tokens else { return }
        var revoked = false
        if let refreshToken = tokens.refreshToken {
            do {
                let discovery: Discovery = try await get(OAuthProtocol.discovery)
                guard discovery.issuer == OAuthProtocol.issuer,
                      discovery.revocation_endpoint == OAuthProtocol.issuer + "/api/accounts/oauth/revoke",
                      let url = URL(string: discovery.revocation_endpoint) else {
                    throw ChatGPTError.invalidAuthorization
                }
                let request = formRequest(url, [
                    ("token", refreshToken), ("token_type_hint", "refresh_token"), ("client_id", record.clientID)
                ], timeout: 10)
                for attempt in 0..<2 {
                    do {
                        let (_, response) = try await accountSession.data(for: request)
                        let status = (response as? HTTPURLResponse)?.statusCode
                        if status == 200 { revoked = true; break }
                        guard let status, (500..<600).contains(status) else { break }
                    } catch is CancellationError {
                        break
                    } catch {
                        // One bounded retry for a transport error.
                    }
                    if attempt == 0 { try? await Task.sleep(for: .milliseconds(300)) }
                }
            } catch {
                // Clearing this Mac is still possible after a provider or network error.
            }
        } else {
            revoked = true
        }
        record.tokens = nil
        state.replace(record)
        try keychain.save(state)
        if !revoked { throw ChatGPTError.revocationUnconfirmed }
    }

    public func models() async throws -> [ChatGPTModel] {
        try begin()
        defer { busy = false }
        let tokens = try await authorizedTokens()
        let catalog: ModelCatalog = try await get(URL(string: OAuthProtocol.resource + "/models")!, bearer: tokens.accessToken)
        return catalog.models.compactMap { entry in
            guard entry.visibility == "list", let id = entry.slug, !id.isEmpty else { return nil }
            return ChatGPTModel(id: id, displayName: entry.display_name ?? id)
        }
    }

    public func respond(instructions: String, input: String, model: String) async throws -> String {
        try begin()
        defer { busy = false }
        let tokens = try await authorizedTokens()
        guard !model.isEmpty else { throw ChatGPTError.modelUnavailable }
        try Task.checkCancellation()
        let payload = InferenceRequest(model: model, instructions: instructions,
                                       input: [.init(role: "user", content: input)])
        var request = URLRequest(url: URL(string: OAuthProtocol.resource + "/responses")!)
        request.httpMethod = "POST"
        request.setValue("Bearer " + tokens.accessToken, forHTTPHeaderField: "Authorization")
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("text/event-stream", forHTTPHeaderField: "Accept")
        request.httpBody = try JSONEncoder().encode(payload)
        let (bytes, response) = try await session.bytes(for: request)
        defer { bytes.task.cancel() }
        try Self.checkHTTP(response)
        var stream = ResponsesStream()
        for try await byte in bytes {
            try Task.checkCancellation()
            if let result = try stream.accept(byte: byte) { return result }
        }
        throw ChatGPTError.incompleteResponse
    }

    private func begin() throws {
        guard !busy else { throw ChatGPTError.busy }
        busy = true
    }

    private func readState() throws -> SessionState {
        try keychain.load() ?? SessionState()
    }

    /// The identifier describes this install and is never a credential.
    private static func localHostID() -> String {
        let preferences = UserDefaults.standard
        let key = "BriefAuthHostUUID"
        if let saved = preferences.string(forKey: key), let uuid = UUID(uuidString: saved) {
            return "urn:uuid:" + uuid.uuidString.lowercased()
        }
        let fresh = UUID().uuidString.lowercased()
        preferences.set(fresh, forKey: key)
        return "urn:uuid:" + fresh
    }

    private func authorizedTokens() async throws -> SessionTokens {
        var state = try readState()
        guard var account = state.active, let tokens = account.tokens else { throw ChatGPTError.signedOut }
        guard tokens.canUsePlan else { throw ChatGPTError.planNotGranted }
        if tokens.expiresAt.timeIntervalSinceNow > 45 { return tokens }
        guard let refresh = tokens.refreshToken else { throw ChatGPTError.sessionExpired }
        let reply: TokenReply
        do {
            reply = try await formPost(OAuthProtocol.token, [
                ("grant_type", "refresh_token"), ("client_id", account.clientID), ("refresh_token", refresh),
                ("resource", OAuthProtocol.resource)
            ])
        } catch ChatGPTError.sessionExpired {
            account.tokens = nil
            state.replace(account)
            try keychain.save(state)
            throw ChatGPTError.sessionExpired
        }
        // The grant must replace its rotating refresh token. Keep the previously verified
        // identity and hint: a refresh response does not run a fresh nonce exchange.
        let rotated = try reply.tokens(previous: tokens)
        account.tokens = rotated
        state.replace(account)
        try keychain.save(state)
        guard rotated.canUsePlan else { throw ChatGPTError.planNotGranted }
        return rotated
    }

    private func get<T: Decodable>(_ url: URL, bearer: String? = nil) async throws -> T {
        var request = URLRequest(url: url, timeoutInterval: 15)
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        if let bearer { request.setValue("Bearer " + bearer, forHTTPHeaderField: "Authorization") }
        let (data, response) = try await accountData(for: request)
        try Self.checkHTTP(response, body: data)
        return try JSONDecoder().decode(T.self, from: data)
    }

    private func formPost<T: Decodable>(_ url: URL, _ values: [(String, String)]) async throws -> T {
        let (data, response) = try await accountData(for: formRequest(url, values))
        if (response as? HTTPURLResponse)?.statusCode == 400,
           (try? JSONDecoder().decode(OAuthFailure.self, from: data).error) == "invalid_grant" {
            throw ChatGPTError.sessionExpired
        }
        try Self.checkHTTP(response, body: data)
        return try JSONDecoder().decode(T.self, from: data)
    }

    private func accountData(for request: URLRequest) async throws -> (Data, URLResponse) {
        do {
            return try await accountSession.data(for: request)
        } catch let error as URLError where error.code == .timedOut {
            throw ChatGPTError.providerTimedOut
        }
    }

    private func formRequest(_ url: URL, _ values: [(String, String)], timeout: TimeInterval = 15) -> URLRequest {
        var request = URLRequest(url: url, timeoutInterval: timeout)
        request.httpMethod = "POST"
        request.setValue("application/x-www-form-urlencoded", forHTTPHeaderField: "Content-Type")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        request.httpBody = Data(OAuthProtocol.form(values).utf8)
        return request
    }

    private static func checkHTTP(_ response: URLResponse, body: Data? = nil) throws {
        let status = (response as? HTTPURLResponse)?.statusCode ?? 0
        if (200...299).contains(status) { return }
        if status == 401 { throw ChatGPTError.sessionExpired }
        if status == 429 { throw ChatGPTError.usageLimit }
        if let body, let envelope = try? JSONDecoder().decode(ProviderFailure.self, from: body),
           let code = envelope.error.code, code.hasPrefix("subscription_sharing_usage_") {
            throw ChatGPTError.usageLimit
        }
        throw ChatGPTError.provider(status)
    }
}

private final class NoRedirects: NSObject, URLSessionTaskDelegate {
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest, completionHandler: @escaping @Sendable (URLRequest?) -> Void) {
        completionHandler(nil)
    }
}

private struct OAuthFailure: Decodable { let error: String }
private struct ProviderFailure: Decodable {
    struct Body: Decodable { let code: String? }
    let error: Body
}
private struct Discovery: Decodable {
    let issuer: String
    let revocation_endpoint: String
}
private struct ModelCatalog: Decodable {
    struct Entry: Decodable {
        let slug: String?
        let display_name: String?
        let visibility: String?
    }
    let models: [Entry]
}
struct InferenceRequest: Encodable {
    struct Message: Encodable {
        let role: String
        let content: String
    }
    let model: String
    let instructions: String
    let input: [Message]
    var stream = true
    var store = false
}

private struct TokenReply: Decodable {
    let access_token: String
    let refresh_token: String?
    let id_token: String?
    let token_type: String
    let expires_in: TimeInterval
    let scope: String?

    func tokens(previous: SessionTokens? = nil) throws -> SessionTokens {
        let scopes = scope?.split(separator: " ").map(String.init) ?? previous?.scopes ?? []
        guard token_type.lowercased() == "bearer", !access_token.isEmpty,
              expires_in.isFinite, (0...86_400).contains(expires_in), expires_in > 0,
              scope != nil || previous != nil,
              let hint = previous?.idToken ?? id_token, !hint.isEmpty,
              previous == nil || (refresh_token?.isEmpty == false),
              !scopes.contains("offline_access") || (refresh_token?.isEmpty == false) else {
            throw ChatGPTError.invalidAuthorization
        }
        return SessionTokens(accessToken: access_token, refreshToken: refresh_token, idToken: hint,
                             expiresAt: Date().addingTimeInterval(expires_in), scopes: scopes)
    }
}

private struct SessionTokens: Codable {
    let accessToken: String
    let refreshToken: String?
    let idToken: String
    let expiresAt: Date
    let scopes: [String]

    var canUsePlan: Bool { scopes.contains(OAuthProtocol.planScope) && scopes.contains("resource.invoke") }
}

private struct Account: Codable {
    let clientID: String
    let subject: String
    let name: String?
    let email: String?
    var tokens: SessionTokens?

    var profile: ChatGPTProfile {
        ChatGPTProfile(id: clientID, name: name, email: email, canUsePlan: tokens?.canUsePlan == true)
    }
}

private struct SessionState: Codable {
    var hostID = "urn:uuid:" + UUID().uuidString.lowercased()
    var selected: String?
    var accounts: [Account] = []

    var active: Account? { accounts.first { $0.clientID == selected } }
    mutating func replace(_ account: Account) {
        if let position = accounts.firstIndex(where: { $0.clientID == account.clientID }) {
            accounts[position] = account
        } else {
            accounts.append(account)
        }
    }
}

private struct Keychain {
    let service: String?
    private static let interactionLock = NSLock()

    private func query() throws -> [String: Any] {
        guard let service else { throw ChatGPTError.keychain(errSecNotAvailable) }
        return [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: "local-accounts-v1",
            kSecAttrSynchronizable as String: false
        ]
    }

    func load() throws -> SessionState? {
        var options = try query()
        options[kSecReturnData as String] = true
        options[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: CFTypeRef?
        let status = try Self.withoutInteraction { SecItemCopyMatching(options as CFDictionary, &result) }
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = result as? Data else { throw ChatGPTError.keychain(status) }
        return try JSONDecoder().decode(SessionState.self, from: data)
    }

    func save(_ state: SessionState) throws {
        let data = try JSONEncoder().encode(state)
        let query = try query()
        let changes = [kSecValueData as String: data]
        let status = try Self.withoutInteraction {
            let updated = SecItemUpdate(query as CFDictionary, changes as CFDictionary)
            guard updated == errSecItemNotFound else { return updated }
            var attributes = query
            attributes[kSecValueData as String] = data
            return SecItemAdd(attributes as CFDictionary, nil)
        }
        guard status == errSecSuccess else { throw ChatGPTError.keychain(status) }
    }

    /// The file-based Keychain honours the legacy process UI policy for ACL
    /// prompts. Change it only while a local storage operation is in progress.
    private static func withoutInteraction<T>(_ body: () -> T) throws -> T {
        interactionLock.lock()
        defer { interactionLock.unlock() }
        var original = DarwinBoolean(false)
        let read = SecKeychainGetUserInteractionAllowed(&original)
        guard read == errSecSuccess else { throw ChatGPTError.keychain(read) }
        if original.boolValue {
            let disabled = SecKeychainSetUserInteractionAllowed(false)
            guard disabled == errSecSuccess else { throw ChatGPTError.keychain(disabled) }
        }
        defer {
            if original.boolValue { SecKeychainSetUserInteractionAllowed(true) }
        }
        return body()
    }
}
