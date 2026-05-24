package io.chepherd.rc.ui

import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import io.chepherd.rc.style.Palette

@Composable
fun ChepherdTheme(content: @Composable () -> Unit) {
    val colors = darkColorScheme(
        primary = Palette.logo,
        onPrimary = Palette.background,
        secondary = Palette.title,
        background = Palette.background,
        onBackground = Palette.primary,
        surface = Palette.background,
        onSurface = Palette.primary,
        error = Palette.apiError,
    )
    MaterialTheme(colorScheme = colors, content = content)
}
