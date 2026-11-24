import BriefCore
import Foundation

enum BriefFiles {
    static let marker = "__EMBEDDED_BRIEF_BASE64__"
    static let maxSize = 512 * 1024

    static func encode(_ brief: BriefDocument) throws -> Data {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes]
        return try encoder.encode(brief)
    }

    static func decode(_ data: Data) throws -> BriefDocument {
        guard data.count <= maxSize else { throw Failure.badDocument }
        let brief = try JSONDecoder().decode(BriefDocument.self, from: data)
        let date = ISO8601DateFormatter()
        let standard = date.date(from: brief.generatedAt)
        date.formatOptions.insert(.withFractionalSeconds)
        guard brief.schema == BriefDocument.currentSchema, standard != nil || date.date(from: brief.generatedAt) != nil,
              brief.title.utf16.count <= 240,
              !brief.title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
              !brief.text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { throw Failure.badDocument }
        return brief
    }

    static func html(_ brief: BriefDocument, template: String) throws -> String {
        // Base64 cannot contain HTML delimiters. The viewer decodes it as data and renders text nodes.
        let positions = template.components(separatedBy: marker)
        guard positions.count == 2 else { throw Failure.viewerMissing }
        let payload = try encode(brief)
        _ = try decode(payload)
        return positions[0] + payload.base64EncodedString() + positions[1]
    }

    static func template() throws -> String {
        let bundled = Bundle.main.url(forResource: "brief-viewer", withExtension: "html")
        let checkout = URL(fileURLWithPath: FileManager.default.currentDirectoryPath)
            .appendingPathComponent("viewer/index.html")
        return try String(contentsOf: bundled ?? checkout, encoding: .utf8)
    }

    static func read(_ url: URL) throws -> BriefDocument {
        let handle = try FileHandle(forReadingFrom: url)
        defer { try? handle.close() }
        return try decode(handle.read(upToCount: maxSize + 1) ?? Data())
    }

    enum Failure: LocalizedError {
        case badDocument, viewerMissing
        var errorDescription: String? {
            switch self {
            case .badDocument: "Ce fichier n’est pas un brief NQ/ES valide."
            case .viewerMissing: "Le modèle HTML est introuvable. Réinstalle Brief Studio."
            }
        }
    }
}
