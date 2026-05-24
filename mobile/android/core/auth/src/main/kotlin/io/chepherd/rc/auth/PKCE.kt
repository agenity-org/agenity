// PKCE (Proof Key for Code Exchange) — RFC 7636. Kotlin mirror of
// chepherd-rc-web/src/auth/pkce.ts and the Swift PKCE in chepherd-rc-ios.

package io.chepherd.rc.auth

import android.util.Base64
import java.security.MessageDigest
import java.security.SecureRandom

object PKCE {
    private val random = SecureRandom()

    fun base64UrlEncode(bytes: ByteArray): String =
        Base64.encodeToString(
            bytes,
            Base64.URL_SAFE or Base64.NO_PADDING or Base64.NO_WRAP,
        )

    fun newCodeVerifier(byteCount: Int = 32): String {
        val buf = ByteArray(byteCount)
        random.nextBytes(buf)
        return base64UrlEncode(buf)
    }

    fun codeChallengeFromVerifier(verifier: String): String {
        val digest = MessageDigest.getInstance("SHA-256").digest(verifier.toByteArray(Charsets.US_ASCII))
        return base64UrlEncode(digest)
    }

    fun newStateNonce(byteCount: Int = 16): String {
        val buf = ByteArray(byteCount)
        random.nextBytes(buf)
        return base64UrlEncode(buf)
    }
}
