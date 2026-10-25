package com.callscreen.app

import android.os.Bundle
import com.callscreen.BuildConfig
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import com.callscreen.app.navigation.AegisDestinations
import com.callscreen.app.navigation.AegisNavGraph
import com.callscreen.core.designsystem.theme.CallScreenTheme


import dagger.hilt.android.AndroidEntryPoint

@AndroidEntryPoint
public class MainActivity : ComponentActivity() {

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        val startDestination = startDestinationFor(intent.getStringExtra(START_DESTINATION_EXTRA), BuildConfig.DEBUG)
        setContent {
            CallScreenTheme {
                AegisNavGraph(startDestination = startDestination)
            }
        }
    }

    override fun onNewIntent(intent: android.content.Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        val startDestination = startDestinationFor(intent.getStringExtra(START_DESTINATION_EXTRA), BuildConfig.DEBUG)
        setContent {
            CallScreenTheme {
                AegisNavGraph(startDestination = startDestination)
            }
        }
    }

    internal companion object {
        /** Debug builds only: open a given screen directly (manual testing). */
        const val START_DESTINATION_EXTRA: String = "startDestination"

        /**
         * Where the app starts. MainActivity is exported (it is the launcher),
         * so any app could send this extra: in a release build it is ignored
         * and the app always starts at the splash screen, so nothing can skip
         * onboarding and sign-in. Debug builds honour it for manual testing.
         */
        fun startDestinationFor(requested: String?, debuggable: Boolean): String =
            if (debuggable && !requested.isNullOrEmpty()) requested else AegisDestinations.SPLASH
    }
}
