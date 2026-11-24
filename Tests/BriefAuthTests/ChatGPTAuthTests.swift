import Foundation
import Testing
@testable import BriefAuth

struct ChatGPTAuthTests {
    @Test func dynamicRegistrationUsesSeparateArtifactIdentityAndPKCE() throws {
        let attempt = OAuthProtocol.Attempt(
            port: 54_321, state: "fixture-state", nonce: "fixture-nonce",
            verifier: "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
        )
        let authorize = try attempt.authorizationURL(
            appName: "Brief Studio", hostID: "urn:uuid:fixture", clientID: nil, identityHint: nil
        )
        let components = try #require(URLComponents(url: authorize, resolvingAgainstBaseURL: false))
        let fields = Dictionary(uniqueKeysWithValues: try #require(components.queryItems).compactMap {
            item in item.value.map { (item.name, $0) }
        })
        #expect(components.host == "auth.openai.com")
        #expect(fields["client_id"] == "dynamic_agent_client")
        #expect(fields["agent_name_hint"] == "Brief Studio")
        #expect(fields["redirect_uri"] == "http://127.0.0.1:54321/auth/callback")
        #expect(fields["ext_agent_host_id"] == "urn:uuid:fixture")
        #expect(fields["resource"] == "https://api.openai.com/v1")
        #expect(fields["code_challenge_method"] == "S256")
        #expect(fields["code_challenge"] == "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM")
        #expect(fields["scope"]?.hasSuffix("chatgpt.tokens.use.direct") == true)
        #expect(fields["state"] == "fixture-state")
        #expect(fields["nonce"] == "fixture-nonce")
        #expect(fields["code_verifier"] == nil)
        #expect(fields["id_token_hint"] == nil)
        let returning = try attempt.authorizationURL(
            appName: "Brief Studio", hostID: "urn:uuid:fixture", clientID: "oaiapp_known", identityHint: "signed.hint"
        )
        let returningFields = try #require(URLComponents(url: returning, resolvingAgainstBaseURL: false)?.queryItems)
        #expect(returningFields.contains(URLQueryItem(name: "client_id", value: "oaiapp_known")))
        #expect(returningFields.contains(URLQueryItem(name: "id_token_hint", value: "signed.hint")))
        #expect(!returningFields.contains(where: { $0.name == "agent_name_hint" }))
        #expect(OAuthProtocol.form([("code", "one+two&three=✓")]) == "code=one%2Btwo%26three%3D%E2%9C%93")
    }

    @Test func callbackRequiresExactPathAndSingleMatchingState() throws {
        let state = "random-test-state"
        let accepted = try OAuthProtocol.Callback.parse(
            requestTarget: "/auth/callback?code=abc%2B123&state=\(state)&client_id=oaiapp_example", state: state
        )
        #expect(accepted.code == "abc+123")
        #expect(try accepted.resolveClientID(existing: nil) == "oaiapp_example")
        #expect(throws: ChatGPTError.invalidAuthorization) {
            try accepted.resolveClientID(existing: "oaiapp_other")
        }
        for target in [
            "/auth/callback?code=one&state=wrong",
            "/auth/callback?code=one&state=\(state)&state=\(state)",
            "/auth/callback?code=one&code=two&state=\(state)",
            "/auth/callback/elsewhere?code=one&state=\(state)",
            "/auth/callback?code=one&state=\(state)#fragment",
        ] {
            #expect(throws: ChatGPTError.invalidAuthorization) {
                try OAuthProtocol.Callback.parse(requestTarget: target, state: state)
            }
        }
        #expect(throws: ChatGPTError.authorizationDeclined) {
            try OAuthProtocol.Callback.parse(requestTarget: "/auth/callback?error=access_denied&state=\(state)", state: state)
        }
    }

    @Test func verifiedIdentityUsesTrustedSignatureAndBoundClaims() throws {
        // Public test key and signature generated independently with OpenSSL.
        let jwksJSON = Data(#"{"keys":[{"kty":"RSA","kid":"test","alg":"RS256","use":"sig","n":"m_OlCxNdqhGyPhQzcCGgwGi2E9JyOTzlEu23yYA_hi0TeSWJyxKgT_-3cGKsrXvyO4OCDVx2Zsc5l8hdP4eTlW1A4DqFBL15ZLHID3tqSF7-9SPWX6sxax0LFFwAimxKrz2RxdSoAjgQJOfZTSmorR2M5v9D7QCXAgZzXfM_a8KpGApzoFI1WnNzvby9POtvw8m4GlMPBuNTXsd-ZKSQW0DwZPs8SnqzR_bhIuihofKHf7dBGONKxY6ecJmOyzngnRMuF6YxWaK9IC1HbD-YjVlQeNCpJhjP3Ma0_O4dypMTUAjZvT496tjqZYA_UBiEu94Yrxey-C0jnLtfkdN97w","e":"AQAB"}]}"#.utf8)
        let keys = try JSONDecoder().decode(VerifiedIdentity.KeySet.self, from: jwksJSON)
        let time = Date(timeIntervalSince1970: 1_800_000_000)
        let body = "eyJhbGciOiJSUzI1NiIsImtpZCI6InRlc3QifQ.eyJpc3MiOiJodHRwczovL2F1dGgub3BlbmFpLmNvbSIsImF1ZCI6Im9haWFwcF90ZXN0Iiwic3ViIjoidmVyaWZpZWQtYWNjb3VudCIsIm5vbmNlIjoibm9uY2UtMTIzIiwiZXhwIjoxODAwMDAwMzAwLCJlbWFpbCI6ImRlbW9AZXhhbXBsZS5jb20iLCJlbWFpbF92ZXJpZmllZCI6dHJ1ZX0"
        let signature = "j3F96qX-WSkPwv-69Py8WPZPm1cRu1uBY9DK91U6tllMUm_3-7k1Dd3ZyXEH-AJj8l9byVQIvkkWW9Xh1il9Wsd7nNrEskzJ8uMX3EXS3hPWPWzByr-7xrSPppaCCafPkLD5lanSkm1bh1XX6uE6B2tDNmYRcyzsA3IC1x05AqgeakZq1nO2OUFn7qGF-Qzg0M39YAp-ua7BMWdScV79rjOisvNtDytIF9CjuG0dWhWzMUr0t8QED8rzU9Trt70CoFNR8yGvxBhvNLDRYb7X_mRmgCSi5BBlivBXLESBpWLBZEGMgGGGtqAFOV3JyYxix4aeWEYoO6ias4M0Ti6ylg"
        let token = body + "." + signature
        let profile = try VerifiedIdentity.validate(token, keys: keys, clientID: "oaiapp_test", nonce: "nonce-123", now: time)
        #expect(profile.subject == "verified-account")
        #expect(profile.email == "demo@example.com")
        #expect(throws: ChatGPTError.invalidIdentity) {
            try VerifiedIdentity.validate(token, keys: keys, clientID: "oaiapp_test", nonce: "wrong", now: time)
        }
        #expect(throws: ChatGPTError.invalidIdentity) {
            try VerifiedIdentity.validate(token, keys: keys, clientID: "oaiapp_other", nonce: "nonce-123", now: time)
        }
        #expect(throws: ChatGPTError.invalidIdentity) {
            try VerifiedIdentity.validate(token, keys: keys, clientID: "oaiapp_test", nonce: "nonce-123", now: time.addingTimeInterval(301))
        }
        let forgedSignature = (signature.first == "a" ? "b" : "a") + String(signature.dropFirst())
        #expect(throws: ChatGPTError.invalidIdentity) {
            try VerifiedIdentity.validate("\(body).\(forgedSignature)", keys: keys,
                                          clientID: "oaiapp_test", nonce: "nonce-123", now: time)
        }
    }

    @Test func inferenceWireFormatUsesPlanCompatibleResponses() throws {
        let data = try JSONEncoder().encode(InferenceRequest(
            model: "fixture-model", instructions: "Write in French.", input: [.init(role: "user", content: "fixture")]
        ))
        let payload = try #require(try JSONSerialization.jsonObject(with: data) as? [String: Any])
        #expect(Set(payload.keys) == ["store", "stream", "model", "input", "instructions"])
        #expect(payload["store"] as? Bool == false)
        #expect(payload["stream"] as? Bool == true)
        #expect((payload["input"] as? [[String: String]])?.first?["role"] == "user")
    }

    @Test func responseReturnsOnlyAfterSuccessfulTerminalEvent() throws {
        var stream = ResponsesStream()
        #expect(try stream.accept(line: #"data: {"type":"response.output_text.delta","delta":"Brief NQ"}"#) == nil)
        #expect(try stream.accept(line: "") == nil)
        #expect(try stream.accept(line: #"data: {"type":"response.output_text.delta","delta":" / ES"}"#) == nil)
        #expect(try stream.accept(line: "") == nil)
        #expect(try stream.accept(line: #"data: {"type":"response.completed","response":{"status":"completed"}}"#) == nil)
        #expect(try stream.accept(line: "") == "Brief NQ / ES")
    }

    @Test func usageLimitErrorDiscardsPartialText() throws {
        var stream = ResponsesStream()
        _ = try stream.accept(line: #"data: {"type":"response.output_text.delta","delta":"partial"}"#)
        _ = try stream.accept(line: "")
        _ = try stream.accept(line: #"data: {"type":"response.failed","response":{"status":"failed","error":{"code":"subscription_sharing_usage_limit_exceeded"}}}"#)
        #expect(throws: ChatGPTError.usageLimit) { try stream.accept(line: "") }
    }

    @Test func byteReaderPreservesSSEFramesAndUTF8() throws {
        let data = [
            #"data: {"type":"response.output_text.delta","delta":"Séance "}"#,
            "",
            #"data: {"type":"response.output_text.delta","delta":"NQ/ES"}"#,
            "",
            #"data: {"type":"response.completed","response":{"status":"completed"}}"#,
            "", "",
        ].joined(separator: "\r\n")
        var stream = ResponsesStream()
        let results = try data.utf8.compactMap { try stream.accept(byte: $0) }
        #expect(results == ["Séance NQ/ES"])
    }

}
