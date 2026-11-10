package com.callscreen.feature.onboarding

import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.annotation.StringRes
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.union
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.telephony.screening.ScreeningRole
import com.callscreen.core.telephony.screening.ScreeningRoleDeclines
import com.callscreen.core.ui.components.AegisPrimaryButton
import com.callscreen.core.ui.components.aegisSafeArea

/**
 * A09b's words (docs/ux/android/06-screens-onboarding-auth-permissions.md,
 * "A09a/b/c · Permission rationales"), in the contract's order
 * (docs/ux/01-cross-surface-conventions.md §1.5): what we will ask for, what it
 * enables, what we will never do, what happens if you say no. The last three
 * are the spec's words exactly; a test holds them to it.
 */
internal object ScreeningRoleCopy {
    @StringRes val TITLE: Int = R.string.onboarding_role_title
    val blocks: List<Pair<Int, Int>> = listOf(
        R.string.onboarding_role_ask_label to R.string.onboarding_role_ask,
        R.string.onboarding_role_enables_label to R.string.onboarding_role_enables,
        R.string.onboarding_role_never_label to R.string.onboarding_role_never,
        R.string.onboarding_role_declined_label to R.string.onboarding_role_declined,
    )

    /**
     * Shown once more after a decline (A09b "previously denied"): the fourth
     * block states the loss in the past tense. It says only what is true today,
     * that blocking on this phone is off; it does not mention call forwarding,
     * which is not set up (decision packet §3c).
     */
    val blocksAfterDecline: List<Pair<Int, Int>> = blocks.dropLast(1) +
        (R.string.onboarding_role_missing_label to R.string.onboarding_role_missing)
    @StringRes val CONTINUE: Int = R.string.onboarding_role_continue
    @StringRes val NOT_NOW: Int = R.string.onboarding_role_not_now
}

/**
 * A09b · Screening role rationale. "Continue" opens the system's role request;
 * either answer, or "Not now", moves on ([onDone]): a denial degrades the
 * product, it does not stop setup. Show it only when the role can be
 * requested and is not held ([ScreeningRole.status]); the caller decides.
 */
@Composable
public fun ScreeningRoleRoute(onDone: () -> Unit, modifier: Modifier = Modifier) {
    val context = LocalContext.current
    val declines = remember { ScreeningRoleDeclines.on(context) }
    // Read once: the variant shown must not change while the person is reading it.
    val previouslyDeclined = remember { declines.previouslyDeclined() }
    val leave = {
        declines.recordOutcome(ScreeningRole.status(context))
        onDone()
    }
    val request = rememberLauncherForActivityResult(ActivityResultContracts.StartActivityForResult()) { leave() }
    ScreeningRoleScreen(
        onContinueClick = {
            val intent = ScreeningRole.requestIntent(context)
            if (intent != null) request.launch(intent) else leave()
        },
        onNotNowClick = leave,
        previouslyDeclined = previouslyDeclined,
        modifier = modifier,
    )
}

@Composable
internal fun ScreeningRoleScreen(
    onContinueClick: () -> Unit,
    onNotNowClick: () -> Unit,
    modifier: Modifier = Modifier,
    previouslyDeclined: Boolean = false,
) {
    Column(
        modifier = modifier
            .fillMaxSize()
            .background(CallScreenTheme.colors.surface.background)
            // 24 dp, or the system bar where that is wider (as AegisScreenColumn).
            .windowInsetsPadding(WindowInsets.aegisSafeArea.union(WindowInsets(24.dp, 24.dp, 24.dp, 24.dp))),
        verticalArrangement = Arrangement.SpaceBetween,
    ) {
        Column(
            verticalArrangement = Arrangement.spacedBy(20.dp),
            modifier = Modifier
                .weight(1f, fill = false)
                .verticalScroll(rememberScrollState())
                .padding(top = 24.dp),
        ) {
            Text(
                text = stringResource(ScreeningRoleCopy.TITLE),
                style = CallScreenTheme.typography.headlineMedium,
                fontWeight = FontWeight.Bold,
                color = CallScreenTheme.colors.content.primary,
                modifier = Modifier.semantics { heading() },
            )
            for ((label, line) in if (previouslyDeclined) ScreeningRoleCopy.blocksAfterDecline else ScreeningRoleCopy.blocks) {
                Column(verticalArrangement = Arrangement.spacedBy(4.dp)) {
                    Text(
                        text = stringResource(label),
                        style = CallScreenTheme.typography.titleMedium,
                        fontWeight = FontWeight.Bold,
                        color = CallScreenTheme.colors.content.primary,
                        modifier = Modifier.semantics { heading() },
                    )
                    Text(
                        text = stringResource(line),
                        style = CallScreenTheme.typography.bodyLarge,
                        color = CallScreenTheme.colors.content.secondary,
                    )
                }
            }
        }
        Column(
            verticalArrangement = Arrangement.spacedBy(8.dp),
            modifier = Modifier.padding(top = 16.dp, bottom = 16.dp),
        ) {
            AegisPrimaryButton(text = stringResource(ScreeningRoleCopy.CONTINUE), onClick = onContinueClick)
            // Not visually suppressed, and at least 48 dp (A09 accessibility rules).
            TextButton(
                onClick = onNotNowClick,
                modifier = Modifier
                    .fillMaxWidth()
                    .heightIn(min = 48.dp),
            ) {
                Text(
                    text = stringResource(ScreeningRoleCopy.NOT_NOW),
                    style = CallScreenTheme.typography.titleMedium,
                    color = CallScreenTheme.colors.content.primary,
                )
            }
        }
    }
}

@Preview(showBackground = true)
@Composable
private fun ScreeningRoleScreenPreview() {
    CallScreenTheme {
        ScreeningRoleScreen(onContinueClick = {}, onNotNowClick = {})
    }
}
