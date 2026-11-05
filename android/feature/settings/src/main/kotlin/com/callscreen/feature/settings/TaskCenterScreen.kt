package com.callscreen.feature.settings

import androidx.annotation.StringRes
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.PendingActions
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.Icon
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.LifecycleResumeEffect
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.telephony.screening.ContactsAccess
import com.callscreen.core.telephony.screening.ScreeningRole
import com.callscreen.core.telephony.screening.ScreeningRoleStatus
import com.callscreen.core.ui.components.AegisBottomBar
import com.callscreen.core.ui.components.AegisStatusChip
import com.callscreen.core.ui.components.AegisTab
import com.callscreen.core.ui.components.AegisTopAppBar
import com.callscreen.core.ui.components.StatusChipVariant
import com.callscreen.core.ui.components.rememberScrollToTop
import com.callscreen.core.ui.language.AppLanguage
import com.callscreen.core.ui.language.AppLanguageSetting
import java.util.Locale

public enum class TaskStatus { DONE, TO_DO, NOT_AVAILABLE }

public data class TaskItem(
    @StringRes val title: Int,
    @StringRes val description: Int,
    val status: TaskStatus,
)

/**
 * The Settings tab: setup steps computed from this phone's actual state,
 * rechecked on each return, the app's language and the account section.
 */
@Composable
public fun TaskCenterRoute(
    onLanguageClick: () -> Unit = {},
    onDeleteAccountClick: () -> Unit = {},
    onTabSelected: (AegisTab) -> Unit = {},
) {
    val context = LocalContext.current
    var tasks by remember { mutableStateOf(SetupTasks.of(ScreeningRoleStatus.UNAVAILABLE, contactsAccess = false)) }
    var language by remember { mutableStateOf(AppLanguageSetting.selected(context)) }
    LifecycleResumeEffect(Unit) {
        tasks = SetupTasks.of(ScreeningRole.status(context), ContactsAccess.granted(context))
        language = AppLanguageSetting.selected(context)
        onPauseOrDispose {}
    }
    TaskCenterScreen(
        tasks = tasks,
        language = language,
        onLanguageClick = onLanguageClick,
        onDeleteAccountClick = onDeleteAccountClick,
        onTabSelected = onTabSelected,
    )
}

@Composable
public fun TaskCenterScreen(
    modifier: Modifier = Modifier,
    tasks: List<TaskItem> = emptyList(),
    /** The chosen app language; null is the phone's language. */
    language: AppLanguage? = null,
    /** The phone's own language, named when no language is chosen (owner decision D7). */
    phoneLocale: Locale = AppLanguageSetting.phoneLocale(LocalContext.current),
    onLanguageClick: () -> Unit = {},
    onDeleteAccountClick: () -> Unit = {},
    /** Another tab chosen in the bottom bar. */
    onTabSelected: (AegisTab) -> Unit = {},
) {

    // Re-selecting this tab scrolls its list to the top (02 §2.1).
    val list = rememberLazyListState()
    val scrollToTop = rememberScrollToTop(list)
    Scaffold(
        topBar = { AegisTopAppBar(title = stringResource(R.string.settings_tasks_title)) },
        // The fourth tab shows the bar like the other three (02 §2.1, F25): a tab root's Back
        // leaves the app, so without it Settings would have no way to the other tabs.
        bottomBar = { AegisBottomBar(currentTab = AegisTab.Settings, onTabSelected = onTabSelected, onReselect = scrollToTop) },
    ) { paddingValues ->
        // One list for the whole screen, heading included: a fixed heading left the list
        // too short to show one checklist card at a large font (F20).
        LazyColumn(
            state = list,
            modifier = modifier
                .fillMaxSize()
                .background(CallScreenTheme.colors.surface.background)
                .padding(paddingValues)
                .padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(10.dp)
        ) {
            item {
                Text(
                    text = stringResource(R.string.settings_tasks_heading),
                    style = CallScreenTheme.typography.titleMedium,
                    fontWeight = FontWeight.Bold,
                    color = CallScreenTheme.colors.content.primary,
                    // 12 dp below, as before: the list's 10 dp and 2 more.
                    modifier = Modifier.padding(bottom = 2.dp),
                )
            }
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
                        // The status sits under the words, not beside them: beside them, with a
                        // large font, it left the description a sliver one letter wide (F20).
                        Column(
                            modifier = Modifier.weight(1f),
                            verticalArrangement = Arrangement.spacedBy(8.dp),
                        ) {
                            Column {
                                Text(
                                    text = stringResource(task.title),
                                    style = CallScreenTheme.typography.titleSmall,
                                    fontWeight = FontWeight.Bold,
                                    color = CallScreenTheme.colors.content.primary
                                )
                                Text(
                                    text = stringResource(task.description),
                                    style = CallScreenTheme.typography.bodyMedium,
                                    color = CallScreenTheme.colors.content.secondary
                                )
                            }
                            AegisStatusChip(
                                text = stringResource(SetupTasks.label(task.status)),
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
            item { LanguageSection(language, phoneLocale, onLanguageClick) }
            item { AccountSection(onDeleteAccountClick) }
        }
    }
}

/** A58l's entry: the language the app's screens are in, and the way to change it. */
@Composable
private fun LanguageSection(language: AppLanguage?, phoneLocale: Locale, onLanguageClick: () -> Unit) {
    Column(
        modifier = Modifier.padding(top = 12.dp),
        verticalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        Text(
            text = stringResource(R.string.settings_language_title),
            style = CallScreenTheme.typography.titleMedium,
            fontWeight = FontWeight.Bold,
            color = CallScreenTheme.colors.content.primary,
            modifier = Modifier.semantics { heading() },
        )
        val reader = AppLanguage.screensLocale(LocalConfiguration.current.locales[0])
        val name = language?.nativeName()
            ?: stringResource(R.string.settings_language_phone, AppLanguage.phoneLanguageName(phoneLocale, reader))
        Text(
            text = name,
            style = CallScreenTheme.typography.bodyMedium,
            color = CallScreenTheme.colors.content.secondary,
            modifier = if (language == null) Modifier else Modifier.readIn(language, name),
        )
        if (language != null && !language.translated) {
            Text(
                text = stringResource(R.string.settings_language_not_translated),
                style = CallScreenTheme.typography.bodySmall,
                color = CallScreenTheme.colors.content.secondary,
            )
        }
        OutlinedButton(onClick = onLanguageClick, modifier = Modifier.fillMaxWidth()) {
            Text(text = stringResource(R.string.settings_language_change), color = CallScreenTheme.colors.content.primary)
        }
    }
}

/** Account management: deletion, reachable from Settings whether or not this phone is signed in. */
@Composable
private fun AccountSection(onDeleteAccountClick: () -> Unit) {
    Column(
        modifier = Modifier.padding(top = 12.dp),
        verticalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        Text(
            text = stringResource(R.string.settings_account_heading),
            style = CallScreenTheme.typography.titleMedium,
            fontWeight = FontWeight.Bold,
            color = CallScreenTheme.colors.content.primary,
            modifier = Modifier.semantics { heading() },
        )
        Text(
            text = stringResource(AccountDeletionCopy.ENTRY_DESCRIPTION),
            style = CallScreenTheme.typography.bodyMedium,
            color = CallScreenTheme.colors.content.secondary,
        )
        OutlinedButton(onClick = onDeleteAccountClick, modifier = Modifier.fillMaxWidth()) {
            Text(text = stringResource(AccountDeletionCopy.TITLE), color = CallScreenTheme.colors.action.danger.fill)
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
