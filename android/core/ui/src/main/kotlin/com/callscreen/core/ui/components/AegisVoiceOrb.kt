package com.callscreen.core.ui.components

import androidx.compose.animation.core.FastOutSlowInEasing
import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.scale
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.designsystem.theme.LilyAiTokens

@Composable
public fun AegisVoiceOrb(
    isListening: Boolean = true,
    modifier: Modifier = Modifier
) {
    val infiniteTransition = rememberInfiniteTransition(label = "orbPulse")
    val scale by infiniteTransition.animateFloat(
        initialValue = 0.92f,
        targetValue = 1.12f,
        animationSpec = infiniteRepeatable(
            animation = tween(durationMillis = 1400, easing = FastOutSlowInEasing),
            repeatMode = RepeatMode.Reverse
        ),
        label = "orbScale"
    )
    val ringScale by infiniteTransition.animateFloat(
        initialValue = 1.05f,
        targetValue = 1.28f,
        animationSpec = infiniteRepeatable(
            animation = tween(durationMillis = 1400, easing = FastOutSlowInEasing),
            repeatMode = RepeatMode.Reverse
        ),
        label = "ringScale"
    )

    Box(
        modifier = modifier.size(160.dp),
        contentAlignment = Alignment.Center
    ) {
        // The outer aura pulses only while listening.
        if (isListening) {
            Box(
                modifier = Modifier
                    .size(150.dp)
                    .scale(ringScale)
                    .clip(CircleShape)
                    .background(LilyAiTokens.brandViolet.copy(alpha = 0.12f))
            )
        }
        Box(
            modifier = Modifier
                .size(130.dp)
                .scale(if (isListening) scale else 1.0f)
                .clip(CircleShape)
                .background(LilyAiTokens.orbGradient)
        )
        Box(
            modifier = Modifier
                .size(76.dp)
                .clip(CircleShape)
                .background(Color.White.copy(alpha = 0.22f))
        )
    }
}

@Preview
@Composable
private fun AegisVoiceOrbPreview() {
    CallScreenTheme {
        AegisVoiceOrb()
    }
}
