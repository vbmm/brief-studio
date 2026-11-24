import BriefCore
import Foundation
import XCTest
@testable import BriefStudio

final class DistributionTests: XCTestCase {
    func testHTMLExportRoundTripsHostileProseAsData() throws {
        let body = "</script><script>alert(1)</script>\n**NQ — Échantillon**"
        let brief = BriefDocument(title: "Exemple", generatedAt: "2026-11-24T14:20:00Z", text: body)
        let template = "<script type=\"application/octet-stream\">\(BriefFiles.marker)</script>"
        let html = try BriefFiles.html(brief, template: template)
        XCTAssertFalse(html.contains("alert(1)"))
        let encoded = html.split(separator: ">")[1].split(separator: "<")[0]
        let bytes = try XCTUnwrap(Data(base64Encoded: String(encoded)))
        XCTAssertEqual(try BriefFiles.decode(bytes), brief)
    }

    func testUpdatesOnlyLinkToValidatedStableReleasesOnConfiguredGitHubRepo() throws {
        let updater = try ReleaseUpdates(repository: "example-owner/brief-studio")
        func json(tag: String, prerelease: Bool = false, draft: Bool = false) -> Data {
            Data("""
                {"tag_name":"\(tag)","prerelease":\(prerelease),"draft":\(draft),"html_url":"https://example.com/unsafe"}
                """.utf8)
        }
        let newer = try XCTUnwrap(updater.parse(json(tag: "v0.10.0"), installed: "0.9.1"))
        XCTAssertEqual(newer.releasePage.absoluteString, "https://github.com/example-owner/brief-studio/releases/tag/v0.10.0")
        XCTAssertNil(try updater.parse(json(tag: "v0.9.0"), installed: "0.9.1"))
        XCTAssertNil(try updater.parse(json(tag: "v1.0.0", prerelease: true), installed: "0.9.1"))
        XCTAssertNil(try updater.parse(json(tag: "v1.0.0", draft: true), installed: "0.9.1"))
        XCTAssertThrowsError(try updater.parse(json(tag: "../../owner"), installed: "0.9.1"))
        XCTAssertThrowsError(try ReleaseUpdates(repository: "api.github.com@other.example/repo"))
        XCTAssertThrowsError(try ReleaseUpdates(repository: "owner/.."))
    }
}
