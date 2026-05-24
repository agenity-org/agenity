package io.chepherd.rc.ui

import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import io.chepherd.rc.protocol.SessionState
import io.chepherd.rc.protocol.VerdictPayload
import io.chepherd.rc.style.ChepherdFont
import io.chepherd.rc.style.ChepherdSpace
import io.chepherd.rc.style.Palette

@Composable
fun SessionRow(
    session: SessionState,
    verdictHistory: List<VerdictPayload> = emptyList(),
) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = ChepherdSpace.s3, vertical = ChepherdSpace.s2),
        horizontalArrangement = Arrangement.spacedBy(ChepherdSpace.s3),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        BandDot(band = session.trust_band?.name, paused = session.paused)
        Column(
            modifier = Modifier.weight(1f),
            verticalArrangement = Arrangement.spacedBy(ChepherdSpace.s1),
        ) {
            Text(text = session.tmux_name, style = ChepherdFont.bodyMono.copy(color = Palette.primary))
            session.repo?.let {
                Text(text = it, style = ChepherdFont.bodyMono.copy(color = Palette.issueRef, fontSize = ChepherdFont.sm))
            }
        }
        session.last_scorecard?.let { s ->
            Text(
                text = "G${s.G} V${s.V} F${s.F} E${s.E}",
                style = ChepherdFont.bodyMono.copy(color = Palette.metric, fontSize = ChepherdFont.sm),
            )
        }
        session.last_verdict?.let { v ->
            Text(
                text = v.name,
                style = ChepherdFont.bodyMono.copy(color = Palette.verdictColor(v.name), fontSize = ChepherdFont.sm),
            )
        }
        if (verdictHistory.isNotEmpty()) {
            HistoryStrip(verdicts = verdictHistory)
        }
    }
}
