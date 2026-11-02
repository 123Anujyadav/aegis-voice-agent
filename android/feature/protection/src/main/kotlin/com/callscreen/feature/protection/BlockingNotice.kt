package com.callscreen.feature.protection

import android.content.Context
import android.content.Intent
import android.net.Uri
import android.provider.Settings
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.annotation.StringRes
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.LifecycleResumeEffect
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.telephony.screening.BlockingStatus
import com.callscreen.core.telephony.screening.ContactsAccess
import com.callscreen.core.telephony.screening.ScreeningRole

/** The blocklist's notice about whether blocks take effect, and its one action. */
internal object BlockingNoticeCopy {
    @StringRes val UNSUPPORTED: Int = R.string.protection_notice_unsupported
    @StringRes val ROLE_NOT_HELD: Int = R.string.protection_notice_role_not_held
    @StringRes val TURN_ON: Int = R.string.protection_notice_turn_on
    @StringRes val CONTACTS: Int = R.string.protection_notice_contacts
    @StringRes val ALLOW_CONTACTS: Int = R.string.protection_notice_allow_contacts
    @StringRes val CONTACTS_DENIED: Int = R.string.protection_notice_contacts_denied
    @StringRes val OPEN_SETTINGS: Int = R.string.protection_notice_open_settings
}

/** What the notice holds: whether blocks apply here, and whether contacts access was just refused. */
internal data class BlockingNoticeState(val status: BlockingStatus, val contactsDenied: Boolean = false)

/**
 * Tracks [BlockingStatus] for the blocklist: checked whenever the screen comes
 * to the foreground and after either system request returns. Contacts access
 * is requested only from the notice's button: the subscriber asking to block
 * saved contacts too.
 */
@Composable
internal fun rememberBlockingNotice(): Pair<BlockingNoticeState?, () -> Unit> {
    val context = LocalContext.current
    var state by remember { mutableStateOf<BlockingNoticeState?>(null) }
    fun refresh(denied: Boolean = state?.contactsDenied == true) {
        val status = BlockingStatus.of(ScreeningRole.status(context), ContactsAccess.granted(context))
        state = BlockingNoticeState(status, contactsDenied = denied && status == BlockingStatus.ACTIVE_EXCEPT_CONTACTS)
    }
    LifecycleResumeEffect(Unit) {
        refresh()
        onPauseOrDispose {}
    }
    val roleRequest = rememberLauncherForActivityResult(ActivityResultContracts.StartActivityForResult()) { refresh() }
    val contactsRequest = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        refresh(denied = !granted)
    }
    val onAction: () -> Unit = {
        val current = state
        when {
            current == null -> Unit
            current.status == BlockingStatus.ROLE_NOT_HELD ->
                ScreeningRole.requestIntent(context)?.let { roleRequest.launch(it) }
            current.status == BlockingStatus.ACTIVE_EXCEPT_CONTACTS && current.contactsDenied ->
                context.startActivity(appSettings(context))
            current.status == BlockingStatus.ACTIVE_EXCEPT_CONTACTS -> contactsRequest.launch(ContactsAccess.PERMISSION)
            else -> Unit
        }
    }
    return state to onAction
}

private fun appSettings(context: Context): Intent =
    Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS, Uri.fromParts("package", context.packageName, null))

/** Nothing when every block takes effect; otherwise what is missing, honestly, and the action that fixes it. */
@Composable
internal fun BlockingNotice(state: BlockingNoticeState, onAction: () -> Unit, modifier: Modifier = Modifier) {
    // The notice's paragraphs, then its action: each paragraph is its own string to translate.
    val (paragraphs, action) = when (state.status) {
        BlockingStatus.ACTIVE -> return
        BlockingStatus.UNSUPPORTED -> listOf(BlockingNoticeCopy.UNSUPPORTED) to null
        BlockingStatus.ROLE_NOT_HELD -> listOf(BlockingNoticeCopy.ROLE_NOT_HELD) to BlockingNoticeCopy.TURN_ON
        BlockingStatus.ACTIVE_EXCEPT_CONTACTS ->
            if (state.contactsDenied) {
                listOf(BlockingNoticeCopy.CONTACTS, BlockingNoticeCopy.CONTACTS_DENIED) to BlockingNoticeCopy.OPEN_SETTINGS
            } else {
                listOf(BlockingNoticeCopy.CONTACTS) to BlockingNoticeCopy.ALLOW_CONTACTS
            }
    }
    val text = paragraphs.map { stringResource(it) }.joinToString("\n")
    Card(
        modifier = modifier.fillMaxWidth(),
        shape = RoundedCornerShape(16.dp),
        colors = CardDefaults.cardColors(containerColor = CallScreenTheme.colors.surface.raised),
    ) {
        Column(verticalArrangement = Arrangement.spacedBy(8.dp), modifier = Modifier.padding(16.dp)) {
            Text(
                text = text,
                style = CallScreenTheme.typography.bodyMedium,
                color = CallScreenTheme.colors.content.primary,
            )
            if (action != null) {
                TextButton(onClick = onAction, modifier = Modifier.heightIn(min = 48.dp)) {
                    Text(stringResource(action), style = CallScreenTheme.typography.titleMedium)
                }
            }
        }
    }
}
