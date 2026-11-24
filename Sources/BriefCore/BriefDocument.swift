import Foundation

public struct BriefDocument: Codable, Equatable, Sendable {
    public static let currentSchema = "nq-es-brief/v1"

    public struct Source: Codable, Equatable, Sendable {
        public let label: String
        public let url: String

        public init(label: String, url: String) {
            self.label = label
            self.url = url
        }
    }

    public let schema: String
    public let title: String
    public let generatedAt: String
    public let text: String
    public let sources: [Source]?

    public init(title: String, generatedAt: String, text: String, sources: [Source]? = nil) {
        self.schema = Self.currentSchema
        self.title = title
        self.generatedAt = generatedAt
        self.text = text
        self.sources = sources
    }

    public static let sample = BriefDocument(
        title: "Brief NQ / ES · Exemple", generatedAt: "2026-11-24T13:30:00Z",
        text: """
        ## Lecture du jour

        Exemple avec données fictives. Les deux contrats sont examinés séparément. Les prix et scénarios ci-dessous illustrent la présentation.

        ## NQ · Nasdaq 100

        - **Dernière observation** 20 100,25 à 08:20 ET
        - **Séance RTH du 23 novembre** bas 19 955,00 · haut 20 185,50 · clôture 20 090,00
        - **Vue** Attente dans la fourchette de référence.
        - **À surveiller** La réaction aux bornes de la séance précédente.

        ## ES · S&P 500

        - **Dernière observation** 5 925,75 à 08:20 ET
        - **Séance RTH du 23 novembre** bas 5 860,50 · haut 5 921,25 · clôture 5 914,25
        - **Vue** Hypothèse haussière au-dessus de la fourchette de référence.
        - **À surveiller** L'acceptation ou le retour dans la fourchette précédente.

        ## Contexte et limites

        L'application identifie la source et l'heure de la dernière observation. GEX indisponible. Analyse descriptive sans ordre ni objectif de prix.
        """
    )
}

enum BriefDate {
    static func iso(_ date: Date) -> String { ISO8601DateFormatter().string(from: date) }

    static func newYork(_ date: Date, format: String) -> String {
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "en_US_POSIX")
        formatter.timeZone = TimeZone(identifier: "America/New_York")
        formatter.dateFormat = format
        return formatter.string(from: date)
    }
}
