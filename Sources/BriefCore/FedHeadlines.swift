import Foundation

struct FedHeadlines: Sendable {
    struct Item: Sendable, Equatable {
        let title: String
        let date: Date
        let url: URL
    }

    enum Feed: Sendable, Equatable {
        case unavailable
        case available([Item])

        var items: [Item] {
            if case let .available(items) = self { items } else { [] }
        }
    }

    private static let feedURL = URL(string: "https://www.federalreserve.gov/feeds/press_monetary.xml")!
    private let load: @Sendable (URLRequest) async throws -> (Data, URLResponse)

    init(load: @escaping @Sendable (URLRequest) async throws -> (Data, URLResponse) = {
        try await URLSession.shared.data(for: $0)
    }) {
        self.load = load
    }

    func read(now: Date) async -> Feed {
        var request = URLRequest(url: Self.feedURL, cachePolicy: .reloadIgnoringLocalCacheData, timeoutInterval: 6)
        request.setValue("application/rss+xml, application/xml, text/xml", forHTTPHeaderField: "Accept")
        do {
            let (data, response) = try await load(request)
            guard let response = response as? HTTPURLResponse, response.statusCode == 200,
                  response.url == Self.feedURL else { return .unavailable }
            return Self.parse(data, now: now)
        } catch {
            return .unavailable
        }
    }

    static func parse(_ data: Data, now: Date) -> Feed {
        guard data.count <= 300_000, let xml = String(data: data, encoding: .utf8),
              !xml.localizedCaseInsensitiveContains("<!DOCTYPE"), !xml.localizedCaseInsensitiveContains("<!ENTITY") else {
            return .unavailable
        }
        let parser = XMLParser(data: data)
        parser.shouldResolveExternalEntities = false
        let delegate = RSSItems()
        parser.delegate = delegate
        guard parser.parse(), delegate.sawRSS, delegate.sawChannel else { return .unavailable }

        let dateFormat = DateFormatter()
        dateFormat.locale = Locale(identifier: "en_US_POSIX")
        dateFormat.dateFormat = "EEE, d MMM yyyy HH:mm:ss zzz"
        let range = now.addingTimeInterval(-7 * 86_400)...now
        let eligible = delegate.rows.compactMap { row -> Item? in
            let title = row["title", default: ""].split(whereSeparator: \.isWhitespace).joined(separator: " ")
            guard !title.isEmpty, title.count <= 300,
                  let date = dateFormat.date(from: row["pubDate", default: ""].trimmingCharacters(in: .whitespacesAndNewlines)),
                  range.contains(date),
                  let url = URL(string: row["link", default: ""].trimmingCharacters(in: .whitespacesAndNewlines)),
                  url.scheme == "https", url.host == "www.federalreserve.gov", url.port == nil,
                  url.user == nil, url.password == nil, url.query == nil, url.fragment == nil,
                  url.path.hasPrefix("/newsevents/pressreleases/monetary"), url.pathExtension == "htm" else { return nil }
            return Item(title: title, date: date, url: url)
        }.sorted { $0.date > $1.date }

        var seen = Set<URL>()
        return .available(Array(eligible.filter { seen.insert($0.url).inserted }.prefix(3)))
    }
}

private final class RSSItems: NSObject, XMLParserDelegate {
    private var path: [String] = []
    private var fields: [String: String] = [:]
    fileprivate var sawRSS = false
    fileprivate var sawChannel = false
    fileprivate var rows: [[String: String]] = []

    func parser(_ parser: XMLParser, didStartElement elementName: String, namespaceURI: String?,
                qualifiedName: String?, attributes: [String: String]) {
        path.append(elementName)
        switch path {
        case ["rss"]: sawRSS = true
        case ["rss", "channel"]: sawChannel = true
        case ["rss", "channel", "item"]: fields = [:]
        default: break
        }
    }

    func parser(_ parser: XMLParser, didEndElement elementName: String, namespaceURI: String?, qualifiedName: String?) {
        if path == ["rss", "channel", "item"] { rows.append(fields) }
        path.removeLast()
    }

    func parser(_ parser: XMLParser, foundCharacters string: String) { append(string) }

    func parser(_ parser: XMLParser, foundCDATA CDATABlock: Data) {
        if let string = String(data: CDATABlock, encoding: .utf8) { append(string) }
    }

    private func append(_ string: String) {
        guard path.count == 4, path.prefix(3).elementsEqual(["rss", "channel", "item"]),
              let field = path.last, ["title", "link", "pubDate"].contains(field) else { return }
        fields[field, default: ""] += string
    }
}
