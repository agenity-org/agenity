package io.chepherd.rc.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import io.chepherd.rc.protocol.Scorecard
import io.chepherd.rc.style.ChepherdFont
import io.chepherd.rc.style.ChepherdSpace
import io.chepherd.rc.style.Palette

@Composable
fun ScorecardView(scorecard: Scorecard, modifier: Modifier = Modifier) {
    Column(
        modifier = modifier
            .background(Palette.background)
            .border(1.dp, Palette.border)
            .padding(ChepherdSpace.s3),
        verticalArrangement = Arrangement.spacedBy(ChepherdSpace.s1),
    ) {
        row("G", "goal     ", scorecard.G)
        row("V", "velocity ", scorecard.V)
        row("F", "focus    ", scorecard.F)
        row("E", "end-state", scorecard.E)
    }
}

@Composable
private fun row(axis: String, label: String, value: Int) {
    val color = bandFor(value)
    Row(
        horizontalArrangement = Arrangement.spacedBy(ChepherdSpace.s2),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(
            text = axis,
            color = Palette.title,
            style = ChepherdFont.bodyMono.copy(fontWeight = FontWeight.Bold, color = Palette.title),
        )
        Text(
            text = label,
            color = Palette.body,
            style = ChepherdFont.bodyMono.copy(color = Palette.body),
            modifier = Modifier.width(96.dp),
        )
        Text(
            text = ": $value / 10",
            color = color,
            style = ChepherdFont.bodyMono.copy(fontWeight = FontWeight.Bold, color = color),
        )
        Text(
            text = gauge(value),
            color = color,
            style = ChepherdFont.bodyMono.copy(color = color),
        )
    }
}

private fun bandFor(v: Int): Color = when {
    v <= 3 -> Palette.bandCrisis
    v <= 6 -> Palette.bandConcerned
    else   -> Palette.bandTrusted
}

private fun gauge(v: Int): String {
    val filled = maxOf(0, minOf(10, v))
    return "▰".repeat(filled) + "▱".repeat(10 - filled)
}
