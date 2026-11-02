package com.callscreen.feature.settings

import androidx.annotation.StringRes
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import androidx.hilt.navigation.compose.hiltViewModel
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.components.AegisDialog
import com.callscreen.core.ui.components.AegisEmergencyButton
import com.callscreen.core.ui.components.AegisPrimaryButton
import com.callscreen.core.ui.components.AegisTopAppBar

/**
 * The words on the deletion screen, as string resources (res/values/strings.xml):
 * they state only what the server does (docs/phase19/ACCOUNT-DELETION.md).
 */
public object AccountDeletionCopy {
    @StringRes public val TITLE: Int = R.string.settings_deletion_title
    @StringRes public val ENTRY_DESCRIPTION: Int = R.string.settings_deletion_entry_description
    @StringRes public val WHAT_HAPPENS: Int = R.string.settings_deletion_what_happens
    public val DELETED: List<Int> = listOf(
        R.string.settings_deletion_deleted_server,
        R.string.settings_deletion_deleted_sessions,
        R.string.settings_deletion_deleted_signed_out,
    )
    @StringRes public val WHAT_STAYS: Int = R.string.settings_deletion_what_stays
    public val KEPT: List<Int> = listOf(
        R.string.settings_deletion_kept_record,
        R.string.settings_deletion_kept_security_log,
        R.string.settings_deletion_kept_lists,
        R.string.settings_deletion_kept_short_lived,
    )
    @StringRes public val PERMANENT: Int = R.string.settings_deletion_permanent
    @StringRes public val DELETE_BUTTON: Int = R.string.settings_deletion_delete_button
    @StringRes public val CONFIRM_TITLE: Int = R.string.settings_deletion_confirm_title
    @StringRes public val CONFIRM_MESSAGE: Int = R.string.settings_deletion_confirm_message
    @StringRes public val CONFIRM: Int = R.string.settings_deletion_confirm
    @StringRes public val CANCEL: Int = R.string.settings_deletion_cancel
    @StringRes public val DELETING: Int = R.string.settings_deletion_deleting
    @StringRes public val DONE_TITLE: Int = R.string.settings_deletion_done_title
    @StringRes public val DONE_BODY: Int = R.string.settings_deletion_done_body
    @StringRes public val DONE_BUTTON: Int = R.string.settings_deletion_done_button
    @StringRes public val CHECKING: Int = R.string.settings_deletion_checking
    @StringRes public val NOT_SIGNED_IN: Int = R.string.settings_deletion_not_signed_in
    @StringRes public val NOT_CONFIGURED: Int = R.string.settings_deletion_not_configured
    @StringRes public val BACK: Int = R.string.settings_deletion_back

    /**
     * Each message says only what the phone knows. A refusal proves that this
     * request deleted nothing, not that an earlier request whose answer was
     * lost did not: so no message says "nothing was deleted".
     */
    @StringRes
    public fun message(message: DeletionMessage): Int = when (message) {
        DeletionMessage.NO_CONFIRMATION -> R.string.settings_deletion_message_no_confirmation
        DeletionMessage.SERVICE_UNAVAILABLE -> R.string.settings_deletion_message_service_unavailable
        DeletionMessage.TOO_MANY_ATTEMPTS -> R.string.settings_deletion_message_too_many_attempts
        DeletionMessage.DEVICE_CHECK_FAILED -> R.string.settings_deletion_message_device_check_failed
        DeletionMessage.SESSION_ENDED -> R.string.settings_deletion_message_session_ended
        DeletionMessage.UNEXPECTED -> R.string.settings_deletion_message_unexpected
    }
}

/** Settings → Delete account. [onDeleted] runs once, after the server confirmed the deletion. */
@Composable
public fun AccountDeletionRoute(
    onDeleted: () -> Unit,
    onBack: () -> Unit,
    modifier: Modifier = Modifier,
    viewModel: AccountDeletionViewModel = hiltViewModel(),
) {
    val state by viewModel.state.collectAsStateWithLifecycle()
    AccountDeletionScreen(
        state = state,
        onDeleteClick = viewModel::requestDeletion,
        onConfirm = viewModel::confirm,
        onCancel = viewModel::cancel,
        onDone = {
            if (!state.deletedHandled) {
                viewModel.onDeletedHandled()
                onDeleted()
            }
        },
        onBack = onBack,
        modifier = modifier,
    )
}

