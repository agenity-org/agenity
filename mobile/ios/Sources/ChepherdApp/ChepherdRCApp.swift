// Application lifecycle types — AppState + RootView. The @main App
// struct lives in the app target (App/AppEntry.swift) so SwiftPM
// libraries don't carry a competing entry point.

#if canImport(SwiftUI)
import SwiftUI

@Observable
public final class AppState {
    public var signedIn: Bool = false
    public var bastionID: String = ""

    public init() {}
}

public struct RootView: View {
    @Environment(AppState.self) private var app

    public init() {}

    public var body: some View {
        if app.signedIn {
            DashboardView()
        } else {
            SignInView()
        }
    }
}
#endif
