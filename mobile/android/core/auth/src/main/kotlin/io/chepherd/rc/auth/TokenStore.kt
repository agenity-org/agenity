// TokenStore backed by EncryptedSharedPreferences (Android Keystore).
// Mirrors the Keychain-backed TokenStore in chepherd-rc-ios.

package io.chepherd.rc.auth

import android.content.Context
import android.content.SharedPreferences
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKey

class TokenStore(context: Context) {
    private val prefs: SharedPreferences

    init {
        val key = MasterKey.Builder(context)
            .setKeyScheme(MasterKey.KeyScheme.AES256_GCM)
            .build()
        prefs = EncryptedSharedPreferences.create(
            context,
            "chepherd_tokens",
            key,
            EncryptedSharedPreferences.PrefKeyEncryptionScheme.AES256_SIV,
            EncryptedSharedPreferences.PrefValueEncryptionScheme.AES256_GCM,
        )
    }

    fun save(tokens: TokenSet) {
        prefs.edit()
            .putString("access_token", tokens.accessToken)
            .putString("refresh_token", tokens.refreshToken)
            .putLong("expires_at", tokens.expiresAt)
            .also { ed -> tokens.idToken?.let { ed.putString("id_token", it) } }
            .apply()
    }

    fun load(): TokenSet? {
        val at = prefs.getString("access_token", null) ?: return null
        val rt = prefs.getString("refresh_token", null) ?: return null
        val ex = prefs.getLong("expires_at", 0)
        val it = prefs.getString("id_token", null)
        return TokenSet(at, rt, ex, it)
    }

    fun clear() {
        prefs.edit().clear().apply()
    }
}
