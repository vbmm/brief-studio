import Foundation

/// Processes the typed Responses SSE events without publishing partial text as a complete brief.
struct ResponsesStream {
    private struct Event: Decodable {
        struct Failure: Decodable { let code: String? }
        struct Response: Decodable {
            let status: String?
            let error: Failure?
        }
        let type: String
        let delta: String?
        let response: Response?
        let code: String?
    }

    private var eventData = [String]()
    private var text = ""
    private var textLength = 0
    private var eventLength = 0
    private var lineBytes: [UInt8] = []
    private var previousWasCarriageReturn = false

    mutating func accept(byte: UInt8) throws -> String? {
        // Foundation's AsyncBytes.lines omits blank lines, which delimit SSE
        // events. Preserve them and decode UTF-8 only after a complete line.
        if previousWasCarriageReturn {
            previousWasCarriageReturn = false
            if byte == 10 { return nil }
        }
        if byte == 10 || byte == 13 {
            previousWasCarriageReturn = byte == 13
            guard let line = String(bytes: lineBytes, encoding: .utf8) else {
                throw ChatGPTError.incompleteResponse
            }
            lineBytes.removeAll(keepingCapacity: true)
            return try accept(line: line)
        }
        guard lineBytes.count < 4 * 1024 * 1024 else { throw ChatGPTError.incompleteResponse }
        lineBytes.append(byte)
        return nil
    }

    mutating func accept(line: String) throws -> String? {
        guard line.isEmpty else {
            guard line.hasPrefix("data:") else { return nil }
            let value = String(line.dropFirst(line.hasPrefix("data: ") ? 6 : 5))
            eventLength += value.utf8.count
            guard eventLength <= 4 * 1024 * 1024, eventData.count < 256 else {
                throw ChatGPTError.incompleteResponse
            }
            eventData.append(value)
            return nil
        }
        guard !eventData.isEmpty else { return nil }
        let data = eventData.joined(separator: "\n")
        eventData.removeAll(keepingCapacity: true)
        eventLength = 0
        if data == "[DONE]" { return nil }
        let event = try JSONDecoder().decode(Event.self, from: Data(data.utf8))
        switch event.type {
        case "response.output_text.delta":
            if let delta = event.delta {
                textLength += delta.utf8.count
                guard textLength <= 1024 * 1024 else { throw ChatGPTError.incompleteResponse }
                text.append(delta)
            }
        case "response.completed":
            guard event.response?.status == "completed" else { throw ChatGPTError.incompleteResponse }
            let completed = text.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !completed.isEmpty else { throw ChatGPTError.emptyResponse }
            return completed
        case "response.failed", "response.incomplete", "error":
            let code = event.response?.error?.code ?? event.code ?? ""
            if code.hasPrefix("subscription_sharing_usage_") { throw ChatGPTError.usageLimit }
            throw ChatGPTError.incompleteResponse
        default:
            break
        }
        return nil
    }
}
