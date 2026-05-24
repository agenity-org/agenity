// AuthCoordinator — drives the ASWebAuthenticationSession flow.
//
// On iOS this presents a system-provided web view (SFAuthenticationSession
// under the hood) so the user can complete the OAuth2 dance against
// OpenOva's identity provider. On macOS the same API works.

#if canImport(AuthenticationServices)
import Foundation
import AuthenticationServices
import ChepherdAuth

/// Errors specific to the coordinator (web auth session); auth-protocol
/// errors are passed through from ChepherdAuth.AuthError.
public enum AuthCoordinatorError: Error {
    case cancelled
    case missingCode
    case missingState
    case stateMismatch
    case callbackURLInvalid
}

@MainActor
public final class AuthCoordinator: NSObject, ASWebAuthenticationPresentationContextProviding {
    private let cfg: AuthConfig
    private let tokenStore: TokenStore

    public init(cfg: AuthConfig, tokenStore: TokenStore) {
        self.cfg = cfg
        self.tokenStore = tokenStore
    }

    /// Run the full PKCE round-trip and persist the resulting tokens.
    public func signIn() async throws -> TokenSet {
        let verifier = ChepherdAuth.PKCE.newCodeVerifier()
        let challenge = ChepherdAuth.PKCE.codeChallenge(from: verifier)
        let state = ChepherdAuth.PKCE.newStateNonce()
        let authURL = buildAuthorizeURL(cfg: cfg, challenge: challenge, state: state)

        let scheme = cfg.redirectURI.scheme ?? "https"
        let callbackURL = try await withCheckedThrowingContinuation { (cont: CheckedContinuation<URL, Error>) in
            let session = ASWebAuthenticationSession(
                url: authURL,
                callbackURLScheme: scheme
            ) { url, err in
                if let err {
                    if let asErr = err as? ASWebAuthenticationSessionError, asErr.code == .canceledLogin {
                        cont.resume(throwing: AuthCoordinatorError.cancelled)
                    } else {
                        cont.resume(throwing: err)
                    }
                    return
                }
                guard let url else {
                    cont.resume(throwing: AuthCoordinatorError.callbackURLInvalid)
                    return
                }
                cont.resume(returning: url)
            }
            session.presentationContextProvider = self
            session.prefersEphemeralWebBrowserSession = false
            if !session.start() {
                cont.resume(throwing: AuthCoordinatorError.callbackURLInvalid)
            }
        }

        // Parse the callback URL for code + state.
        guard let comps = URLComponents(url: callbackURL, resolvingAgainstBaseURL: false) else {
            throw AuthCoordinatorError.callbackURLInvalid
        }
        let returnedState = comps.queryItems?.first(where: { $0.name == "state" })?.value
        let code = comps.queryItems?.first(where: { $0.name == "code" })?.value

        guard returnedState == state else {
            throw AuthCoordinatorError.stateMismatch
        }
        guard let code else {
            throw AuthCoordinatorError.missingCode
        }

        let tokens = try await exchangeAuthCode(cfg: cfg, code: code, verifier: verifier)
        try await tokenStore.save(tokens)
        return tokens
    }

    public nonisolated func presentationAnchor(for session: ASWebAuthenticationSession) -> ASPresentationAnchor {
        // ASWebAuthenticationPresentationContextProviding requires a
        // nonisolated conformance, but UIApplication.shared and
        // friends are @MainActor-isolated under Swift 6.
        // MainActor.assumeIsolated bridges the gap: Apple's SDK
        // guarantees this method is called on the main thread.
        return MainActor.assumeIsolated {
            #if canImport(UIKit)
            if let scene = UIApplication.shared.connectedScenes.first(where: { $0.activationState == .foregroundActive }) as? UIWindowScene,
               let window = scene.windows.first(where: { $0.isKeyWindow }) ?? scene.windows.first {
                return window
            }
            #endif
            return ASPresentationAnchor()
        }
    }
}

#if canImport(UIKit)
import UIKit
#endif
#endif
