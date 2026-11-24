import Foundation
import Network

/// A one-shot listener on IPv4 loopback. Mutable state is confined to its queue.
final class Loopback: @unchecked Sendable {
    private let queue = DispatchQueue(label: "BriefAuth.loopback")
    private let listener: NWListener
    private let expectedState: String
    private var startReply: CheckedContinuation<UInt16, Error>?
    private var callbackReply: CheckedContinuation<OAuthProtocol.Callback, Error>?
    private var result: Result<OAuthProtocol.Callback, Error>?
    private var pendingConnections: [ObjectIdentifier: NWConnection] = [:]

    init(state: String) throws {
        let parameters = NWParameters.tcp
        parameters.requiredLocalEndpoint = .hostPort(host: "127.0.0.1", port: .any)
        listener = try NWListener(using: parameters)
        expectedState = state
    }

    func start() async throws -> UInt16 {
        try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { reply in
                queue.async { [self] in
                    if let result = self.result {
                        do { _ = try result.get() } catch { reply.resume(throwing: error); return }
                        reply.resume(throwing: ChatGPTError.invalidAuthorization)
                        return
                    }
                    self.startReply = reply
                    self.listener.stateUpdateHandler = { [weak self] state in self?.receiveListenerState(state) }
                    self.listener.newConnectionHandler = { [weak self] connection in self?.receive(connection) }
                    self.listener.start(queue: self.queue)
                    self.queue.asyncAfter(deadline: .now() + 180) { [weak self] in
                        self?.finish(.failure(ChatGPTError.authorizationTimedOut))
                    }
                }
            }
        } onCancel: { cancel() }
    }

    func callback() async throws -> OAuthProtocol.Callback {
        try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { reply in
                queue.async { [self] in
                    if let result = self.result {
                        reply.resume(with: result)
                    } else {
                        self.callbackReply = reply
                    }
                }
            }
        } onCancel: { cancel() }
    }

    func cancel() {
        queue.async { [self] in self.finish(.failure(CancellationError())) }
    }

    private func receiveListenerState(_ state: NWListener.State) {
        switch state {
        case .ready:
            if let port = listener.port?.rawValue, port != 0 {
                startReply?.resume(returning: port)
                startReply = nil
            } else {
                finish(.failure(ChatGPTError.localListener))
            }
        case .failed, .cancelled:
            finish(.failure(ChatGPTError.localListener))
        default:
            break
        }
    }

    private func receive(_ connection: NWConnection) {
        guard result == nil, pendingConnections.count < 8 else { connection.cancel(); return }
        let id = ObjectIdentifier(connection)
        pendingConnections[id] = connection
        connection.start(queue: queue)
        read(connection, accumulated: Data())
        queue.asyncAfter(deadline: .now() + 5) { [weak self, weak connection] in
            guard let connection, self?.pendingConnections.removeValue(forKey: id) != nil else { return }
            connection.cancel()
        }
    }

    private func read(_ connection: NWConnection, accumulated: Data) {
        connection.receive(minimumIncompleteLength: 1, maximumLength: 8192) { [weak self] buffer, _, end, error in
            guard let self, self.result == nil, self.pendingConnections[ObjectIdentifier(connection)] != nil else { return }
            let request = accumulated + (buffer ?? Data())
            guard request.count <= 8192, error == nil else {
                self.reply(to: connection, ok: false)
                return
            }
            if let endOfLine = request.range(of: Data([13, 10])),
               let line = String(data: request.prefix(upTo: endOfLine.lowerBound), encoding: .ascii) {
                self.handle(line, connection: connection)
            } else if end {
                self.reply(to: connection, ok: false)
            } else {
                self.read(connection, accumulated: request)
            }
        }
    }

    private func handle(_ line: String, connection: NWConnection) {
        let fields = line.split(separator: " ", omittingEmptySubsequences: false)
        guard fields.count == 3, fields[0] == "GET", ["HTTP/1.1", "HTTP/1.0"].contains(fields[2]) else {
            reply(to: connection, ok: false)
            return
        }
        do {
            let callback = try OAuthProtocol.Callback.parse(requestTarget: String(fields[1]), state: expectedState)
            reply(to: connection, ok: true)
            finish(.success(callback))
        } catch ChatGPTError.invalidAuthorization {
            // Stray browser requests and mismatched state do not consume the attempt.
            reply(to: connection, ok: false)
        } catch {
            reply(to: connection, ok: false)
            finish(.failure(error))
        }
    }

    private func reply(to connection: NWConnection, ok: Bool) {
        pendingConnections.removeValue(forKey: ObjectIdentifier(connection))
        let message = ok
            ? "Autorisation reçue. Revenez à Brief Studio pour terminer la connexion."
            : "Cette demande n’a pas abouti. Revenez à Brief Studio."
        let status = ok ? "200 OK" : "400 Bad Request"
        let headers = "HTTP/1.1 \(status)\r\nContent-Type: text/plain; charset=utf-8\r\n"
            + "Content-Length: \(message.utf8.count)\r\nCache-Control: no-store\r\n"
            + "Referrer-Policy: no-referrer\r\nX-Content-Type-Options: nosniff\r\nConnection: close\r\n\r\n"
        connection.send(content: Data((headers + message).utf8), completion: .contentProcessed { _ in
            connection.cancel()
        })
    }

    private func finish(_ outcome: Result<OAuthProtocol.Callback, Error>) {
        guard result == nil else { return }
        result = outcome
        listener.cancel()
        for connection in pendingConnections.values { connection.cancel() }
        pendingConnections.removeAll()
        if let startReply {
            switch outcome {
            case .success: startReply.resume(throwing: ChatGPTError.invalidAuthorization)
            case let .failure(error): startReply.resume(throwing: error)
            }
            self.startReply = nil
        }
        callbackReply?.resume(with: outcome)
        callbackReply = nil
    }
}
