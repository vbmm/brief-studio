import Foundation
import XCTest
@testable import BriefCore

final class BriefCoreTests: XCTestCase {
    func testProfilesRequireEveryBarAndSkipThanksgiving() throws {
        let now = date("2026-11-27T15:00:00Z")
        let wednesday = bars("2026-11-25T14:30:00Z", count: 78)
        let thanksgiving = bars("2026-11-26T14:30:00Z", count: 78)
        let after = try YahooMarket.decode(series(.nq, now: now, times: wednesday + thanksgiving), future: .nq, now: now)
        XCTAssertEqual(after.session?.day, "2026-11-25")
        XCTAssertEqual(after.session?.low, 19_950)
        XCTAssertEqual(after.session?.high, 20_050)

        let hole = wednesday.enumerated().filter { $0.offset != 30 }.map(\.element)
        let incomplete = try YahooMarket.decode(series(.nq, now: now, times: hole), future: .nq, now: now)
        XCTAssertNil(incomplete.session)
        XCTAssertNotNil(incomplete.quote)
    }

    func testEarlyCloseAndStaleQuoteAreDistinct() throws {
        let now = date("2026-11-27T19:00:00Z")
        let earlyClose = bars("2026-11-27T14:30:00Z", count: 42)
        let data = try series(.es, now: now, quoteAge: 60 * 60, times: earlyClose)
        let observation = try YahooMarket.decode(data, future: .es, now: now)
        XCTAssertNil(observation.quote)
        XCTAssertEqual(observation.session?.day, "2026-11-27")
        XCTAssertEqual(observation.session?.close, 6001)
    }

    func testGenerationRendersOnlySupportedFactsAndKeepsInstrumentsSeparate() async throws {
        let now = date("2026-11-24T14:00:00Z")
        let history = bars("2026-11-23T14:30:00Z", count: 78)
        let nq = try series(.nq, now: now, quotePrice: 20_100, times: history)
        let es = try series(.es, now: now, quoteAge: 60 * 60, times: history)
        let remote = YahooMarket { request in
            let url = try XCTUnwrap(request.url)
            let response = try XCTUnwrap(HTTPURLResponse(url: url, statusCode: 200, httpVersion: nil, headerFields: nil))
            return (url.path.contains("NQ") ? nq : es, response)
        }
        let fiscalSource = Data(rss("""
            <item><title>Minutes of the Federal Open Market Committee</title>
            <link><![CDATA[https://www.federalreserve.gov/newsevents/pressreleases/monetary20261118a.htm]]></link>
            <pubDate><![CDATA[Wed, 18 Nov 2026 19:00:00 GMT]]></pubDate></item>
            """).utf8)
        let fed = FedHeadlines { request in
            let response = try XCTUnwrap(HTTPURLResponse(url: XCTUnwrap(request.url), statusCode: 200, httpVersion: nil, headerFields: nil))
            return (fiscalSource, response)
        }
        let document = try await BriefEngine(market: remote, fed: fed, now: { now }).generate(
            context: "Surveiller le calendrier.", sourceURL: "https://example.com/calendar"
        ) { prompt in
            XCTAssertTrue(prompt.contains("NQ\nCotation : 20100.0 USD"))
            XCTAssertTrue(prompt.contains("ES\nCotation : indisponible"))
            XCTAssertTrue(prompt.contains("Surveiller le calendrier."))
            XCTAssertTrue(prompt.contains("Minutes of the Federal Open Market Committee"))
            return """
            ```json
            {"nq":{"view":"hausse","watch":"bornes","target":29999},"es":{"view":"baisse","watch":"cloture"}}
            ```
            """
        }
        XCTAssertEqual(document.schema, "nq-es-brief/v1")
        XCTAssertEqual(document.generatedAt, "2026-11-24T14:00:00Z")
        XCTAssertTrue(document.text.contains("Hypothèse haussière au-dessus"))
        XCTAssertTrue(document.text.contains("Indisponible sans cotation récente"))
        XCTAssertFalse(document.text.contains("Hypothèse baissière"))
        XCTAssertFalse(document.text.contains("29999"))
        XCTAssertTrue(document.text.contains("Fed · 2026-11-18 14:00 ET"))
        XCTAssertTrue(document.sources?.contains(where: { $0.url.contains("monetary20261118a.htm") }) == true)
        XCTAssertEqual(document.sources?.last?.url, "https://example.com/calendar")
        XCTAssertEqual(try JSONDecoder().decode(BriefDocument.self, from: JSONEncoder().encode(document)), document)
    }

