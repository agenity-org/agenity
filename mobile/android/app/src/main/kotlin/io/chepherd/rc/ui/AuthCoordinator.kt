// AuthCoordinator — drives the AppAuth-based OAuth2 PKCE flow.
//
// Hands off the actual browser presentation to a Chrome Custom Tab via
// net.openid:appauth. The library handles the authorization-code +
// PKCE challenge round-trip; we own the surrounding state machine
// (verifier persistence + TokenStore wiring).

package io.chepherd.rc.ui

import android.content.Context
import android.content.Intent
import android.net.Uri
import io.chepherd.rc.auth.Auth
import io.chepherd.rc.auth.AuthConfig
import io.chepherd.rc.auth.PKCE
import io.chepherd.rc.auth.TokenSet
import io.chepherd.rc.auth.TokenStore
import kotlinx.coroutines.suspendCancellableCoroutine
import net.openid.appauth.AuthorizationException
import net.openid.appauth.AuthorizationRequest
import net.openid.appauth.AuthorizationResponse
import net.openid.appauth.AuthorizationService
import net.openid.appauth.AuthorizationServiceConfiguration
import net.openid.appauth.ResponseTypeValues
import kotlin.coroutines.resume
import kotlin.coroutines.resumeWithException

sealed class AuthCoordinatorError(msg: String) : RuntimeException(msg) {
    object Cancelled : AuthCoordinatorError("auth: cancelled")
    object MissingCode : AuthCoordinatorError("auth: missing code")
    object StateMismatch : AuthCoordinatorError("auth: state mismatch")
    class Wrapped(cause: Throwable) : AuthCoordinatorError(cause.message ?: "auth error") {
        init { initCause(cause) }
    }
}

class AuthCoordinator(
    private val ctx: Context,
    private val cfg: AuthConfig,
    private val tokenStore: TokenStore,
) {

    private val service = AuthorizationService(ctx)
    private val serviceConfig = AuthorizationServiceConfiguration(
        Uri.parse("${cfg.idpBaseUrl.trimEnd('/')}/realms/openova/protocol/openid-connect/auth"),
        Uri.parse("${cfg.idpBaseUrl.trimEnd('/')}/realms/openova/protocol/openid-connect/token"),
    )

    /** Build an intent the Activity launches to start the browser flow. */
    fun buildAuthIntent(verifier: String): Intent {
        val req = AuthorizationRequest.Builder(
            serviceConfig,
            cfg.clientId,
            ResponseTypeValues.CODE,
            Uri.parse(cfg.redirectUri),
        )
            .setScope(cfg.scope)
            .setCodeVerifier(verifier, PKCE.codeChallengeFromVerifier(verifier), "S256")
            .build()
        return service.getAuthorizationRequestIntent(req)
    }

    /** Handle the redirect Intent that Activity.onCreate received. */
    suspend fun handleRedirect(intent: Intent, verifier: String): TokenSet {
        val resp = AuthorizationResponse.fromIntent(intent)
        val ex = AuthorizationException.fromIntent(intent)
        when {
            ex != null -> {
                if (ex.code == AuthorizationException.GeneralErrors.USER_CANCELED_AUTH_FLOW.code) {
                    throw AuthCoordinatorError.Cancelled
                }
                throw AuthCoordinatorError.Wrapped(ex)
            }
            resp == null -> throw AuthCoordinatorError.MissingCode
            resp.authorizationCode.isNullOrEmpty() -> throw AuthCoordinatorError.MissingCode
        }
        val tokens = Auth.exchangeAuthCode(
            cfg = cfg,
            code = resp!!.authorizationCode!!,
            verifier = verifier,
        )
        tokenStore.save(tokens)
        return tokens
    }

    fun dispose() {
        service.dispose()
    }

    suspend fun signIn(launcher: ActivityResultLikeLauncher): TokenSet {
        val verifier = PKCE.newCodeVerifier()
        val intent = buildAuthIntent(verifier)
        val result = suspendCancellableCoroutine<Intent?> { cont ->
            launcher.launch(intent) { resultIntent -> cont.resume(resultIntent) }
            cont.invokeOnCancellation { /* nothing extra */ }
        }
        if (result == null) throw AuthCoordinatorError.Cancelled
        return handleRedirect(result, verifier)
    }
}

/** Activity-agnostic launcher abstraction. The Compose layer adapts
 *  ActivityResultLauncher<Intent> to this interface. */
fun interface ActivityResultLikeLauncher {
    fun launch(intent: Intent, onResult: (Intent?) -> Unit)
}
