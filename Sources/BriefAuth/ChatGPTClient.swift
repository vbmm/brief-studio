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

public enum ChatGPTError: LocalizedError, Equatable {
    case busy, signedOut, planNotGranted, authorizationDeclined, authorizationTimedOut
    case invalidAuthorization, invalidIdentity, localListener, browserUnavailable
    case sessionExpired, revocationUnconfirmed, keychain(OSStatus), provider(Int)
    case usageLimit, modelUnavailable, incompleteResponse, emptyResponse

    public var errorDescription: String? {
        switch self {
        case .busy: "Une action ChatGPT est déjà en cours."
        case .signedOut: "Connectez-vous à ChatGPT pour utiliser cette fonction."
        case .planNotGranted: "Ce compte n’a pas autorisé l’utilisation de son forfait ChatGPT."
        case .authorizationDeclined: "La connexion ChatGPT a été refusée."
        case .authorizationTimedOut: "La demande de connexion a expiré."
        case .invalidAuthorization: "La réponse de connexion ChatGPT est invalide."
        case .invalidIdentity: "Impossible de vérifier l’identité du compte ChatGPT."
        case .localListener: "Impossible de recevoir la réponse du navigateur sur ce Mac."
        case .browserUnavailable: "Impossible d’ouvrir le navigateur par défaut."
        case .sessionExpired: "La session ChatGPT a expiré. Reconnectez-vous."
        case .revocationUnconfirmed:
            "Connexion effacée sur ce Mac. La révocation n’a pas été confirmée par OpenAI. Vous pouvez déconnecter l’app dans ChatGPT."
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
    private var busy = false

    public init(appName: String = "Brief Studio") {
        self.appName = appName
        keychain = Keychain(service: (Bundle.main.bundleIdentifier ?? "com.briefmac.local") + ".siwc")
        let configuration = URLSessionConfiguration.ephemeral
        configuration.httpShouldSetCookies = false
        configuration.urlCache = nil
        configuration.requestCachePolicy = .reloadIgnoringLocalCacheData
        configuration.timeoutIntervalForRequest = 75
        configuration.timeoutIntervalForResource = 600
        session = URLSession(configuration: configuration, delegate: NoRedirects(), delegateQueue: nil)
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
    public func signIn() async throws -> ChatGPTProfile {
        try await authorize(clientID: readState().selected, createNew: false)
    }

    /// Prompts for a separate account or workspace registration.
    public func addAccount() async throws -> ChatGPTProfile {
        try await authorize(clientID: nil, createNew: true)
    }

    /// Selects an existing registration and reauthorizes it if needed.
    public func useProfile(id: String) async throws -> ChatGPTProfile {
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
        return try await authorize(clientID: saved.clientID, createNew: false)
    }

    private func authorize(clientID: String?, createNew: Bool) async throws -> ChatGPTProfile {
        try begin()
        defer { busy = false }

        var state = try readState()
        // Persist a pseudonymous host ID even if this first attempt is cancelled.
        try keychain.save(state)
        let old = createNew ? nil : state.accounts.first(where: { $0.clientID == clientID })
        let draft = try OAuthProtocol.Attempt(port: 0)
        let listener = try Loopback(state: draft.state)
        defer { listener.cancel() }
        let port = try await listener.start()
        let attempt = OAuthProtocol.Attempt(port: port, state: draft.state, nonce: draft.nonce, verifier: draft.verifier)
        let browserURL = try attempt.authorizationURL(appName: appName, hostID: state.hostID,
                                                       clientID: old?.clientID, identityHint: old?.tokens?.idToken)
        try Task.checkCancellation()
        let opened = await MainActor.run { NSWorkspace.shared.open(browserURL) }
        guard opened else { throw ChatGPTError.browserUnavailable }
        let callback = try await listener.callback()
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
                        let (_, response) = try await session.data(for: request)
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
        var request = URLRequest(url: url)
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        if let bearer { request.setValue("Bearer " + bearer, forHTTPHeaderField: "Authorization") }
        let (data, response) = try await session.data(for: request)
        try Self.checkHTTP(response, body: data)
        return try JSONDecoder().decode(T.self, from: data)
    }

    private func formPost<T: Decodable>(_ url: URL, _ values: [(String, String)]) async throws -> T {
        let (data, response) = try await session.data(for: formRequest(url, values))
        if (response as? HTTPURLResponse)?.statusCode == 400,
           (try? JSONDecoder().decode(OAuthFailure.self, from: data).error) == "invalid_grant" {
            throw ChatGPTError.sessionExpired
        }
        try Self.checkHTTP(response, body: data)
        return try JSONDecoder().decode(T.self, from: data)
    }

    private func formRequest(_ url: URL, _ values: [(String, String)], timeout: TimeInterval = 30) -> URLRequest {
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
    let service: String

    private var query: [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: "local-accounts-v1",
            kSecAttrSynchronizable as String: false
        ]
    }

    func load() throws -> SessionState? {
        var options = query
        options[kSecReturnData as String] = true
        options[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: CFTypeRef?
        let status = SecItemCopyMatching(options as CFDictionary, &result)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = result as? Data else { throw ChatGPTError.keychain(status) }
        return try JSONDecoder().decode(SessionState.self, from: data)
    }

    func save(_ state: SessionState) throws {
        let data = try JSONEncoder().encode(state)
        let changes = [kSecValueData as String: data]
        var status = SecItemUpdate(query as CFDictionary, changes as CFDictionary)
        if status == errSecItemNotFound {
            var attributes = query
            attributes[kSecValueData as String] = data
            status = SecItemAdd(attributes as CFDictionary, nil)
        }
        guard status == errSecSuccess else { throw ChatGPTError.keychain(status) }
    }
}