    func testNoInferenceWhenProviderDataIsUnavailable() async throws {
        let now = date("2026-11-24T14:00:00Z")
        let remote = YahooMarket { request in
            let response = try XCTUnwrap(HTTPURLResponse(url: XCTUnwrap(request.url), statusCode: 429, httpVersion: nil, headerFields: nil))
            return (Data(), response)
        }
        do {
            _ = try await BriefEngine(market: remote, fed: offlineFed, now: { now }).generate { _ in
                XCTFail("Inference must not be called with no current data.")
                return ""
            }
            XCTFail("Expected a market error.")
        } catch BriefError.marketUnavailable(let details) {
            XCTAssertTrue(details.contains("NQ :"))
            XCTAssertTrue(details.contains("ES :"))
        }
    }

    func testFedUnavailableKeepsFuturesBriefUsable() async throws {
        let now = date("2026-11-24T14:00:00Z")
        let nq = try series(.nq, now: now, times: [])
        let es = try series(.es, now: now, times: [])
        let remote = YahooMarket { request in
            let url = try XCTUnwrap(request.url)
            let response = try XCTUnwrap(HTTPURLResponse(url: url, statusCode: 200, httpVersion: nil, headerFields: nil))
            return (url.path.contains("NQ") ? nq : es, response)
        }
        let document = try await BriefEngine(market: remote, fed: offlineFed, now: { now }).generate { _ in
            #"{"nq":{"view":"attente","watch":"confirmation"},"es":{"view":"attente","watch":"confirmation"}}"#
        }
        XCTAssertTrue(document.text.contains("Fil monétaire de la Fed indisponible."))
        XCTAssertEqual(document.sources?.count, 2)
    }

    func testFedParsesSevenDayWindowAndRejectsMalformedOrExternalItems() {
        let now = date("2026-11-24T14:00:00Z")
        func item(_ id: String, _ publication: String, host: String = "www.federalreserve.gov") -> String {
            "<item><title>News &amp; \(id)</title><link>https://\(host)/newsevents/pressreleases/monetary\(id).htm</link><pubDate>\(publication)</pubDate></item>"
        }
        let approved = item("2", "Mon, 23 Nov 2026 16:00:00 GMT")
        let xml = rss(approved + approved +
                      item("1", "Wed, 18 Nov 2026 19:00:00 GMT") +
                      item("3", "Tue, 24 Nov 2026 13:00:00 GMT") +
                      item("4", "Tue, 24 Nov 2026 12:00:00 GMT") +
                      item("old", "Mon, 16 Nov 2026 19:00:00 GMT") +
                      item("future", "Tue, 24 Nov 2026 16:00:00 GMT") +
                      item("external", "Tue, 24 Nov 2026 13:59:00 GMT", host: "example.com"))
        XCTAssertEqual(FedHeadlines.parse(Data(xml.utf8), now: now).items.map(\.title), ["News & 3", "News & 4", "News & 2"])
        XCTAssertEqual(FedHeadlines.parse(Data("<html/ >".utf8), now: now), .unavailable)
        XCTAssertEqual(FedHeadlines.parse(Data(("<!DOCTYPE rss>" + xml).utf8), now: now), .unavailable)
        XCTAssertEqual(FedHeadlines.parse(Data(rss("").utf8), now: now), .available([]))
    }

    private var offlineFed: FedHeadlines { FedHeadlines { _ in throw URLError(.timedOut) } }

    private func rss(_ items: String) -> String { "<rss version=\"2.0\"><channel>\(items)</channel></rss>" }

    private func date(_ string: String) -> Date { ISO8601DateFormatter().date(from: string)! }

    private func bars(_ start: String, count: Int) -> [Int] {
        let timestamp = Int(date(start).timeIntervalSince1970)
        return (0..<count).map { timestamp + 300 * $0 }
    }

    private func series(_ future: Future, now: Date, quoteAge: Int = 600, quotePrice: Double? = nil,
                        times: [Int]) throws -> Data {
        let price: Double = future == .nq ? 20_000 : 6_000
        let fixture: [String: Any] = ["chart": ["result": [[
            "meta": ["symbol": future.ticker, "currency": "USD", "instrumentType": "FUTURE", "dataGranularity": "5m",
                     "regularMarketPrice": quotePrice ?? price, "regularMarketTime": Int(now.timeIntervalSince1970) - quoteAge],
            "timestamp": times,
            "indicators": ["quote": [[
                "open": times.map { _ in price }, "low": times.map { _ in price - 50 },
                "high": times.map { _ in price + 50 }, "close": times.map { _ in price + 1 },
            ]]],
        ]]]]
        return try JSONSerialization.data(withJSONObject: fixture)
    }
}