/** The stateless screen. */
@Composable
public fun AccountDeletionScreen(
    state: AccountDeletionState,
    onDeleteClick: () -> Unit,
    onConfirm: () -> Unit,
    onCancel: () -> Unit,
    onDone: () -> Unit,
    onBack: () -> Unit,
    modifier: Modifier = Modifier,
) {
    Scaffold(topBar = { AegisTopAppBar(title = stringResource(AccountDeletionCopy.TITLE)) }) { padding ->
        Column(
            modifier = modifier
                .fillMaxSize()
                .background(CallScreenTheme.colors.surface.background)
                .padding(padding)
                .verticalScroll(rememberScrollState())
                .padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            when (state.step) {
                DeletionStep.CHECKING -> Progress(stringResource(AccountDeletionCopy.CHECKING))
                DeletionStep.NOT_CONFIGURED -> Unavailable(stringResource(AccountDeletionCopy.NOT_CONFIGURED), null, onBack)
                DeletionStep.NOT_SIGNED_IN -> Unavailable(stringResource(AccountDeletionCopy.NOT_SIGNED_IN), state.message, onBack)
                DeletionStep.READY, DeletionStep.CONFIRMING -> Explanation(state.message, onDeleteClick, onBack)
                DeletionStep.DELETING -> Progress(stringResource(AccountDeletionCopy.DELETING))
                DeletionStep.DELETED -> Deleted(onDone)
            }
        }
    }
    if (state.step == DeletionStep.CONFIRMING) {
        AegisDialog(
            title = stringResource(AccountDeletionCopy.CONFIRM_TITLE),
            message = stringResource(AccountDeletionCopy.CONFIRM_MESSAGE),
            confirmText = stringResource(AccountDeletionCopy.CONFIRM),
            dismissText = stringResource(AccountDeletionCopy.CANCEL),
            onConfirm = onConfirm,
            onDismiss = onCancel,
        )
    }
}

@Composable
private fun Explanation(message: DeletionMessage?, onDeleteClick: () -> Unit, onBack: () -> Unit) {
    if (message != null) Alert(stringResource(AccountDeletionCopy.message(message)))
    Heading(stringResource(AccountDeletionCopy.WHAT_HAPPENS))
    AccountDeletionCopy.DELETED.forEach { Bullet(stringResource(it)) }
    Heading(stringResource(AccountDeletionCopy.WHAT_STAYS))
    AccountDeletionCopy.KEPT.forEach { Bullet(stringResource(it)) }
    Text(
        text = stringResource(AccountDeletionCopy.PERMANENT),
        style = CallScreenTheme.typography.bodyMedium,
        fontWeight = FontWeight.Bold,
        color = CallScreenTheme.colors.content.primary,
    )
    AegisEmergencyButton(text = stringResource(AccountDeletionCopy.DELETE_BUTTON), onClick = onDeleteClick)
    BackButton(onBack)
}

@Composable
private fun Unavailable(text: String, message: DeletionMessage?, onBack: () -> Unit) {
    if (message != null) Alert(stringResource(AccountDeletionCopy.message(message)))
    Body(text)
    BackButton(onBack)
}

@Composable
private fun Deleted(onDone: () -> Unit) {
    Text(
        text = stringResource(AccountDeletionCopy.DONE_TITLE),
        style = CallScreenTheme.typography.titleLarge,
        fontWeight = FontWeight.Bold,
        color = CallScreenTheme.colors.content.primary,
        modifier = Modifier.semantics {
            heading()
            liveRegion = LiveRegionMode.Polite
        },
    )
    Body(stringResource(AccountDeletionCopy.DONE_BODY))
    AegisPrimaryButton(text = stringResource(AccountDeletionCopy.DONE_BUTTON), onClick = onDone)
}

@Composable
private fun Progress(text: String) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .semantics { liveRegion = LiveRegionMode.Polite },
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        CircularProgressIndicator(modifier = Modifier.size(24.dp))
        Body(text)
    }
}

@Composable
private fun Alert(text: String) {
    Text(
        text = text,
        style = CallScreenTheme.typography.bodyMedium,
        fontWeight = FontWeight.Bold,
        color = CallScreenTheme.colors.status.warning.text,
        modifier = Modifier.semantics { liveRegion = LiveRegionMode.Assertive },
    )
}

@Composable
private fun Heading(text: String) {
    Text(
        text = text,
        style = CallScreenTheme.typography.titleMedium,
        fontWeight = FontWeight.Bold,
        color = CallScreenTheme.colors.content.primary,
        modifier = Modifier.semantics { heading() },
    )
}

@Composable
private fun Bullet(text: String) {
    Body(stringResource(R.string.settings_deletion_bullet, text))
}

@Composable
private fun Body(text: String) {
    Text(text = text, style = CallScreenTheme.typography.bodyMedium, color = CallScreenTheme.colors.content.secondary)
}

@Composable
private fun BackButton(onBack: () -> Unit) {
    OutlinedButton(onClick = onBack, modifier = Modifier.fillMaxWidth()) {
        Text(stringResource(AccountDeletionCopy.BACK))
    }
}

@Preview
@Composable
private fun AccountDeletionScreenPreview() {
    CallScreenTheme {
        AccountDeletionScreen(AccountDeletionState(step = DeletionStep.READY), {}, {}, {}, {}, {})
    }
}
