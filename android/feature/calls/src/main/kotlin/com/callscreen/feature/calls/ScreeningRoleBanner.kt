package com.callscreen.feature.calls

import androidx.annotation.StringRes
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
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
import com.callscreen.core.telephony.screening.PreferencesFlag
import com.callscreen.core.telephony.screening.ScreeningRole
import com.callscreen.core.telephony.screening.ScreeningRoleWatch
import com.callscreen.core.ui.components.AegisTab
import com.callscreen.core.ui.components.rememberConfirmHaptic

/**
 * F14's words (docs/ux/android/08-user-flows.md, "Losing and regaining the
 * screening role"), as string resources (res/values/strings.xml); a test
 * holds them to it.
 */
internal object ScreeningRoleBannerCopy {
    @StringRes val LOST: Int = R.string.calls_role_lost
    @StringRes val SWITCH_BACK: Int = R.string.calls_role_switch_back
}

/**
 * A20 · Calls feed, with F14's banner (A75) when another app has taken the
 * screening role. Checked each time the feed comes to the foreground, and
 * again after the role request returns.
 */
@Composable
public fun CallsFeedRoute(onCallClick: (String) -> Unit, onTabSelected: (AegisTab) -> Unit) {
    val context = LocalContext.current
    val watch = remember { ScreeningRoleWatch(PreferencesFlag(context, PreferencesFlag.HELD_BEFORE)) }
    var lost by remember { mutableStateOf(false) }
    val haptic = rememberConfirmHaptic()
    // F14 "granted → banner clears, haptic.confirm" (decision packet §3a).
    val recheck = {
        val now = watch.lost(ScreeningRole.status(context))
        if (roleRegained(lost, now)) haptic.confirm()
        lost = now
    }
    LifecycleResumeEffect(Unit) {
        recheck()
        onPauseOrDispose {}
    }
    val request = rememberLauncherForActivityResult(ActivityResultContracts.StartActivityForResult()) { recheck() }
    CallsFeedScreen(
        onCallClick = onCallClick,
        onTabSelected = onTabSelected,
        roleLost = lost,
        onSwitchBackClick = { ScreeningRole.requestIntent(context)?.let { request.launch(it) } },
    )
}

/**
 * A75 · not a full-screen block: forwarding, transcripts and history still
 * work; only the on-device pre-filter is lost, and the copy says which.
 */
@Composable
internal fun ScreeningRoleBanner(onSwitchBack: () -> Unit, modifier: Modifier = Modifier) {
    Card(
        modifier = modifier.fillMaxWidth(),
        shape = RoundedCornerShape(16.dp),
        colors = CardDefaults.cardColors(containerColor = CallScreenTheme.colors.surface.raised),
    ) {
        Column(verticalArrangement = Arrangement.spacedBy(8.dp), modifier = Modifier.padding(16.dp)) {
            Text(
                text = stringResource(ScreeningRoleBannerCopy.LOST),
                style = CallScreenTheme.typography.bodyLarge,
                color = CallScreenTheme.colors.content.primary,
            )
            TextButton(onClick = onSwitchBack) {
                Text(text = stringResource(ScreeningRoleBannerCopy.SWITCH_BACK), style = CallScreenTheme.typography.titleMedium)
            }
        }
    }
}

/** F14: the banner was showing and the role is held again, so the banner clears. */
internal fun roleRegained(wasLost: Boolean, nowLost: Boolean): Boolean = wasLost && !nowLost
