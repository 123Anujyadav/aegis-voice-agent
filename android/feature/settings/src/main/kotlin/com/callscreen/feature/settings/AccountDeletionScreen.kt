package com.callscreen.feature.settings

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

/** The words on the deletion screen; they state only what the server does (docs/phase19/ACCOUNT-DELETION.md). */
public object AccountDeletionCopy {
    public const val TITLE: String = "Delete account"
    public const val ENTRY_DESCRIPTION: String = "Permanently delete your Aegis account and sign out."
    public const val WHAT_HAPPENS: String = "What deleting your account does"
    public val DELETED: List<String> = listOf(
        "Your account is deleted on our servers, on every phone where you are signed in.",
        "Your sign-in sessions, pending verification codes and device records are deleted.",
        "This phone is signed out.",
    )
    public const val WHAT_STAYS: String = "What stays"
    public val KEPT: List<String> = listOf(
        "A record that your account was deleted, and when. It holds a coded account id, not your phone number.",
        "Our security log, which contains no phone number or account id.",
        "Your blocked and allowed numbers stay on this phone. They were never sent to us. Remove them in Protection, or uninstall the app.",
        "Short-lived security records, such as abuse limits and a receipt for this request, are removed automatically within about a day.",
    )
    public const val PERMANENT: String = "This cannot be undone."
    public const val DELETE_BUTTON: String = "Delete my account"
    public const val CONFIRM_TITLE: String = "Delete your account permanently?"
    public const val CONFIRM_MESSAGE: String = "Your account will be deleted on every phone. This cannot be undone."
    public const val CONFIRM: String = "Delete permanently"
    public const val CANCEL: String = "Cancel"
    public const val DELETING: String = "Deleting your account…"
    public const val DONE_TITLE: String = "Your account has been deleted"
    public const val DONE_BODY: String = "Our servers confirmed the deletion, and this phone is signed out."
    public const val DONE_BUTTON: String = "Done"
    public const val CHECKING: String = "Checking this phone's sign-in…"
    public const val NOT_SIGNED_IN: String = "This phone is not signed in, so there is no account to delete from here. " +
        "To delete an account, sign in with its number first."
    public const val NOT_CONFIGURED: String = "Accounts are not available in this version of the app, so there is no account to delete."
    public const val BACK: String = "Back"

    public fun message(message: DeletionMessage): String = when (message) {
        DeletionMessage.NO_CONFIRMATION ->
            "We could not confirm the deletion. Check your connection and try again. If it already went through, trying again confirms it."
        DeletionMessage.SERVICE_UNAVAILABLE -> "The service is unavailable right now. Nothing was deleted. Try again later."
        DeletionMessage.TOO_MANY_ATTEMPTS -> "Too many attempts. Nothing was deleted. Wait a while and try again."
        DeletionMessage.DEVICE_CHECK_FAILED ->
            "This phone could not prove it is the one you signed in on. Nothing was deleted. Check the phone's date and time, then try again."
        DeletionMessage.SESSION_ENDED ->
            "This phone's sign-in had already ended, so it could not delete the account and has been signed out. Sign in again to delete it."
        DeletionMessage.UNEXPECTED -> "Something went wrong. Nothing was confirmed as deleted. Try again."
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
    Scaffold(topBar = { AegisTopAppBar(title = AccountDeletionCopy.TITLE) }) { padding ->
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
                DeletionStep.CHECKING -> Progress(AccountDeletionCopy.CHECKING)
                DeletionStep.NOT_CONFIGURED -> Unavailable(AccountDeletionCopy.NOT_CONFIGURED, null, onBack)
                DeletionStep.NOT_SIGNED_IN -> Unavailable(AccountDeletionCopy.NOT_SIGNED_IN, state.message, onBack)
                DeletionStep.READY, DeletionStep.CONFIRMING -> Explanation(state.message, onDeleteClick, onBack)
                DeletionStep.DELETING -> Progress(AccountDeletionCopy.DELETING)
                DeletionStep.DELETED -> Deleted(onDone)
            }
        }
    }
    if (state.step == DeletionStep.CONFIRMING) {
        AegisDialog(
            title = AccountDeletionCopy.CONFIRM_TITLE,
            message = AccountDeletionCopy.CONFIRM_MESSAGE,
            confirmText = AccountDeletionCopy.CONFIRM,
            dismissText = AccountDeletionCopy.CANCEL,
            onConfirm = onConfirm,
            onDismiss = onCancel,
        )
    }
}

@Composable
private fun Explanation(message: DeletionMessage?, onDeleteClick: () -> Unit, onBack: () -> Unit) {
    if (message != null) Alert(AccountDeletionCopy.message(message))
    Heading(AccountDeletionCopy.WHAT_HAPPENS)
    AccountDeletionCopy.DELETED.forEach { Bullet(it) }
    Heading(AccountDeletionCopy.WHAT_STAYS)
    AccountDeletionCopy.KEPT.forEach { Bullet(it) }
    Text(
        text = AccountDeletionCopy.PERMANENT,
        style = CallScreenTheme.typography.bodyMedium,
        fontWeight = FontWeight.Bold,
        color = CallScreenTheme.colors.content.primary,
    )
    AegisEmergencyButton(text = AccountDeletionCopy.DELETE_BUTTON, onClick = onDeleteClick)
    BackButton(onBack)
}

@Composable
private fun Unavailable(text: String, message: DeletionMessage?, onBack: () -> Unit) {
    if (message != null) Alert(AccountDeletionCopy.message(message))
    Body(text)
    BackButton(onBack)
}

@Composable
private fun Deleted(onDone: () -> Unit) {
    Text(
        text = AccountDeletionCopy.DONE_TITLE,
        style = CallScreenTheme.typography.titleLarge,
        fontWeight = FontWeight.Bold,
        color = CallScreenTheme.colors.content.primary,
        modifier = Modifier.semantics {
            heading()
            liveRegion = LiveRegionMode.Polite
        },
    )
    Body(AccountDeletionCopy.DONE_BODY)
    AegisPrimaryButton(text = AccountDeletionCopy.DONE_BUTTON, onClick = onDone)
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
    Body("• $text")
}

@Composable
private fun Body(text: String) {
    Text(text = text, style = CallScreenTheme.typography.bodyMedium, color = CallScreenTheme.colors.content.secondary)
}

@Composable
private fun BackButton(onBack: () -> Unit) {
    OutlinedButton(onClick = onBack, modifier = Modifier.fillMaxWidth()) {
        Text(AccountDeletionCopy.BACK)
    }
}

@Preview
@Composable
private fun AccountDeletionScreenPreview() {
    CallScreenTheme {
        AccountDeletionScreen(AccountDeletionState(step = DeletionStep.READY), {}, {}, {}, {}, {})
    }
}
