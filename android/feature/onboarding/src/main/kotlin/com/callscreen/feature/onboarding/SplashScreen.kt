package com.callscreen.feature.onboarding

import androidx.compose.animation.core.FastOutSlowInEasing
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.tween
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Shield
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.scale
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import kotlinx.coroutines.delay

@Composable
public fun SplashScreen(
    onSplashFinished: () -> Unit = {},
    modifier: Modifier = Modifier
) {
    var startAnimation by remember { mutableStateOf(false) }
    val scale by animateFloatAsState(
        targetValue = if (startAnimation) 1.0f else 0.5f,
        animationSpec = tween(durationMillis = 800, easing = FastOutSlowInEasing),
        label = "logoScale"
    )
    val alpha by animateFloatAsState(
        targetValue = if (startAnimation) 1.0f else 0.0f,
        animationSpec = tween(durationMillis = 800),
        label = "logoAlpha"
    )

    LaunchedEffect(Unit) {
        startAnimation = true
        delay(2000)
        onSplashFinished()
    }

    Box(
        modifier = modifier
            .fillMaxSize()
            .background(CallScreenTheme.colors.action.primary.fill),
        contentAlignment = Alignment.Center
    ) {
        Column(
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.spacedBy(16.dp),
            modifier = Modifier
                .scale(scale)
                .alpha(alpha)
        ) {
            Icon(
                imageVector = Icons.Default.Shield,
                contentDescription = "Aegis Shield Logo",
                tint = CallScreenTheme.colors.action.secondary.fill,
                modifier = Modifier.size(96.dp)
            )
            Text(
                text = "Aegis AI",
                style = CallScreenTheme.typography.displayLarge,
                fontWeight = FontWeight.Bold,
                color = CallScreenTheme.colors.content.inverse
            )
            Text(
                text = "Autonomous Call Defense & Screening",
                style = CallScreenTheme.typography.bodyLarge,
                fontSize = 16.sp,
                color = CallScreenTheme.colors.content.inverse.copy(alpha = 0.8f)
            )
        }
    }
}

@Preview
@Composable
private fun SplashScreenPreview() {
    CallScreenTheme {
        SplashScreen()
    }
}
