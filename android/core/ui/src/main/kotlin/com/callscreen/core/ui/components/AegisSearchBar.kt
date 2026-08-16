package com.callscreen.core.ui.components

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Search
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme

@Composable
public fun AegisSearchBar(
    query: String,
    onQueryChange: (String) -> Unit,
    placeholder: String = "Search calls, contacts, threats...",
    modifier: Modifier = Modifier
) {
    Row(
        modifier = modifier
            .fillMaxWidth()
            .height(50.dp)
            .clip(RoundedCornerShape(25.dp))
            .background(CallScreenTheme.colors.surface.sunken)
            .padding(horizontal = 16.dp),
        verticalAlignment = Alignment.CenterVertically
    ) {
        Icon(
            imageVector = Icons.Default.Search,
            contentDescription = "Search",
            tint = CallScreenTheme.colors.content.secondary,
            modifier = Modifier.size(20.dp)
        )
        BasicTextField(
            value = query,
            onValueChange = onQueryChange,
            modifier = Modifier
                .weight(1f)
                .padding(start = 12.dp),
            textStyle = CallScreenTheme.typography.bodyMedium.copy(
                color = CallScreenTheme.colors.content.primary
            ),
            cursorBrush = SolidColor(CallScreenTheme.colors.action.primary.fill),
            singleLine = true,
            decorationBox = { innerTextField ->
                if (query.isEmpty()) {
                    Text(
                        text = placeholder,
                        style = CallScreenTheme.typography.bodyMedium,
                        color = CallScreenTheme.colors.content.tertiary
                    )
                }
                innerTextField()
            }
        )
    }
}

@Preview
@Composable
private fun AegisSearchBarPreview() {
    CallScreenTheme {
        AegisSearchBar(query = "", onQueryChange = {})
    }
}
