package com.callscreen.feature.settings

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.PendingActions
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.Icon
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.components.AegisStatusChip
import com.callscreen.core.ui.components.AegisTopAppBar
import com.callscreen.core.ui.components.StatusChipVariant

public data class TaskItem(
    val title: String,
    val description: String,
    val isCompleted: Boolean
)

@Composable
public fun TaskCenterScreen(
    modifier: Modifier = Modifier
) {
    val tasks = remember {
        listOf(
            TaskItem("Update Fraud Database", "Download latest India spam telemetry signatures.", true),
            TaskItem("Verify Family Contacts", "Confirm David's Senior Mode emergency bypass number.", false),
            TaskItem("Microphone Calibration", "Audition noise suppression for outdoor call screening.", false)
        )
    }

    Scaffold(
        topBar = { AegisTopAppBar(title = "Task Center & Action Checklist") }
    ) { paddingValues ->
        Column(
            modifier = modifier
                .fillMaxSize()
                .background(CallScreenTheme.colors.surface.background)
                .padding(paddingValues)
                .padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp)
        ) {
            Text(
                text = "Recommended System Tasks",
                style = CallScreenTheme.typography.titleMedium,
                fontWeight = FontWeight.Bold,
                color = CallScreenTheme.colors.content.primary
            )

            LazyColumn(
                verticalArrangement = Arrangement.spacedBy(10.dp)
            ) {
                items(tasks) { task ->
                    Card(
                        modifier = Modifier.fillMaxWidth(),
                        shape = RoundedCornerShape(16.dp),
                        colors = CardDefaults.cardColors(containerColor = CallScreenTheme.colors.surface.default)
                    ) {
                        Row(
                            modifier = Modifier.padding(16.dp),
                            verticalAlignment = Alignment.CenterVertically,
                            horizontalArrangement = Arrangement.spacedBy(16.dp)
                        ) {
                            Icon(
                                imageVector = if (task.isCompleted) Icons.Default.CheckCircle else Icons.Default.PendingActions,
                                contentDescription = null,
                                tint = if (task.isCompleted) CallScreenTheme.colors.status.success.text else CallScreenTheme.colors.status.warning.text
                            )
                            Column(modifier = Modifier.weight(1f)) {
                                Text(
                                    text = task.title,
                                    style = CallScreenTheme.typography.titleSmall,
                                    fontWeight = FontWeight.Bold,
                                    color = CallScreenTheme.colors.content.primary
                                )
                                Text(
                                    text = task.description,
                                    style = CallScreenTheme.typography.bodyMedium,
                                    color = CallScreenTheme.colors.content.secondary
                                )
                            }
                            AegisStatusChip(
                                text = if (task.isCompleted) "Done" else "Pending",
                                variant = if (task.isCompleted) StatusChipVariant.Verified else StatusChipVariant.Spam
                            )
                        }
                    }
                }
            }
        }
    }
}

@Preview
@Composable
private fun TaskCenterScreenPreview() {
    CallScreenTheme {
        TaskCenterScreen()
    }
}
