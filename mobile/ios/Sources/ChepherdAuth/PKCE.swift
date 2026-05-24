// PKCE (Proof Key for Code Exchange) — RFC 7636.
// Swift mirror of chepherd-rc-web/src/auth/pkce.ts.

import Foundation
import CryptoKit

public enum PKCE {
    public static func base64UrlEncode(_ data: Data) -> String {
        data.base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }

    public static func newCodeVerifier(byteCount: Int = 32) -> String {
        var bytes = [UInt8](repeating: 0, count: byteCount)
        _ = SecRandomCopyBytes(kSecRandomDefault, byteCount, &bytes)
        return base64UrlEncode(Data(bytes))
    }

    public static func codeChallenge(from verifier: String) -> String {
        let data = Data(verifier.utf8)
        let digest = SHA256.hash(data: data)
        return base64UrlEncode(Data(digest))
    }

    public static func newStateNonce(byteCount: Int = 16) -> String {
        var bytes = [UInt8](repeating: 0, count: byteCount)
        _ = SecRandomCopyBytes(kSecRandomDefault, byteCount, &bytes)
        return base64UrlEncode(Data(bytes))
    }
}
