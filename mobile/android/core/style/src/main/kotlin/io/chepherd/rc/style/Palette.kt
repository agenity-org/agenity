// chepherd design palette — Kotlin mirror of the k9s palette canon.
// Source-of-truth: chepherd/internal/style/palette.go.
// Human-readable contract: chepherd/docs/DESIGN-SYSTEM.md.

package io.chepherd.rc.style

import androidx.compose.ui.graphics.Color

object Palette {
    // §2.1 Body
    val body       = Color(0xFF5F9EA0) // cadetblue
    val primary    = Color(0xFFFFFFFF) // white
    val background = Color(0xFF000000) // black

    // §2.2 Brand
    val logo = Color(0xFFFFA500) // orange

    // §2.3 Frame chrome
    val title       = Color(0xFF00FFFF) // aqua
    val titleRule   = Color(0xFF87CEFA) // lightskyblue
    val border      = Color(0xFF1E90FF) // dodgerblue
    val borderFocus = Color(0xFF87CEFA) // lightskyblue

    // §2.4 Menu / footer
    val keyLetter  = Color(0xFF1E90FF)
    val keyDesc    = Color(0xFFFFFFFF)
    val keyNumeric = Color(0xFFFF00FF) // fuchsia

    // §2.5 Breadcrumbs
    val crumbFG     = Color(0xFF000000)
    val crumbBG     = Color(0xFF4682B4) // steelblue
    val crumbActive = Color(0xFFFFA500) // orange

    // §2.6 Trust bands
    val bandTrusted   = Color(0xFFADFF2F) // greenyellow
    val bandStandard  = Color(0xFF5F9EA0) // cadetblue
    val bandConcerned = Color(0xFFFF8C00) // darkorange
    val bandCrisis    = Color(0xFFFF4500) // orangered
    val bandPaused    = Color(0xFF778899) // lightslategray

    // §2.7 Verdict
    val verdictSilent    = Color(0xFF5F9EA0)
    val verdictPraise    = Color(0xFFADFF2F)
    val verdictCoach     = Color(0xFFFF8C00)
    val verdictIntervene = Color(0xFFFF4500)

    // §2.8 Special events
    val injected   = Color(0xFF9370DB) // mediumpurple
    val escalating = Color(0xFFFFEFD5) // papayawhip
    val apiError   = Color(0xFFFF4500)
    val adopted    = Color(0xFF00CED1) // darkturquoise

    // §2.9 Metrics + refs
    val metric    = Color(0xFFFFEFD5)
    val issueRef  = Color(0xFF4682B4)
    val marked    = Color(0xFFB8860B) // darkgoldenrod
    val timestamp = Color(0xFF778899)

    fun bandColor(band: String?, paused: Boolean = false): Color {
        if (paused) return bandPaused
        return when (band) {
            "trusted"   -> bandTrusted
            "concerned" -> bandConcerned
            "crisis"    -> bandCrisis
            "paused"    -> bandPaused
            else        -> bandStandard
        }
    }

    fun verdictColor(v: String?): Color = when (v) {
        "praise"    -> verdictPraise
        "coach"     -> verdictCoach
        "intervene" -> verdictIntervene
        else        -> verdictSilent
    }
}
