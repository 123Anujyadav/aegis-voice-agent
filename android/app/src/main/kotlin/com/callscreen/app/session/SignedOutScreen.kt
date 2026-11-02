package com.callscreen.app.session

import androidx.annotation.StringRes
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.R
import com.callscreen.core.auth.SessionEndCause
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.components.AegisPrimaryButton
import com.callscreen.core.ui.components.AegisScreenColumn

/**
 * A73: tells this phone its session is over, without ambiguity. Shown once,
 * when a stored session goes away while the app is in use (SessionRouting).
 */
@Composable
internal fun SignedOutScreen(
    cause: SessionEndCause?,
    /** Whether this app still holds the call screening role: on-device blocking needs no session. */
    blockingOn: Boolean,
    onSignInClick: () -> Unit,
    modifier: Modifier = Modifier,
) {
    // Scrolls when the words do not fit: a large font, or a longer translation.
    AegisScreenColumn(modifier = modifier) {
        Column(
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.spacedBy(20.dp),
            modifier = Modifier.padding(top = 48.dp),
        ) {
            Text(
                text = stringResource(SignedOutCopy.TITLE),
                style = CallScreenTheme.typography.headlineLarge,
                fontWeight = FontWeight.Bold,
                color = CallScreenTheme.colors.content.primary,
                modifier = Modifier.semantics { heading() },
            )
            for (line in SignedOutCopy.lines(cause, blockingOn)) {
                Text(
                    text = stringResource(line),
                    style = CallScreenTheme.typography.bodyLarge,
                    color = CallScreenTheme.colors.content.secondary,
                )
            }
        }
        AegisPrimaryButton(
            text = stringResource(SignedOutCopy.SIGN_IN),
            onClick = onSignInClick,
            modifier = Modifier.padding(bottom = 16.dp),
        )
    }
}

/**
 * A73's words, as string resources (res/values/strings.xml). A reason is
 * given only when the app actually knows it: a session the server ended may
 * have been signed in elsewhere, revoked or expired, and the app cannot tell
 * which, so it says none of them.
 */
internal object SignedOutCopy {
    @StringRes val TITLE: Int = R.string.app_signed_out_title
    @StringRes val YOU_SIGNED_OUT: Int = R.string.app_signed_out_you_signed_out
    @StringRes val NOT_VERIFIED: Int = R.string.app_signed_out_not_verified

    /** A73's "screening is still active" line, for the screening this app does today: on the phone. */
    @StringRes val BLOCKING_STILL_ON: Int = R.string.app_signed_out_blocking_still_on
    @StringRes val SIGN_IN: Int = R.string.app_signed_out_sign_in

    @StringRes
    fun reason(cause: SessionEndCause?): Int? = when (cause) {
        SessionEndCause.SIGNED_OUT_ON_THIS_DEVICE -> YOU_SIGNED_OUT
        SessionEndCause.STORED_SESSION_UNRECOVERABLE -> NOT_VERIFIED
        else -> null
    }

    fun lines(cause: SessionEndCause?, blockingOn: Boolean): List<Int> =
        listOfNotNull(reason(cause), if (blockingOn) BLOCKING_STILL_ON else null)
}

@Preview
@Composable
private fun SignedOutScreenPreview() {
    CallScreenTheme {
        SignedOutScreen(cause = SessionEndCause.SESSION_ENDED, blockingOn = true, onSignInClick = {})
    }
}
