import Foundation

struct Version: Comparable {
    let numbers: [Int]

    init?(_ value: String) {
        let text = value.hasPrefix("v") ? value.dropFirst() : value[...]
        let parts = text.split(separator: ".", omittingEmptySubsequences: false)
        guard parts.count == 3,
              parts.allSatisfy({ !$0.isEmpty && $0.utf8.allSatisfy { (48...57).contains($0) } }),
              parts.compactMap({ Int($0) }).count == 3 else { return nil }
        numbers = parts.compactMap { Int($0) }
    }

    static func < (lhs: Version, rhs: Version) -> Bool {
        lhs.numbers.lexicographicallyPrecedes(rhs.numbers)
    }
}

struct ReleaseUpdates {
    struct Available: Identifiable {
        let tag: String
        let releasePage: URL
        var id: String { tag }
    }

    enum Failure: LocalizedError {
        case badRepository, noRelease, invalidRelease, server(Int)
        var errorDescription: String? {
            switch self {
            case .badRepository: "Indique le dépôt GitHub au format propriétaire/dépôt."
            case .noRelease: "Ce dépôt n’a pas encore de version publique."
            case .invalidRelease: "Cette version GitHub ne correspond pas au format de l’app."
            case let .server(code): "GitHub est indisponible (HTTP \(code)). Réessaie plus tard."
            }
        }
    }

    let repository: String

    init(repository: String) throws {
        let candidate = repository.trimmingCharacters(in: .whitespacesAndNewlines)
        let parts = candidate.split(separator: "/", omittingEmptySubsequences: false)
        let allowed = CharacterSet(charactersIn: "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._-")
        guard parts.count == 2, parts.allSatisfy({ part in
            !part.isEmpty && part.utf8.count <= 100 && part != "." && part != ".." &&
            part.unicodeScalars.allSatisfy(allowed.contains)
        }) else { throw Failure.badRepository }
        self.repository = candidate
    }

    func check(installed: String, session: URLSession = .shared) async throws -> Available? {
        var request = URLRequest(url: URL(string: "https://api.github.com/repos/\(repository)/releases/latest")!)
        request.timeoutInterval = 15
        request.setValue("application/vnd.github+json", forHTTPHeaderField: "Accept")
        let (data, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse else { throw Failure.invalidRelease }
        if http.statusCode == 404 { throw Failure.noRelease }
        guard http.statusCode == 200 else { throw Failure.server(http.statusCode) }
        return try parse(data, installed: installed)
    }

    func parse(_ data: Data, installed: String) throws -> Available? {
        struct Release: Decodable {
            let tag_name: String
            let draft: Bool
            let prerelease: Bool
        }
        let item = try JSONDecoder().decode(Release.self, from: data)
        guard !item.draft, !item.prerelease else { return nil }
        guard let current = Version(installed), let offered = Version(item.tag_name) else { throw Failure.invalidRelease }
        guard current < offered else { return nil }
        // Construct the link from the user's repository and validated tag. Ignore URLs in metadata.
        let page = URL(string: "https://github.com/\(repository)/releases/tag/\(item.tag_name)")!
        return Available(tag: item.tag_name, releasePage: page)
    }
}
