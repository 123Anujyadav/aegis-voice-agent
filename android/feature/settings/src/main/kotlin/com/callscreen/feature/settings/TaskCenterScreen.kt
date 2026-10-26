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
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.platform.LocalContext
import androidx.lifecycle.compose.LifecycleResumeEffect
import com.callscreen.core.telephony.screening.ContactsAccess
import com.callscreen.core.telephony.screening.ScreeningRole
import com.callscreen.core.telephony.screening.ScreeningRoleStatus
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

public enum class TaskStatus { DONE, TO_DO, NOT_AVAILABLE }

public data class TaskItem(
    val title: String,
    val description: String,
    val status: TaskStatus,
)

/** The Settings tab: setup steps computed from this phone's actual state, rechecked on each return. */
@Composable
public fun TaskCenterRoute() {
    val context = LocalContext.current
    var tasks by remember { mutableStateOf(SetupTasks.of(ScreeningRoleStatus.UNAVAILABLE, contactsAccess = false)) }
    LifecycleResumeEffect(Unit) {
        tasks = SetupTasks.of(ScreeningRole.status(context), ContactsAccess.granted(context))
        onPauseOrDispose {}
    }
    TaskCenterScreen(tasks = tasks)
}

@Composable
public fun TaskCenterScreen(
    modifier: Modifier = Modifier,
    tasks: List<TaskItem> = emptyList(),
) {

    Scaffold(
        topBar = { AegisTopAppBar(title = "Setup") }
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
                text = "What is set up on this phone",
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
                                imageVector = if (task.status == TaskStatus.DONE) Icons.Default.CheckCircle else Icons.Default.PendingActions,
                                contentDescription = null,
                                tint = if (task.status == TaskStatus.DONE) {
                                    CallScreenTheme.colors.status.success.text
                                } else {
                                    CallScreenTheme.colors.status.warning.text
                                }
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
                                text = SetupTasks.label(task.status),
                                variant = when (task.status) {
                                    TaskStatus.DONE -> StatusChipVariant.Verified
                                    TaskStatus.TO_DO -> StatusChipVariant.Spam
                                    TaskStatus.NOT_AVAILABLE -> StatusChipVariant.Fraud
                                }
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
        TaskCenterScreen(tasks = SetupTasks.of(ScreeningRoleStatus.NOT_HELD, contactsAccess = false))
    }
}
