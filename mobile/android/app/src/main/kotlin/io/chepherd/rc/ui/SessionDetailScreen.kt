package io.chepherd.rc.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import io.chepherd.rc.protocol.LogPayload
import io.chepherd.rc.protocol.SessionState
import io.chepherd.rc.protocol.VerdictPayload
import io.chepherd.rc.style.ChepherdFont
import io.chepherd.rc.style.ChepherdSpace
import io.chepherd.rc.style.Palette
import io.chepherd.rc.viewmodel.SessionStore
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

@Composable
fun SessionDetailScreen(session: SessionState, store: SessionStore? = null) {
    val logs = store?.logs?.collectAsState()?.value ?: emptyList()
    val verdicts = store?.verdictHistory?.collectAsState()?.value
        ?.filter { it.session_uuid == session.uuid }
        ?: emptyList()

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(Palette.background)
            .padding(ChepherdSpace.s4),
        verticalArrangement = Arrangement.spacedBy(ChepherdSpace.s3),
    ) {
        Row(
            verticalAlignment = androidx.compose.ui.Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(ChepherdSpace.s2),
        ) {
            BandDot(band = session.trust_band?.name, paused = session.paused)
            Text(
                text = session.tmux_name,
                color = Palette.primary,
                style = ChepherdFont.bodyMono.copy(
                    fontSize = ChepherdFont.lg,
                    fontWeight = FontWeight.Bold,
                    color = Palette.primary,
                ),
            )
            session.repo?.let {
                Text(
                    text = it,
                    color = Palette.issueRef,
                    style = ChepherdFont.bodyMono.copy(
                        fontSize = ChepherdFont.sm,
                        color = Palette.issueRef,
                    ),
                )
            }
        }

        session.last_scorecard?.let { ScorecardView(scorecard = it) }

        if (verdicts.isNotEmpty() && session.last_scorecard != null) {
            TrendCard(verdicts = verdicts, current = session.last_scorecard!!)
        }

        Text(
            text = "log",
            color = Palette.title,
            style = ChepherdFont.titleMono.copy(fontWeight = FontWeight.Bold),
        )

        LazyColumn(
            modifier = Modifier
                .fillMaxWidth()
                .weight(1f)
                .border(1.dp, Palette.border)
                .padding(ChepherdSpace.s2),
        ) {
            val filtered = logs.filter { it.session == session.tmux_name }.takeLast(50)
            items(filtered) { entry ->
                LogLine(entry)
            }
        }
    }
}

@Composable
private fun TrendCard(verdicts: List<VerdictPayload>, current: io.chepherd.rc.protocol.Scorecard) {
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .border(1.dp, Palette.border)
            .padding(ChepherdSpace.s3),
        verticalArrangement = Arrangement.spacedBy(ChepherdSpace.s1),
    ) {
        Text(
            text = "trend",
            color = Palette.title,
            style = ChepherdFont.bodyMono.copy(
                fontSize = ChepherdFont.sm,
                fontWeight = FontWeight.Bold,
                color = Palette.title,
            ),
        )
        trendRow("G", verdicts.mapNotNull { it.scorecard?.G }, current.G)
        trendRow("V", verdicts.mapNotNull { it.scorecard?.V }, current.V)
        trendRow("F", verdicts.mapNotNull { it.scorecard?.F }, current.F)
        trendRow("E", verdicts.mapNotNull { it.scorecard?.E }, current.E)
    }
}

@Composable
private fun trendRow(axis: String, series: List<Int>, current: Int) {
    Row(
        horizontalArrangement = Arrangement.spacedBy(ChepherdSpace.s2),
        verticalAlignment = androidx.compose.ui.Alignment.CenterVertically,
    ) {
        Text(
            text = axis,
            color = Palette.title,
            style = ChepherdFont.bodyMono.copy(fontWeight = FontWeight.Bold, color = Palette.title),
        )
        Sparkline(values = series, current = current)
    }
}

@Composable
private fun LogLine(entry: LogPayload) {
    val ts = SimpleDateFormat("HH:mm:ss", Locale.US).format(Date())
    val level = entry.level.name.padEnd(7)
    Text(
        text = "$ts $level ${entry.text}",
        color = Palette.body,
        style = ChepherdFont.bodyMono.copy(fontSize = ChepherdFont.sm, color = Palette.body),
    )
}
