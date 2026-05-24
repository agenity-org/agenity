#if canImport(SwiftUI)
import SwiftUI
import ChepherdStyle
import ChepherdAuth

extension AuthCoordinatorError: Equatable {
    public static func == (lhs: AuthCoordinatorError, rhs: AuthCoordinatorError) -> Bool {
        switch (lhs, rhs) {
        case (.cancelled, .cancelled),
             (.missingCode, .missingCode),
             (.missingState, .missingState),
             (.stateMismatch, .stateMismatch),
             (.callbackURLInvalid, .callbackURLInvalid):
            return true
        default:
            return false
        }
    }
}

struct SignInView: View {
    @Environment(AppState.self) private var app
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        VStack(alignment: .leading, spacing: ChepherdSpace.s4) {
            Text("chepherd")
                .font(ChepherdFont.mono(ChepherdFont.xxl, weight: .bold))
                .foregroundColor(Palette.logo)
            Text("Sign in to view your sessions from anywhere.")
                .font(ChepherdFont.mono(ChepherdFont.base))
                .foregroundColor(Palette.body)
            Button(action: signIn) {
                Text(busy ? "redirecting…" : "Sign in with OpenOva")
                    .font(ChepherdFont.mono(ChepherdFont.base, weight: .bold))
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, ChepherdSpace.s3)
            }
            .buttonStyle(.borderedProminent)
            .tint(Palette.logo)
            .foregroundStyle(Palette.background)
            .disabled(busy)

            if let error {
                Text(error)
                    .font(ChepherdFont.mono(ChepherdFont.sm))
                    .foregroundColor(Palette.apiError)
            }

            Spacer()

            Text("chepherd-rc encrypts every byte. By default the daemon and your device talk peer-to-peer via WebRTC. Your data is your data.")
                .font(ChepherdFont.mono(ChepherdFont.sm))
                .foregroundColor(Palette.timestamp)
        }
        .padding(ChepherdSpace.s6)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .background(Palette.background)
    }

    private func signIn() {
        busy = true
        error = nil
        Task { @MainActor in
            do {
                let cfg = AuthConfig(
                    idpBaseURL: URL(string: "https://id.openova.io")!,
                    clientID: "chepherd-rc-ios",
                    redirectURI: URL(string: "io.chepherd.rc://callback")!,
                    scope: "openid profile email chepherd:rc"
                )
                let store = TokenStore()
                let coord = AuthCoordinator(cfg: cfg, tokenStore: store)
                _ = try await coord.signIn()
                app.signedIn = true
            } catch let e as AuthCoordinatorError where e == .cancelled {
                // User cancelled — just reset the busy flag.
            } catch {
                self.error = String(describing: error)
            }
            busy = false
        }
    }
}
#endif
