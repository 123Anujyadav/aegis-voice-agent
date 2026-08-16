package com.callscreen.core.ui.components

import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Dialpad
import androidx.compose.material3.FloatingActionButton
import androidx.compose.material3.Icon
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.tooling.preview.Preview
import com.callscreen.core.designsystem.theme.CallScreenTheme

@Composable
public fun AegisFAB(
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    icon: ImageVector = Icons.Default.Dialpad,
    contentDescription: String = "Action"
) {
    FloatingActionButton(
        onClick = onClick,
        modifier = modifier,
        shape = CircleShape,
        containerColor = CallScreenTheme.colors.action.primary.fill,
        contentColor = CallScreenTheme.colors.action.primary.content
    ) {
        Icon(
            imageVector = icon,
            contentDescription = contentDescription
        )
    }
}

@Preview
@Composable
private fun AegisFABPreview() {
    CallScreenTheme {
        AegisFAB(onClick = {})
    }
}
