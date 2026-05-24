// OAuth2 PKCE protocol — Kotlin mirror of TS + Swift.
// The Activity-level glue (AppAuth integration) lives in :app since it
// needs an Activity context. This file owns the wire protocol +
// TokenStore + buildAuthorizeUri helpers.

package io.chepherd.rc.auth

import android.net.Uri
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import okhttp3.FormBody
import okhttp3.OkHttpClient
import okhttp3.Request
import org.json.JSONObject

data class AuthConfig(
    val idpBaseUrl: String,
    val clientId: String,
    val redirectUri: String,
    val scope: String,
)

data class TokenSet(
    val accessToken: String,
    val refreshToken: String,
    /** epoch seconds at which the access token expires (30s safety margin already applied). */
    val expiresAt: Long,
    val idToken: String?,
)

sealed class AuthError(msg: String) : RuntimeException(msg) {
    object MissingParams : AuthError("auth: missing OAuth2 params")
    object StateMismatch : AuthError("auth: state mismatch")
    class ExchangeFailed(status: Int, body: String) : AuthError("auth: exchange failed status=$status body=$body")
    class DecodeFailed(reason: String) : AuthError("auth: decode failed: $reason")
}

object Auth {
    fun buildAuthorizeUri(cfg: AuthConfig, challenge: String, state: String): Uri {
        return Uri.parse(cfg.idpBaseUrl)
            .buildUpon()
            .appendEncodedPath("realms/openova/protocol/openid-connect/auth")
            .appendQueryParameter("response_type", "code")
            .appendQueryParameter("client_id", cfg.clientId)
            .appendQueryParameter("redirect_uri", cfg.redirectUri)
            .appendQueryParameter("scope", cfg.scope)
            .appendQueryParameter("state", state)
            .appendQueryParameter("code_challenge", challenge)
            .appendQueryParameter("code_challenge_method", "S256")
            .build()
    }

    suspend fun exchangeAuthCode(
        cfg: AuthConfig,
        code: String,
        verifier: String,
        http: OkHttpClient = OkHttpClient(),
    ): TokenSet = withContext(Dispatchers.IO) {
        val body = FormBody.Builder()
            .add("grant_type", "authorization_code")
            .add("code", code)
            .add("redirect_uri", cfg.redirectUri)
            .add("client_id", cfg.clientId)
            .add("code_verifier", verifier)
            .build()
        val req = Request.Builder()
            .url("${cfg.idpBaseUrl.trimEnd('/')}/realms/openova/protocol/openid-connect/token")
            .post(body)
            .build()
        http.newCall(req).execute().use { resp ->
            val text = resp.body?.string() ?: ""
            if (!resp.isSuccessful) {
                throw AuthError.ExchangeFailed(resp.code, text)
            }
            decodeTokenJson(text)
        }
    }

    suspend fun refreshAccessToken(
        cfg: AuthConfig,
        refreshToken: String,
        http: OkHttpClient = OkHttpClient(),
    ): TokenSet = withContext(Dispatchers.IO) {
        val body = FormBody.Builder()
            .add("grant_type", "refresh_token")
            .add("refresh_token", refreshToken)
            .add("client_id", cfg.clientId)
            .build()
        val req = Request.Builder()
            .url("${cfg.idpBaseUrl.trimEnd('/')}/realms/openova/protocol/openid-connect/token")
            .post(body)
            .build()
        http.newCall(req).execute().use { resp ->
            val text = resp.body?.string() ?: ""
            if (!resp.isSuccessful) {
                throw AuthError.ExchangeFailed(resp.code, text)
            }
            decodeTokenJson(text)
        }
    }

    private fun decodeTokenJson(text: String): TokenSet {
        return try {
            val j = JSONObject(text)
            TokenSet(
                accessToken = j.getString("access_token"),
                refreshToken = j.getString("refresh_token"),
                expiresAt = System.currentTimeMillis() / 1000 + j.getInt("expires_in") - 30,
                idToken = if (j.isNull("id_token") || !j.has("id_token")) null else j.optString("id_token"),
            )
        } catch (t: Throwable) {
            throw AuthError.DecodeFailed(t.message ?: t::class.simpleName ?: "unknown")
        }
    }
}
