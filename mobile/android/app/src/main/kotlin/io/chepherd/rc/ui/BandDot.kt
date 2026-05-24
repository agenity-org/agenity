package io.chepherd.rc.ui

import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.Modifier
import io.chepherd.rc.style.ChepherdFont
import io.chepherd.rc.style.Palette

@Composable
fun BandDot(band: String?, paused: Boolean = false, modifier: Modifier = Modifier) {
    val glyph = if (paused) "○" else "●"
    val color = Palette.bandColor(band?.lowercase(), paused)
    Text(
        text = glyph,
        color = color,
        style = ChepherdFont.bodyMono.copy(color = color),
        modifier = modifier.semantics { },
    )
}
