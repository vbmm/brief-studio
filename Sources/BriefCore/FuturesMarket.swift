import Foundation

enum Future: String, CaseIterable, Sendable {
    case nq = "NQ"
    case es = "ES"

    var name: String { self == .nq ? "Nasdaq 100" : "S&P 500" }
    var ticker: String { rawValue + "=F" }
    var source: BriefDocument.Source {
        .init(label: "Yahoo Finance · \(ticker) (CME, différé)",
              url: "https://finance.yahoo.com/quote/\(rawValue)%3DF/")
    }
}

struct MarketObservation: Sendable {
    let future: Future
    let quote: Quote?
    let session: CashSession?
    let message: String?

    struct Quote: Sendable {
        let price: Double
        let time: Date
    }

    struct CashSession: Sendable {
        let day: String
        let low: Double
        let high: Double
        let close: Double
    }
}

struct YahooMarket: Sendable {
    typealias Load = @Sendable (URLRequest) async throws -> (Data, URLResponse)
    private let load: Load
    private static let host = "query1.finance.yahoo.com"
    private static let barSeconds = 300
    private static let freshnessLimit: TimeInterval = 45 * 60

    init(load: @escaping Load = { request in try await URLSession.shared.data(for: request) }) {
        self.load = load
    }

    func read(_ future: Future, now: Date) async throws -> MarketObservation {
        let url = URL(string: "https://\(Self.host)/v8/finance/chart/\(future.rawValue)%3DF?range=5d&interval=5m")!
        var request = URLRequest(url: url, cachePolicy: .reloadIgnoringLocalCacheData, timeoutInterval: 15)
        request.setValue("Mozilla/5.0", forHTTPHeaderField: "User-Agent")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        do {
            let (data, response) = try await load(request)
            try Task.checkCancellation()
            guard let response = response as? HTTPURLResponse,
                  response.url?.scheme == "https", response.url?.host == Self.host,
                  response.statusCode == 200, data.count <= 2_000_000 else {
                return Self.unavailable(future, "Yahoo Finance n’a pas fourni la série.")
            }
            return try Self.decode(data, future: future, now: now)
        } catch {
            try Task.checkCancellation()
            return Self.unavailable(future, "La série Yahoo Finance est temporairement indisponible.")
        }
    }

    static func unavailable(_ future: Future, _ reason: String) -> MarketObservation {
        MarketObservation(future: future, quote: nil, session: nil, message: reason)
    }

    static func decode(_ data: Data, future: Future, now: Date) throws -> MarketObservation {
        let envelope = try JSONDecoder().decode(Envelope.self, from: data)
        guard let series = envelope.chart.result?.first,
              series.meta.symbol == future.ticker, series.meta.instrumentType == "FUTURE",
              series.meta.currency == "USD", series.meta.dataGranularity == "5m" else {
            return unavailable(future, "La réponse ne correspond pas au contrat attendu.")
        }

        let quote: MarketObservation.Quote?
        let message: String?
        if let price = series.meta.regularMarketPrice, validPrice(price),
           let tick = series.meta.regularMarketTime, tick > 0 {
            let time = Date(timeIntervalSince1970: Double(tick))
            let age = now.timeIntervalSince(time)
            if (0...freshnessLimit).contains(age) {
                quote = .init(price: price, time: time)
                message = nil
            } else {
                quote = nil
                message = age > freshnessLimit
                    ? "Dernière cotation trop ancienne (\(BriefDate.newYork(time, format: "yyyy-MM-dd HH:mm")) ET)."
                    : "Horodatage de cotation incohérent."
            }
        } else {
            quote = nil
            message = "Dernière cotation horodatée indisponible."
        }

        return MarketObservation(future: future, quote: quote, session: cashSession(series, now: now), message: message)
    }

