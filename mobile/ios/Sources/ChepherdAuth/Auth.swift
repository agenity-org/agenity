// OAuth2 authorization-code flow with PKCE (RFC 6749 + RFC 7636).
// Swift mirror of chepherd-rc-web/src/auth/auth.ts.
//
// The UI shell launches the flow via ASWebAuthenticationSession; this
// file owns the protocol + token storage. ASWebAuthenticationSession
// itself lives in the ChepherdApp target since it needs a presentation
// context.

#if canImport(Foundation)
import Foundation

public struct AuthConfig: Sendable {
    public let idpBaseURL: URL
    public let clientID: String
    public let redirectURI: URL
    public let scope: String

    public init(idpBaseURL: URL, clientID: String, redirectURI: URL, scope: String) {
        self.idpBaseURL = idpBaseURL
        self.clientID = clientID
        self.redirectURI = redirectURI
        self.scope = scope
    }
}

public struct TokenSet: Codable, Sendable {
    public let accessToken: String
    public let refreshToken: String
    public let expiresAt: Date
    public let idToken: String?

    enum CodingKeys: String, CodingKey {
        case accessToken = "access_token"
        case refreshToken = "refresh_token"
        case expiresAt = "expires_at"
        case idToken = "id_token"
    }
}

public enum AuthError: Error, Sendable {
    case missingParams
    case stateMismatch
    case exchangeFailed(status: Int, body: String)
    case decodeFailed(String)
}

/// Build the authorize URL — the UI navigates here in an
/// ASWebAuthenticationSession. The state + verifier returned must be
/// stashed in the TokenStore so the callback can complete the exchange.
public func buildAuthorizeURL(
    cfg: AuthConfig,
    challenge: String,
    state: String
) -> URL {
    var components = URLComponents(
        url: cfg.idpBaseURL.appendingPathComponent("/realms/openova/protocol/openid-connect/auth"),
        resolvingAgainstBaseURL: false
    )!
    components.queryItems = [
        URLQueryItem(name: "response_type", value: "code"),
        URLQueryItem(name: "client_id", value: cfg.clientID),
        URLQueryItem(name: "redirect_uri", value: cfg.redirectURI.absoluteString),
        URLQueryItem(name: "scope", value: cfg.scope),
        URLQueryItem(name: "state", value: state),
        URLQueryItem(name: "code_challenge", value: challenge),
        URLQueryItem(name: "code_challenge_method", value: "S256"),
    ]
    return components.url!
}

/// Exchange the auth code for a TokenSet at the IdP token endpoint.
public func exchangeAuthCode(
    cfg: AuthConfig,
    code: String,
    verifier: String,
    session: URLSession = .shared
) async throws -> TokenSet {
    let tokenURL = cfg.idpBaseURL.appendingPathComponent(
        "/realms/openova/protocol/openid-connect/token"
    )
    var req = URLRequest(url: tokenURL)
    req.httpMethod = "POST"
    req.setValue("application/x-www-form-urlencoded", forHTTPHeaderField: "Content-Type")
    var body = URLComponents()
    body.queryItems = [
        URLQueryItem(name: "grant_type", value: "authorization_code"),
        URLQueryItem(name: "code", value: code),
        URLQueryItem(name: "redirect_uri", value: cfg.redirectURI.absoluteString),
        URLQueryItem(name: "client_id", value: cfg.clientID),
        URLQueryItem(name: "code_verifier", value: verifier),
    ]
    req.httpBody = body.percentEncodedQuery?.data(using: .utf8)

    let (data, resp) = try await session.data(for: req)
    guard let http = resp as? HTTPURLResponse, http.statusCode == 200 else {
        let status = (resp as? HTTPURLResponse)?.statusCode ?? -1
        let bodyText = String(data: data, encoding: .utf8) ?? ""
        throw AuthError.exchangeFailed(status: status, body: bodyText)
    }

    struct Resp: Codable {
        let access_token: String
        let refresh_token: String
        let expires_in: Int
        let id_token: String?
    }
    do {
        let parsed = try JSONDecoder().decode(Resp.self, from: data)
        return TokenSet(
            accessToken: parsed.access_token,
            refreshToken: parsed.refresh_token,
            expiresAt: Date().addingTimeInterval(TimeInterval(parsed.expires_in - 30)),
            idToken: parsed.id_token
        )
    } catch {
        throw AuthError.decodeFailed(String(describing: error))
    }
}

/// Refresh an access token using the refresh_token grant.
public func refreshAccessToken(
    cfg: AuthConfig,
    refreshToken: String,
    session: URLSession = .shared
) async throws -> TokenSet {
    let tokenURL = cfg.idpBaseURL.appendingPathComponent(
        "/realms/openova/protocol/openid-connect/token"
    )
    var req = URLRequest(url: tokenURL)
    req.httpMethod = "POST"
    req.setValue("application/x-www-form-urlencoded", forHTTPHeaderField: "Content-Type")
    var body = URLComponents()
    body.queryItems = [
        URLQueryItem(name: "grant_type", value: "refresh_token"),
        URLQueryItem(name: "refresh_token", value: refreshToken),
        URLQueryItem(name: "client_id", value: cfg.clientID),
    ]
    req.httpBody = body.percentEncodedQuery?.data(using: .utf8)

    let (data, resp) = try await session.data(for: req)
    guard let http = resp as? HTTPURLResponse, http.statusCode == 200 else {
        let status = (resp as? HTTPURLResponse)?.statusCode ?? -1
        let bodyText = String(data: data, encoding: .utf8) ?? ""
        throw AuthError.exchangeFailed(status: status, body: bodyText)
    }
    struct Resp: Codable {
        let access_token: String
        let refresh_token: String
        let expires_in: Int
        let id_token: String?
    }
    let parsed = try JSONDecoder().decode(Resp.self, from: data)
    return TokenSet(
        accessToken: parsed.access_token,
        refreshToken: parsed.refresh_token,
        expiresAt: Date().addingTimeInterval(TimeInterval(parsed.expires_in - 30)),
        idToken: parsed.id_token
    )
}
#endif
