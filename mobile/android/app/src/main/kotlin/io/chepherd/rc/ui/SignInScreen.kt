package io.chepherd.rc.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import io.chepherd.rc.auth.AuthConfig
import io.chepherd.rc.auth.TokenStore
import io.chepherd.rc.style.ChepherdFont
import io.chepherd.rc.style.ChepherdSpace
import io.chepherd.rc.style.Palette
import kotlinx.coroutines.launch

@Composable
fun SignInScreen(onSignedIn: () -> Unit) {
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    val ctx = LocalContext.current
    val launcher = LocalAuthLauncher.current
    val scope = rememberCoroutineScope()

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(Palette.background)
            .padding(ChepherdSpace.s6),
        verticalArrangement = Arrangement.spacedBy(ChepherdSpace.s4),
        horizontalAlignment = Alignment.Start,
    ) {
        Text(
            text = "chepherd",
            color = Palette.logo,
            fontSize = ChepherdFont.xxl,
            fontWeight = FontWeight.Bold,
            style = ChepherdFont.bodyMono.copy(color = Palette.logo, fontSize = ChepherdFont.xxl, fontWeight = FontWeight.Bold),
        )
        Text(
            text = "Sign in to view your sessions from anywhere.",
            color = Palette.body,
            style = ChepherdFont.bodyMono,
        )

        Button(
            onClick = {
                busy = true
                error = null
                scope.launch {
                    try {
                        val cfg = AuthConfig(
                            idpBaseUrl = "https://id.openova.io",
                            clientId = "chepherd-rc-android",
                            redirectUri = "io.chepherd.rc://callback",
                            scope = "openid profile email chepherd:rc",
                        )
                        val store = TokenStore(ctx)
                        val coord = AuthCoordinator(ctx, cfg, store)
                        coord.signIn(launcher)
                        coord.dispose()
                        onSignedIn()
                    } catch (e: AuthCoordinatorError.Cancelled) {
                        // user cancelled — no-op
                    } catch (e: Throwable) {
                        error = e.message ?: e::class.simpleName
                    } finally {
                        busy = false
                    }
                }
            },
            enabled = !busy,
            colors = ButtonDefaults.buttonColors(containerColor = Palette.logo, contentColor = Palette.background),
            modifier = Modifier.fillMaxWidth(),
        ) {
            Text(
                if (busy) "redirecting…" else "Sign in with OpenOva",
                style = ChepherdFont.bodyMono.copy(fontWeight = FontWeight.Bold),
            )
        }

        error?.let {
            Text(
                text = it,
                color = Palette.apiError,
                style = ChepherdFont.bodyMono.copy(fontSize = ChepherdFont.sm),
            )
        }

        Spacer(Modifier.weight(1f))

        Text(
            text = "chepherd-rc encrypts every byte. By default the daemon and your device talk peer-to-peer via WebRTC. Your data is your data.",
            color = Palette.timestamp,
            style = ChepherdFont.bodyMono.copy(fontSize = ChepherdFont.sm),
        )
    }
}
