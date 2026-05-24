package io.chepherd.rc.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import io.chepherd.rc.style.ChepherdFont
import io.chepherd.rc.style.ChepherdSpace
import io.chepherd.rc.style.Palette
import io.chepherd.rc.transport.TransportState
import io.chepherd.rc.viewmodel.SessionStore

@Composable
fun DashboardScreen(store: SessionStore? = null) {
    val sessions = store?.sessions?.collectAsState()?.value ?: emptyList()
    val state = store?.state?.collectAsState()?.value ?: TransportState.Idle

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(Palette.background)
            .padding(ChepherdSpace.s4),
        verticalArrangement = Arrangement.spacedBy(ChepherdSpace.s3),
    ) {
        Row(verticalAlignment = androidx.compose.ui.Alignment.CenterVertically) {
            Text(
                text = "SESSIONS",
                color = Palette.title,
                style = ChepherdFont.titleMono.copy(fontWeight = FontWeight.Bold),
            )
            Spacer(modifier = Modifier.weight(1f))
            store?.let {
                Text(
                    text = "${it.kind.name.lowercase()}/${state.name.lowercase()}",
                    color = Palette.body,
                    style = ChepherdFont.bodyMono.copy(fontSize = ChepherdFont.xs),
                )
            }
        }
        if (sessions.isEmpty()) {
            Text(
                text = connectionStatusText(state, store == null),
                color = Palette.timestamp,
                style = ChepherdFont.bodyMono.copy(fontSize = ChepherdFont.sm),
            )
        } else {
            val verdicts = store?.verdictHistory?.collectAsState()?.value ?: emptyList()
            LazyColumn(verticalArrangement = Arrangement.spacedBy(ChepherdSpace.s2)) {
                items(sessions, key = { it.uuid }) { s ->
                    SessionRow(
                        session = s,
                        verdictHistory = verdicts.filter { it.session_uuid == s.uuid },
                    )
                }
            }
        }
    }
}

private fun connectionStatusText(state: TransportState, missingStore: Boolean): String {
    if (missingStore) return "preparing connection…"
    return when (state) {
        TransportState.Idle, TransportState.Connecting -> "connecting…"
        TransportState.Open -> "no sessions yet"
        TransportState.Closing, TransportState.Closed -> "disconnected"
    }
}
