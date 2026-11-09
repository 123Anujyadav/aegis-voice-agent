package com.callscreen.core.ui.components

import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.unit.Dp
import com.callscreen.core.designsystem.R as DesignR
import com.callscreen.core.designsystem.theme.LilyAiTokens

/**
 * The Lily AI logo on its round light plate.
 *
 * The plate is light in every theme: the logo's wordmark is navy and would
 * vanish on a dark surface. [contentDescription] is null where the logo is
 * decorative (next to the product's name as text).
 */
@Composable
public fun LilyLogo(
    size: Dp,
    contentDescription: String?,
    modifier: Modifier = Modifier,
) {
    Box(
        modifier = modifier
            .size(size)
            .clip(CircleShape)
            .background(LilyAiTokens.logoPlate),
        contentAlignment = Alignment.Center,
    ) {
        Image(
            painter = painterResource(id = DesignR.drawable.ic_lily_ai_logo),
            contentDescription = contentDescription,
            modifier = Modifier
                .size(size)
                .padding(size / 16),
        )
    }
}
