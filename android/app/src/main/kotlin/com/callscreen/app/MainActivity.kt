package com.callscreen.app

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import com.callscreen.app.navigation.AegisNavGraph
import com.callscreen.core.designsystem.theme.CallScreenTheme


import dagger.hilt.android.AndroidEntryPoint

@AndroidEntryPoint
public class MainActivity : ComponentActivity() {

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        val startDestination = intent.getStringExtra("startDestination") ?: com.callscreen.app.navigation.AegisDestinations.SPLASH
        setContent {
            CallScreenTheme {
                AegisNavGraph(startDestination = startDestination)
            }
        }
    }

    override fun onNewIntent(intent: android.content.Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        val startDestination = intent.getStringExtra("startDestination") ?: com.callscreen.app.navigation.AegisDestinations.SPLASH
        setContent {
            CallScreenTheme {
                AegisNavGraph(startDestination = startDestination)
            }
        }
    }
}

