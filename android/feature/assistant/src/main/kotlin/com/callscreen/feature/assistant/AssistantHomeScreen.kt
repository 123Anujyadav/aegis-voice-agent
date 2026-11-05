package com.callscreen.feature.assistant

import androidx.annotation.StringRes
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Mic
import androidx.compose.material.icons.filled.RecordVoiceOver
import androidx.compose.material.icons.filled.SmartToy
import androidx.compose.material.icons.filled.Tune
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.Icon
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.components.AegisBottomBar
import com.callscreen.core.ui.components.AegisStatusChip
import com.callscreen.core.ui.components.AegisTab
import com.callscreen.core.ui.components.AegisTopAppBar
import com.callscreen.core.ui.components.StatusChipVariant
import com.callscreen.core.ui.components.rememberScrollToTop

@Composable
public fun AssistantHomeScreen(
    onVoiceSetupClick: () -> Unit = {},
    onStartVoiceChatClick: () -> Unit = {},
    onTabSelected: (AegisTab) -> Unit = {},
    modifier: Modifier = Modifier
) {

    // Re-selecting this tab scrolls its list to the top (02 §2.1).
    val list = rememberLazyListState()
    val scrollToTop = rememberScrollToTop(list)
    Scaffold(
        topBar = { AegisTopAppBar(title = stringResource(R.string.assistant_home_title)) },
        bottomBar = { AegisBottomBar(currentTab = AegisTab.Assistant, onTabSelected = onTabSelected, onReselect = scrollToTop) }
    ) { paddingValues ->
        LazyColumn(
            state = list,
            modifier = modifier
                .fillMaxSize()
                .background(CallScreenTheme.colors.surface.background)
                .padding(paddingValues)
                .padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(16.dp)
        ) {
            item {
                Card(
                    modifier = Modifier.fillMaxWidth(),
                    shape = RoundedCornerShape(20.dp),
                    colors = CardDefaults.cardColors(containerColor = CallScreenTheme.colors.surface.default)
                ) {
                    // The status sits under the words, not beside them: beside them, with a
                    // large font, the words took the whole row and pushed it out of view (F22).
                    Column(
                        modifier = Modifier
                            .fillMaxWidth()
                            .padding(20.dp),
                        verticalArrangement = Arrangement.spacedBy(8.dp),
                    ) {
                        Column {
                            Text(
                                text = stringResource(AssistantCopy.SCREENING_TITLE),
                                style = CallScreenTheme.typography.titleMedium,
                                fontWeight = FontWeight.Bold,
                                color = CallScreenTheme.colors.content.primary
                            )
                            Text(
                                text = stringResource(AssistantCopy.SCREENING_UNAVAILABLE),
                                style = CallScreenTheme.typography.bodyMedium,
                                color = CallScreenTheme.colors.content.secondary
                            )
                        }
                        AegisStatusChip(text = stringResource(AssistantCopy.NOT_AVAILABLE), variant = StatusChipVariant.Fraud)
                    }
                }
            }

            item {
                Text(
                    text = stringResource(R.string.assistant_controls_heading),
                    style = CallScreenTheme.typography.titleMedium,
                    fontWeight = FontWeight.Bold,
                    color = CallScreenTheme.colors.content.primary
                )
            }

            item {
                AssistantSettingCard(
                    icon = Icons.Default.RecordVoiceOver,
                    title = stringResource(R.string.assistant_voice_title),
                    description = stringResource(AssistantCopy.VOICE_CARD),
                    onClick = onVoiceSetupClick
                )
            }

            item {
                AssistantSettingCard(
                    icon = Icons.Default.Mic,
                    title = stringResource(R.string.assistant_session_title),
                    description = stringResource(AssistantCopy.SESSION_CARD),
                    onClick = onStartVoiceChatClick
                )
            }
        }
    }
}

@Composable
private fun AssistantSettingCard(
    icon: ImageVector,
    title: String,
    description: String,
    onClick: () -> Unit
) {
    Card(
        modifier = Modifier
            .fillMaxWidth()
            .clickable(onClick = onClick),
        shape = RoundedCornerShape(16.dp),
        colors = CardDefaults.cardColors(containerColor = CallScreenTheme.colors.surface.default)
    ) {
        Row(
            modifier = Modifier.padding(16.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(16.dp)
        ) {
            Box(
                modifier = Modifier
                    .size(44.dp)
                    .clip(CircleShape)
                    .background(CallScreenTheme.colors.status.ai.subtle),
                contentAlignment = Alignment.Center
            ) {
                Icon(
                    imageVector = icon,
                    // Decorative: the title beside it is read once, not twice.
                    contentDescription = null,
                    tint = CallScreenTheme.colors.status.ai.text,
                    modifier = Modifier.size(24.dp)
                )
            }
            Column {
                Text(
                    text = title,
                    style = CallScreenTheme.typography.titleMedium,
                    fontWeight = FontWeight.Bold,
                    color = CallScreenTheme.colors.content.primary
                )
                Text(
                    text = description,
                    style = CallScreenTheme.typography.bodyMedium,
                    color = CallScreenTheme.colors.content.secondary
                )
            }
        }
    }
}

@Preview
@Composable
private fun AssistantHomeScreenPreview() {
    CallScreenTheme {
        AssistantHomeScreen()
    }
}

/**
 * What the assistant can honestly say today. Screening by the assistant needs
 * carrier forwarding (ADR-0002 Stage 2), which is not available yet, so there
 * is no switch: a toggle that changes nothing and saves nothing would mislead.
 * The words are string resources (res/values/strings.xml).
 */
internal object AssistantCopy {
    @StringRes val SCREENING_TITLE: Int = R.string.assistant_screening_title
    @StringRes val SCREENING_UNAVAILABLE: Int = R.string.assistant_screening_unavailable
    @StringRes val NOT_AVAILABLE: Int = R.string.assistant_not_available
    @StringRes val VOICE_CARD: Int = R.string.assistant_voice_card
    @StringRes val SESSION_CARD: Int = R.string.assistant_session_card
    @StringRes val VOICES_INTRO: Int = R.string.assistant_voices_intro
    @StringRes val SAVE_UNAVAILABLE: Int = R.string.assistant_save_unavailable
    @StringRes val SESSION_UNAVAILABLE: Int = R.string.assistant_session_unavailable
    @StringRes val NO_MICROPHONE: Int = R.string.assistant_no_microphone
}
