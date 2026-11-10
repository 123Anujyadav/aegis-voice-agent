package com.callscreen.core.ui.components

import android.view.HapticFeedbackConstants
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.junit4.createComposeRule
import org.junit.Assert.assertEquals
import org.junit.Assert.assertSame
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/** `haptic.confirm` (decision packet §3a): the platform's touch feedback, by API level. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class ConfirmHapticTest {

    @get:Rule
    val compose = createComposeRule()

    @Test
    fun confirmOnApi30AndLaterVirtualKeyBefore() {
        for (sdk in 30..36) assertEquals(HapticFeedbackConstants.CONFIRM, confirmFeedbackConstant(sdk))
        // CONFIRM does not exist before API 30 (the spec's API 26-28 cell named it; corrected in §5.8's note).
        for (sdk in 26..29) assertEquals(HapticFeedbackConstants.VIRTUAL_KEY, confirmFeedbackConstant(sdk))
    }

    @Test
    fun aProvidedHapticReplacesTheViewsAndTheViewsOwnDoesNotThrow() {
        val fake = ConfirmHaptic { }
        var provided: ConfirmHaptic? = null
        var own: ConfirmHaptic? = null
        compose.setContent {
            CompositionLocalProvider(LocalConfirmHaptic provides fake) { provided = rememberConfirmHaptic() }
            own = rememberConfirmHaptic()
        }
        compose.waitForIdle()
        assertSame(fake, provided)
        // The view's own feedback runs without a permission (no VIBRATE is declared).
        compose.runOnIdle { requireNotNull(own).confirm() }
    }
}
