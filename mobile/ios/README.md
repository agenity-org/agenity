# chepherd-rc-ios

> **Status:** Living · pre-release **v0.2.0-rc3** · App Store submission pending
> **Component line:** chepherd-rc client (versioned independently from the chepherd daemon, which is at v0.9.2)
> **Audience:** iOS contributors building/sideloading the chepherd-rc mobile client

[![ios-build](https://github.com/chepherd/chepherd-rc-ios/actions/workflows/ios-build.yml/badge.svg)](https://github.com/chepherd/chepherd-rc-ios/actions/workflows/ios-build.yml)
[![release](https://img.shields.io/github/v/tag/chepherd/chepherd-rc-ios?label=release&sort=semver)](https://github.com/chepherd/chepherd-rc-ios/releases)

Native iOS client for chepherd-rc. Swift 6, SwiftUI, WidgetKit.

**Privacy by default** — pairs to your bastion daemon over WebRTC DataChannel. Relay only sees the signaling handshake. Your data is your data.

## Project layout

```
chepherd-rc-ios/
├── Package.swift                          # Swift Package manifest
├── Sources/
│   ├── ChepherdProtocol/                  # Wire shapes — mirrors
│   │   ├── Envelope.swift                 #   chepherd-rc-web/src/protocol/
│   │   ├── Payloads.swift                 #   envelope.ts + payloads.ts
│   │   └── SequenceCounter.swift          #   Reconnect-resume support
│   ├── ChepherdTransport/                 # Transport abstractions
│   │   ├── Transport.swift                #   Common protocol
│   │   ├── WSTransport.swift              #   URLSessionWebSocketTask
│   │   ├── WebRTCTransport.swift          #   google-webrtc binding
│   │   ├── SignalingClient.swift          #   HTTP signaling
│   │   └── Factory.swift                  #   auto / p2p / relayed
│   ├── ChepherdStyle/                     # Design system in Swift
│   │   ├── Palette.swift                  #   k9s palette tokens
│   │   ├── Typography.swift               #   font scale + weights
│   │   └── Spacing.swift                  #   doubling scale
│   ├── ChepherdAuth/                      # OAuth2 PKCE
│   │   ├── PKCE.swift                     #   verifier + challenge
│   │   ├── Auth.swift                     #   ASWebAuthenticationSession
│   │   └── TokenStore.swift               #   Keychain-backed cache
│   ├── ChepherdApp/                       # App entry + views
│   │   ├── ChepherdRCApp.swift            #   @main
│   │   ├── Views/
│   │   │   ├── SignInView.swift
│   │   │   ├── DashboardView.swift
│   │   │   ├── SessionRow.swift
│   │   │   ├── SessionDetailView.swift
│   │   │   ├── ScorecardView.swift
│   │   │   ├── BandDotView.swift
│   │   │   └── SparklineView.swift
│   │   └── ViewModels/
│   │       └── SessionStore.swift         #   @Observable wrapper
│   └── ChepherdWidget/                    # WidgetKit complications
│       └── ChepherdWidget.swift           #   Lock-screen + home widget
└── Tests/
    └── ChepherdProtocolTests/
        └── EnvelopeTests.swift
```

## Building

```bash
# One-time on a Mac with Xcode 16+ installed:
brew install xcodegen
xcodegen                              # generates chepherd-rc.xcodeproj
open chepherd-rc.xcodeproj            # opens in Xcode

# Or, library-only (no app target — useful for unit tests / CI):
open Package.swift
```

Sideloading to a real device:

1. Open `chepherd-rc.xcodeproj` in Xcode 16+
2. Select the `chepherd-rc` scheme + an attached iPhone (iOS 17+)
3. Signing → set Team + bundle ID (the default `io.chepherd.rc` is
   reserved; use your own for sideloading)
4. ⌘R — the app launches with the sign-in screen

The OAuth2 callback scheme `io.chepherd.rc://callback` is pre-registered
in `App/Info.plist`, so the ASWebAuthenticationSession round-trip
completes cleanly on first launch.

## Privacy contract

Same as the web client + the TUI: WebRTC DataChannel is the default; the user can opt into relayed mode for low-trust networks. No content is ever logged to disk in plain text.

## Design system

Mirrors `chepherd/docs/DESIGN-SYSTEM.md`. The Swift palette tokens live in `Sources/ChepherdStyle/Palette.swift` — every hex value comes from the canon mirror in `chepherd/internal/style/palette.go`.

## Status

**v0.2.0-rc3** (chepherd-rc client line) — buildable iOS app with full sign-in
(ASWebAuthenticationSession + Keychain TokenStore) + WSTransport relayed-mode
session list + SwiftUI dashboard mirroring the chepherd TUI palette. WebRTC
peer-to-peer mode + WidgetKit lock-screen complications are the next follow-ups.

App Store submission pending:
  - App icon assets (1024x1024 + sized variants)
  - Privacy policy URL
  - Screenshot bundle (5 sizes per device class)
  - TestFlight beta cycle
