// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "ChepherdRC",
    platforms: [
        .iOS(.v17),
        .macOS(.v14),
        .watchOS(.v10),
        .visionOS(.v1)
    ],
    products: [
        .library(name: "ChepherdProtocol", targets: ["ChepherdProtocol"]),
        .library(name: "ChepherdTransport", targets: ["ChepherdTransport"]),
        .library(name: "ChepherdAuth", targets: ["ChepherdAuth"]),
        .library(name: "ChepherdStyle", targets: ["ChepherdStyle"]),
        .library(name: "ChepherdApp", targets: ["ChepherdApp"]),
    ],
    dependencies: [
        // WebRTC is sourced from a binary distribution. Pinned to a known-good
        // tag from Google's build-tools repo; update via dependency rule.
        .package(url: "https://github.com/stasel/WebRTC.git",
                 from: "126.0.0"),
    ],
    targets: [
        .target(
            name: "ChepherdProtocol",
            path: "Sources/ChepherdProtocol"
        ),
        .target(
            name: "ChepherdTransport",
            dependencies: [
                "ChepherdProtocol",
                .product(name: "WebRTC", package: "WebRTC"),
            ],
            path: "Sources/ChepherdTransport"
        ),
        .target(
            name: "ChepherdAuth",
            path: "Sources/ChepherdAuth"
        ),
        .target(
            name: "ChepherdStyle",
            path: "Sources/ChepherdStyle"
        ),
        .target(
            name: "ChepherdApp",
            dependencies: [
                "ChepherdProtocol",
                "ChepherdTransport",
                "ChepherdAuth",
                "ChepherdStyle",
            ],
            path: "Sources/ChepherdApp",
            exclude: [],
            sources: nil
        ),
        .testTarget(
            name: "ChepherdProtocolTests",
            dependencies: ["ChepherdProtocol"],
            path: "Tests/ChepherdProtocolTests"
        ),
    ]
)
