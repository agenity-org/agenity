// Typography — §3 of the design system. Monospace default everywhere.

package io.chepherd.rc.style

import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.sp

object ChepherdFont {
    val xs   = 11.sp
    val sm   = 13.sp
    val base = 16.sp
    val lg   = 20.sp
    val xl   = 24.sp
    val xxl  = 32.sp
    val xxxl = 48.sp

    val mono: FontFamily = FontFamily.Monospace

    val bodyMono = TextStyle(
        fontFamily = mono,
        fontSize = base,
        fontWeight = FontWeight.Normal,
        color = Palette.primary,
    )

    val titleMono = TextStyle(
        fontFamily = mono,
        fontSize = base,
        fontWeight = FontWeight.Bold,
        color = Palette.title,
    )
}
