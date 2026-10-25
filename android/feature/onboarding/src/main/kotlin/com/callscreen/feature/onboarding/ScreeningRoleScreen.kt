package com.callscreen.feature.onboarding

import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.telephony.screening.ScreeningRole
import com.callscreen.core.ui.components.AegisPrimaryButton

/**
 * A09b's words (docs/ux/android/06-screens-onboarding-auth-permissions.md,
 * "A09a/b/c · Permission rationales"), in the contract's order
 * (docs/ux/01-cross-surface-conventions.md §1.5): what we will ask for, what it
 * enables, what we will never do, what happens if you say no. The last three
 * are the spec's words exactly; a test holds them to it.
 */
internal object ScreeningRoleCopy {
    const val TITLE = "Screen calls on your phone"
    val blocks: List<Pair<String, String>> = listOf(
        "What Android will ask" to "Android will ask to make this app your caller ID & spam app.",
        "What it enables" to "We can block numbers you've blocked, and let people you know ring through, " +
            "without the call ever leaving your phone.",
        "What we'll never do" to "We can't hear your calls. " +
            "Android doesn't allow that for any app, including this one.",
        "If you say no" to "Every unknown call gets forwarded, which may cost you carrier charges and means " +
            "people you know get screened too.",
    )
    const val CONTINUE = "Continue"
    const val NOT_NOW = "Not now"
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
    val request = rememberLauncherForActivityResult(ActivityResultContracts.StartActivityForResult()) { onDone() }
    ScreeningRoleScreen(
        onContinueClick = {
            val intent = ScreeningRole.requestIntent(context)
            if (intent != null) request.launch(intent) else onDone()
        },
        onNotNowClick = onDone,
        modifier = modifier,
    )
}

@Composable
internal fun ScreeningRoleScreen(
    onContinueClick: () -> Unit,
    onNotNowClick: () -> Unit,
    modifier: Modifier = Modifier,
) {
    Column(
        modifier = modifier
            .fillMaxSize()
            .background(CallScreenTheme.colors.surface.background)
            .padding(24.dp),
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
                text = ScreeningRoleCopy.TITLE,
                style = CallScreenTheme.typography.headlineMedium,
                fontWeight = FontWeight.Bold,
                color = CallScreenTheme.colors.content.primary,
                modifier = Modifier.semantics { heading() },
            )
            for ((label, line) in ScreeningRoleCopy.blocks) {
                Column(verticalArrangement = Arrangement.spacedBy(4.dp)) {
                    Text(
                        text = label,
                        style = CallScreenTheme.typography.titleMedium,
                        fontWeight = FontWeight.Bold,
                        color = CallScreenTheme.colors.content.primary,
                        modifier = Modifier.semantics { heading() },
                    )
                    Text(
                        text = line,
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
            AegisPrimaryButton(text = ScreeningRoleCopy.CONTINUE, onClick = onContinueClick)
            // Not visually suppressed, and at least 48 dp (A09 accessibility rules).
            TextButton(
                onClick = onNotNowClick,
                modifier = Modifier
                    .fillMaxWidth()
                    .heightIn(min = 48.dp),
            ) {
                Text(
                    text = ScreeningRoleCopy.NOT_NOW,
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
