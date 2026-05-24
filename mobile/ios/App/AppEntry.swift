// App-target entry point.
//
// SwiftPM library targets can't carry @main (Xcode resolves @main at
// the app-target level only). The library exports AppState + RootView
// as public types; this file is the only place with @main.

import SwiftUI
import ChepherdApp
import ChepherdStyle

@main
struct RootApp: App {
    @State private var appState = AppState()

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(appState)
                .preferredColorScheme(.dark)
                .background(Palette.background)
                .tint(Palette.logo)
        }
    }
}