    private static func cashSession(_ series: Series, now: Date) -> MarketObservation.CashSession? {
        guard let window = CashSchedule.lastCompleted(before: now),
              let times = series.timestamp, let columns = series.indicators?.quote.first,
              let opens = columns.open, let lows = columns.low, let highs = columns.high,
              let closes = columns.close,
              [opens.count, lows.count, highs.count, closes.count].allSatisfy({ $0 == times.count }) else { return nil }

        let start = Int(window.open.timeIntervalSince1970)
        let end = Int(window.close.timeIntervalSince1970)
        let indices = times.indices.filter { (start..<end).contains(times[$0]) }.sorted { times[$0] < times[$1] }
        let expected = Array(stride(from: start, to: end, by: barSeconds))
        guard indices.map({ times[$0] }) == expected, let final = indices.last, let finalClose = closes[final] else { return nil }
        for index in indices {
            guard let open = opens[index], let low = lows[index], let high = highs[index], let close = closes[index],
                  [open, low, high, close].allSatisfy(validPrice), low <= min(open, close), high >= max(open, close) else { return nil }
        }
        guard let low = indices.compactMap({ lows[$0] }).min(), let high = indices.compactMap({ highs[$0] }).max() else { return nil }
        return .init(day: BriefDate.newYork(window.open, format: "yyyy-MM-dd"), low: low, high: high, close: finalClose)
    }

    private static func validPrice(_ price: Double) -> Bool { price.isFinite && price > 0 }

    private struct Envelope: Decodable {
        let chart: Chart
        struct Chart: Decodable { let result: [Series]? }
    }

    private struct Series: Decodable {
        let meta: Meta
        let timestamp: [Int]?
        let indicators: Indicators?

        struct Meta: Decodable {
            let symbol: String
            let instrumentType: String
            let currency: String
            let dataGranularity: String
            let regularMarketPrice: Double?
            let regularMarketTime: Int?
        }

        struct Indicators: Decodable {
            let quote: [Columns]
            struct Columns: Decodable {
                let open: [Double?]?
                let low: [Double?]?
                let high: [Double?]?
                let close: [Double?]?
            }
        }
    }
}

private enum CashSchedule {
    // Official NYSE cash calendar. Uncovered years omit RTH levels instead of guessing.
    // https://www.nyse.com/trade/hours-calendars
    static let supportedYears = [2026, 2027, 2028]
    static let holidays: Set<String> = [
        "2026-01-01", "2026-01-19", "2026-02-16", "2026-04-03", "2026-05-25",
        "2026-06-19", "2026-07-03", "2026-09-07", "2026-11-26", "2026-12-25",
        "2027-01-01", "2027-01-18", "2027-02-15", "2027-03-26", "2027-05-31",
        "2027-06-18", "2027-07-05", "2027-09-06", "2027-11-25", "2027-12-24",
        "2028-01-17", "2028-02-21", "2028-04-14", "2028-05-29", "2028-06-19",
        "2028-07-04", "2028-09-04", "2028-11-23", "2028-12-25",
    ]
    static let earlyCloses: Set<String> = ["2026-11-27", "2026-12-24", "2027-11-26", "2028-07-03", "2028-11-24"]

    static func lastCompleted(before now: Date) -> (open: Date, close: Date)? {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(identifier: "America/New_York")!
        for offset in 0..<8 {
            guard let day = calendar.date(byAdding: .day, value: -offset, to: now),
                  supportedYears.contains(calendar.component(.year, from: day)) else { return nil }
            let weekday = calendar.component(.weekday, from: day)
            let dayString = BriefDate.newYork(day, format: "yyyy-MM-dd")
            if weekday == 1 || weekday == 7 || holidays.contains(dayString) { continue }
            guard let open = calendar.date(bySettingHour: 9, minute: 30, second: 0, of: day),
                  let close = calendar.date(bySettingHour: earlyCloses.contains(dayString) ? 13 : 16, minute: 0, second: 0, of: day) else { return nil }
            if close <= now { return (open, close) }
        }
        return nil
    }
}
