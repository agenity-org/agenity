package io.chepherd.rc.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Row
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import io.chepherd.rc.protocol.VerdictPayload
import io.chepherd.rc.style.ChepherdFont
import io.chepherd.rc.style.Palette

@Composable
fun HistoryStrip(
    verdicts: List<VerdictPayload>,
    limit: Int = 12,
    modifier: Modifier = Modifier,
) {
    val trailing = verdicts.takeLast(limit)
    Row(
        modifier = modifier,
        horizontalArrangement = Arrangement.spacedBy(1.dp),
    ) {
        if (trailing.isEmpty()) {
            Text(
                text = "—",
                color = Palette.timestamp,
                style = ChepherdFont.bodyMono.copy(fontSize = ChepherdFont.xs),
            )
        } else {
            for (v in trailing) {
                val color = Palette.verdictColor(v.verdict.name)
                Text(
                    text = "●",
                    color = color,
                    style = ChepherdFont.bodyMono.copy(color = color, fontSize = ChepherdFont.sm),
                )
            }
        }
    }
}
