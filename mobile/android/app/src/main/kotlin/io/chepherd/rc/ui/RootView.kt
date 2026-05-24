package io.chepherd.rc.ui

import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.platform.LocalContext
import io.chepherd.rc.auth.TokenStore
import io.chepherd.rc.transport.TransportState
import io.chepherd.rc.transport.WSTransport
import io.chepherd.rc.viewmodel.SessionStore
import kotlinx.coroutines.delay
import org.json.JSONObject
import kotlin.math.min
import kotlin.math.pow

@Composable
fun RootView() {
    val ctx = LocalContext.current
    var signedIn by remember { mutableStateOf(TokenStore(ctx).load() != null) }
    var store by remember { mutableStateOf<SessionStore?>(null) }

    LaunchedEffect(signedIn) {
        if (!signedIn) return@LaunchedEffect
        var attempt = 0
        while (true) {
            val current = store
            if (current == null || current.state.value == TransportState.Closed) {
                val tokens = TokenStore(ctx).load() ?: return@LaunchedEffect
                val bastion = bastionFromJwt(tokens.accessToken) ?: "primary"
                val transport = WSTransport(
                    url = "wss://relay.chepherd.org/v1/signaling/ws",
                    authToken = tokens.accessToken,
                    bastionId = bastion,
                )
                val fresh = SessionStore(transport)
                fresh.connect()
                store = fresh
                if (fresh.state.value == TransportState.Open) attempt = 0
            }
            val state = store?.state?.value
            if (state == TransportState.Open) {
                attempt = 0
                delay(5000)
            } else {
                val delayMs = min(30_000.0, 2.0.pow(attempt) * 1000.0).toLong()
                attempt += 1
                delay(delayMs)
            }
        }
    }

    DisposableEffect(Unit) {
        onDispose {
            // Coroutines are scoped to the ViewModel; nothing extra to clean up.
        }
    }

    if (signedIn) {
        DashboardScreen(store = store)
    } else {
        SignInScreen(onSignedIn = { signedIn = true })
    }
}

private fun bastionFromJwt(jwt: String): String? {
    val parts = jwt.split(".")
    if (parts.size < 2) return null
    val payload = parts[1].padEnd((parts[1].length + 3) / 4 * 4, '=')
        .replace('-', '+').replace('_', '/')
    val data = try {
        android.util.Base64.decode(payload, android.util.Base64.DEFAULT)
    } catch (_: Throwable) { return null }
    val obj = try { JSONObject(String(data)) } catch (_: Throwable) { return null }
    return optStr(obj, "chepherd_bastion") ?: optStr(obj, "bid")
}

private fun optStr(obj: JSONObject, key: String): String? {
    if (!obj.has(key) || obj.isNull(key)) return null
    val v = obj.optString(key)
    return v.ifEmpty { null }
}
