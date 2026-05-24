package io.chepherd.rc

import android.content.Intent
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.ActivityResultLauncher
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.Surface
import androidx.compose.ui.Modifier
import io.chepherd.rc.ui.ActivityResultLikeLauncher
import io.chepherd.rc.style.Palette
import io.chepherd.rc.ui.ChepherdTheme
import io.chepherd.rc.ui.LocalAuthLauncher
import io.chepherd.rc.ui.RootView

class ChepherdActivity : ComponentActivity() {
    private lateinit var authLauncher: ActivityResultLauncher<Intent>
    private var onAuthResult: ((Intent?) -> Unit)? = null

    private val launcherAdapter = ActivityResultLikeLauncher { intent, cb ->
        onAuthResult = cb
        authLauncher.launch(intent)
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        authLauncher = registerForActivityResult(
            ActivityResultContracts.StartActivityForResult()
        ) { result ->
            onAuthResult?.invoke(result.data)
            onAuthResult = null
        }
        enableEdgeToEdge()
        setContent {
            ChepherdTheme {
                Surface(modifier = Modifier.fillMaxSize().background(Palette.background)) {
                    androidx.compose.runtime.CompositionLocalProvider(
                        LocalAuthLauncher provides launcherAdapter,
                    ) {
                        RootView()
                    }
                }
            }
        }
    }
}
