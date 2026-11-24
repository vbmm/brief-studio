import CryptoKit
import Foundation
import Security

enum OAuthProtocol {
    static let issuer = "https://auth.openai.com"
    static let authorization = URL(string: issuer + "/api/accounts/authorize")!
    static let token = URL(string: issuer + "/api/accounts/oauth/token")!
    static let jwks = URL(string: issuer + "/.well-known/jwks.json")!
    static let discovery = URL(string: issuer + "/.well-known/openid-configuration")!
    static let resource = "https://api.openai.com/v1"
    static let callbackPath = "/auth/callback"
    static let planScope = "chatgpt.tokens.use.direct"

    struct Attempt {
        let redirect: String
        let state: String
        let nonce: String
        let verifier: String

        init(port: UInt16) throws {
            self.init(port: port, state: try random(), nonce: try random(), verifier: try random())
        }

        init(port: UInt16, state: String, nonce: String, verifier: String) {
            self.redirect = "http://127.0.0.1:\(port)" + OAuthProtocol.callbackPath
            self.state = state
            self.nonce = nonce
            self.verifier = verifier
        }

        func authorizationURL(appName: String, hostID: String, clientID: String?, identityHint: String?) throws -> URL {
            var values: [(String, String)] = [
                ("client_id", clientID ?? "dynamic_agent_client"),
                ("ext_agent_host_id", hostID),
                ("response_type", "code"),
                ("redirect_uri", redirect),
                ("scope", "openid profile email offline_access resource.invoke " + planScope),
                ("resource", resource),
                ("state", state),
                ("nonce", nonce),
                ("code_challenge_method", "S256"),
                ("code_challenge", Data(SHA256.hash(data: Data(verifier.utf8))).base64URL)
            ]
            if clientID == nil {
                values.append(("agent_name_hint", appName))
            } else if let identityHint {
                values.append(("id_token_hint", identityHint))
            }
            var components = URLComponents(url: authorization, resolvingAgainstBaseURL: false)
            components?.percentEncodedQuery = form(values)
            guard let url = components?.url else { throw ChatGPTError.invalidAuthorization }
            return url
        }
    }

    struct Callback: Sendable {
        let code: String
        let clientID: String?

        /// A request with our state is terminal, even when the provider's payload is malformed.
        /// Anything else may be a browser probe or a prior attempt and leaves the listener open.
        static func matching(requestTarget: String, state: String) throws -> Callback? {
            guard requestTarget.utf8.count <= 8192,
                  requestTarget.hasPrefix(callbackPath + "?"),
                  let url = URLComponents(string: "http://127.0.0.1" + requestTarget),
                  url.percentEncodedPath == callbackPath, url.fragment == nil else {
                return nil
            }
            let items = url.queryItems ?? []
            let states = items.filter { $0.name == "state" }
            guard states.count == 1, states[0].value == state else { return nil }
            var parameters: [String: String] = [:]
            for item in items where ["code", "error", "client_id"].contains(item.name) {
                guard let value = item.value, !value.isEmpty,
                      parameters.updateValue(value, forKey: item.name) == nil else {
                    throw ChatGPTError.invalidAuthorization
                }
            }
            if let error = parameters["error"] {
                throw error == "access_denied" ? ChatGPTError.authorizationDeclined : ChatGPTError.provider(400)
            }
            guard let code = parameters["code"] else { throw ChatGPTError.invalidAuthorization }
            return Callback(code: code, clientID: parameters["client_id"])
        }

        func resolveClientID(existing: String?) throws -> String {
            if let existing {
                guard clientID == nil || clientID == existing else { throw ChatGPTError.invalidAuthorization }
                return existing
            }
            guard let clientID, clientID.hasPrefix("oaiapp_"), clientID.count > 7,
                  clientID.utf8.count <= 256,
                  clientID.utf8.allSatisfy({ asciiNameBytes.contains($0) || $0 == 95 || $0 == 45 }) else {
                throw ChatGPTError.invalidAuthorization
            }
            return clientID
        }
    }

    private static let asciiNameBytes = Set("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789".utf8)
    private static let urlBytes = CharacterSet(charactersIn: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._~")

    static func form(_ values: [(String, String)]) -> String {
        values.map { name, value in
            let key = name.addingPercentEncoding(withAllowedCharacters: urlBytes)!
            let escaped = value.addingPercentEncoding(withAllowedCharacters: urlBytes)!
            return "\(key)=\(escaped)"
        }.joined(separator: "&")
    }

    private static func random() throws -> String {
        var bytes = [UInt8](repeating: 0, count: 32)
        guard SecRandomCopyBytes(kSecRandomDefault, bytes.count, &bytes) == errSecSuccess else {
            throw ChatGPTError.invalidAuthorization
        }
        return Data(bytes).base64URL
    }
}

extension Data {
    fileprivate var base64URL: String {
        base64EncodedString().replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }

