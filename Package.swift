// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "BriefStudio",
    platforms: [.macOS(.v14)],
    products: [.executable(name: "BriefStudio", targets: ["BriefStudio"])],
    targets: [
        .target(name: "BriefCore"),
        .target(name: "BriefAuth"),
        .executableTarget(name: "BriefStudio", dependencies: ["BriefCore", "BriefAuth"]),
        .testTarget(name: "BriefStudioTests", dependencies: ["BriefStudio", "BriefCore"]),
        .testTarget(name: "BriefCoreTests", dependencies: ["BriefCore"]),
        .testTarget(name: "BriefAuthTests", dependencies: ["BriefAuth"]),
    ]
)
