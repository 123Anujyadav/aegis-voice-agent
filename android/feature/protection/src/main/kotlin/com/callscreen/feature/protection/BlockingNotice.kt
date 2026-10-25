package com.callscreen.feature.protection

import android.content.Context
import android.content.Intent
import android.net.Uri
import android.provider.Settings
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
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
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.LifecycleResumeEffect
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.telephony.screening.BlockingStatus
import com.callscreen.core.telephony.screening.ContactsAccess
import com.callscreen.core.telephony.screening.ScreeningRole

/** The blocklist's notice about whether blocks take effect, and its one action. */
internal object BlockingNoticeCopy {
    const val UNSUPPORTED =
        "This version of Android doesn't let apps block calls, so numbers on this list aren't blocked on this phone."
    const val ROLE_NOT_HELD =
        "Blocking is off. Numbers on this list won't be blocked until this app is your caller ID & spam app."
    const val TURN_ON = "Turn on blocking"
    const val CONTACTS =
        "Numbers saved in your contacts ring without being checked, so blocking one of them here has no effect. " +
            "To block saved contacts too, allow contacts access. This app doesn't read or upload your contacts: " +
            "Android uses the permission only to let it check calls from them."
    const val ALLOW_CONTACTS = "Allow contacts access"
    const val CONTACTS_DENIED = "Contacts access is off. You can turn it on in Settings."
    const val OPEN_SETTINGS = "Open settings"
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
    val (text, action) = when (state.status) {
        BlockingStatus.ACTIVE -> return
        BlockingStatus.UNSUPPORTED -> BlockingNoticeCopy.UNSUPPORTED to null
        BlockingStatus.ROLE_NOT_HELD -> BlockingNoticeCopy.ROLE_NOT_HELD to BlockingNoticeCopy.TURN_ON
        BlockingStatus.ACTIVE_EXCEPT_CONTACTS ->
            if (state.contactsDenied) {
                val denied = "${BlockingNoticeCopy.CONTACTS}\n${BlockingNoticeCopy.CONTACTS_DENIED}"
                denied to BlockingNoticeCopy.OPEN_SETTINGS
            } else {
                BlockingNoticeCopy.CONTACTS to BlockingNoticeCopy.ALLOW_CONTACTS
            }
    }
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
                    Text(action, style = CallScreenTheme.typography.titleMedium)
                }
            }
        }
    }
}
