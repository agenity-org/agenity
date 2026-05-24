package io.chepherd.rc.ui

import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import io.chepherd.rc.style.ChepherdFont
import io.chepherd.rc.style.ChepherdSpace
import io.chepherd.rc.style.Palette

@Composable
fun Sparkline(
    values: List<Int>,
    current: Int? = null,
    modifier: Modifier = Modifier,
) {
    Row(modifier = modifier) {
        val trailing = values.takeLast(8)
        trailing.forEach { v ->
            Text(
                text = glyph(v),
                color = bandFor(v),
                style = ChepherdFont.bodyMono.copy(color = bandFor(v)),
            )
        }
        current?.let { c ->
            Text(
                text = "$c",
                color = bandFor(c),
                style = ChepherdFont.bodyMono.copy(fontWeight = FontWeight.Bold, color = bandFor(c)),
                modifier = Modifier.padding(start = ChepherdSpace.s1),
            )
        }
    }
}

private val GLYPHS = listOf("▁", "▂", "▃", "▄", "▅", "▆", "▇", "█")

private fun glyph(v: Int): String {
    val clamped = maxOf(0, minOf(10, v))
    val idx = (clamped.toFloat() / 10f * (GLYPHS.size - 1)).toInt()
    return GLYPHS[idx]
}

private fun bandFor(v: Int): Color = when {
    v <= 3 -> Palette.bandCrisis
    v <= 6 -> Palette.bandConcerned
    else   -> Palette.bandTrusted
}