    fileprivate init?(base64URL: Substring) {
        guard !base64URL.isEmpty,
              base64URL.utf8.allSatisfy({ (65...90).contains($0) || (97...122).contains($0) ||
                                        (48...57).contains($0) || $0 == 45 || $0 == 95 }) else { return nil }
        let encoded = base64URL.replacingOccurrences(of: "-", with: "+").replacingOccurrences(of: "_", with: "/")
        self.init(base64Encoded: encoded + String(repeating: "=", count: (4 - encoded.count % 4) % 4))
    }
}

struct VerifiedIdentity {
    let subject: String
    let name: String?
    let email: String?

    struct KeySet: Decodable {
        let keys: [Key]
    }

    struct Key: Decodable {
        let kty: String
        let kid: String
        let alg: String?
        let use: String?
        let n: String?
        let e: String?
    }

    private struct Header: Decodable {
        let alg: String
        let kid: String
    }

    private struct Claims: Decodable {
        let iss: String
        let sub: String
        let aud: Audience
        let exp: TimeInterval
        let nbf: TimeInterval?
        let azp: String?
        let nonce: String?
        let name: String?
        let email: String?
        let email_verified: Bool?
    }

    private struct Audience: Decodable {
        let values: [String]

        init(from decoder: any Decoder) throws {
            let container = try decoder.singleValueContainer()
            if let single = try? container.decode(String.self) {
                values = [single]
            } else {
                values = try container.decode([String].self)
            }
        }
    }

    static func validate(
        _ token: String, keys: KeySet, clientID: String, nonce: String, now: Date = Date()
    ) throws -> VerifiedIdentity {
        let parts = token.split(separator: ".", omittingEmptySubsequences: false)
        let decoder = JSONDecoder()
        guard token.utf8.count <= 32_768, parts.count == 3,
              let headerData = Data(base64URL: parts[0]),
              let claimsData = Data(base64URL: parts[1]),
              let signature = Data(base64URL: parts[2]),
              let header = try? decoder.decode(Header.self, from: headerData), header.alg == "RS256" else {
            throw ChatGPTError.invalidIdentity
        }

        let matches = keys.keys.filter { $0.kid == header.kid && $0.kty == "RSA" &&
            ($0.alg == nil || $0.alg == "RS256") && ($0.use == nil || $0.use == "sig") }
        guard matches.count == 1, let key = matches.first,
              let encodedModulus = key.n, let modulus = Data(base64URL: encodedModulus[...]),
              (256...1024).contains(modulus.count),
              let encodedExponent = key.e, let exponent = Data(base64URL: encodedExponent[...]),
              (1...8).contains(exponent.count) else {
            throw ChatGPTError.invalidIdentity
        }

        // Security.framework accepts DER PKCS #1 public keys.
        let pkcs1 = der(0x30, integer(modulus) + integer(exponent))
        let attributes: [String: Any] = [
            kSecAttrKeyType as String: kSecAttrKeyTypeRSA,
            kSecAttrKeyClass as String: kSecAttrKeyClassPublic
        ]
        let signed = Data("\(parts[0]).\(parts[1])".utf8)
        guard let publicKey = SecKeyCreateWithData(pkcs1 as CFData, attributes as CFDictionary, nil),
              SecKeyVerifySignature(publicKey, .rsaSignatureMessagePKCS1v15SHA256, signed as CFData, signature as CFData, nil),
              let claims = try? decoder.decode(Claims.self, from: claimsData),
              claims.iss == OAuthProtocol.issuer, claims.nonce == nonce,
              claims.aud.values.contains(clientID),
              claims.aud.values.count == 1 || claims.azp == clientID,
              claims.azp == nil || claims.azp == clientID,
              !claims.sub.isEmpty, claims.exp > now.timeIntervalSince1970,
              (claims.nbf ?? 0) <= now.timeIntervalSince1970 + 60 else {
            throw ChatGPTError.invalidIdentity
        }
        return VerifiedIdentity(subject: claims.sub, name: claims.name,
                                email: claims.email_verified == true ? claims.email : nil)
    }

    private static func integer(_ source: Data) -> Data {
        var bytes = Data(source.drop(while: { $0 == 0 }))
        if bytes.isEmpty || bytes[0] & 0x80 != 0 { bytes.insert(0, at: 0) }
        return der(0x02, bytes)
    }

    private static func der(_ tag: UInt8, _ bytes: Data) -> Data {
        let count = bytes.count
        if count < 128 { return Data([tag, UInt8(count)]) + bytes }
        if count <= UInt8.max { return Data([tag, 0x81, UInt8(count)]) + bytes }
        return Data([tag, 0x82, UInt8(count >> 8), UInt8(count & 0xff)]) + bytes
    }
}
