// Composition locals — DI bridge from the Activity into Compose.

package io.chepherd.rc.ui

import androidx.compose.runtime.compositionLocalOf

/** Activity-bound auth launcher. Provided in ChepherdActivity.onCreate. */
val LocalAuthLauncher = compositionLocalOf<ActivityResultLikeLauncher> {
    error("LocalAuthLauncher not provided — wrap your composition in a CompositionLocalProvider")
}
