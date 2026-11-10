package com.callscreen.core.ui.components

import android.os.Build
import android.view.HapticFeedbackConstants
import androidx.compose.runtime.Composable
import androidx.compose.runtime.ProvidableCompositionLocal
import androidx.compose.runtime.remember
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.platform.LocalView

/**
 * `haptic.confirm` (docs/design/05-motion.md §5.8), as decided on 2026-11-10
 * under the owner's delegation (decision packet §3a, option A): Android's own
 * touch feedback through the view, never the Vibrator.
 *
 * - No permission: the app does not declare VIBRATE (release-permissions.txt).
 * - The system's "Touch feedback" setting is honoured by the platform: with
 *   it off, nothing is felt (§5.8 "Respect the system setting").
 * - API 30 and later: CONFIRM. Earlier: VIRTUAL_KEY, the closest constant
 *   that exists there (CONFIRM does not exist before API 30).
 *
 * Haptics only add to what the screen already shows and announces.
 */
public fun interface ConfirmHaptic {
    public fun confirm()
}

/** The HapticFeedbackConstants value used for `haptic.confirm` on [sdk]. */
public fun confirmFeedbackConstant(sdk: Int): Int =
    if (sdk >= Build.VERSION_CODES.R) HapticFeedbackConstants.CONFIRM else HapticFeedbackConstants.VIRTUAL_KEY

/** Lets a test (or a preview) replace the real feedback; null means the view's own. */
public val LocalConfirmHaptic: ProvidableCompositionLocal<ConfirmHaptic?> = staticCompositionLocalOf { null }

/** The `haptic.confirm` to use here: [LocalConfirmHaptic] if provided, else the current view's. */
@Composable
public fun rememberConfirmHaptic(): ConfirmHaptic {
    val provided = LocalConfirmHaptic.current
    val view = LocalView.current
    return provided ?: remember(view) {
        ConfirmHaptic { view.performHapticFeedback(confirmFeedbackConstant(Build.VERSION.SDK_INT)) }
    }
}
