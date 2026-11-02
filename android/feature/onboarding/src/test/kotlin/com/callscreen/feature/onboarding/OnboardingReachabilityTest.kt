package com.callscreen.feature.onboarding

import android.telephony.TelephonyManager
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.size
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsNode
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

/**
 * Every onboarding screen's action can be reached on a small display. On a
 * 640 x 320 dp landscape emulator the sign-in screen's message and button
 * were off screen with no way to scroll to them (found by the minified-build
 * functional tests, android/test-minified). Each screen is hosted in that
 * size here, and its last element must scroll into view and be displayed.
 */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
// Real text measurement: Robolectric's default graphics give every glyph a
// fixed size and ignore the font scale.
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class OnboardingReachabilityTest {

    @get:Rule
    val compose = createComposeRule()

    private fun landscape(screen: @Composable () -> Unit) {
        compose.setContent { CallScreenTheme { Box(Modifier.size(width = 640.dp, height = 320.dp)) { screen() } } }
    }

    /**
     * [text] can be brought fully into view: scrolled to when it is inside a scrolling
     * container, then entirely inside the 640 x 320 dp window and clipped by no parent.
     */
    private fun reachable(text: String) {
        val node = compose.onNodeWithText(text)
        if (node.fetchSemanticsNode().inScrollable()) node.performScrollTo()
        node.assertIsDisplayed()
        val shown = node.fetchSemanticsNode()
        val window = with(compose.density) { Rect(0f, 0f, 640.dp.toPx(), 320.dp.toPx()) }
        val bounds = shown.boundsInRoot
        assertTrue(
            "\"$text\" is cut off: $bounds of ${shown.size}, window $window",
            bounds.height >= shown.size.height - 1 && bounds.width >= shown.size.width - 1 &&
                bounds.top >= window.top - 1 && bounds.bottom <= window.bottom + 1,
        )
    }

    private fun SemanticsNode.inScrollable(): Boolean =
        generateSequence(parent) { it.parent }.any { SemanticsActions.ScrollBy in it.config }

    @Test
    fun welcome() {
        landscape { WelcomeScreen() }
        reachable("Get Protected")
    }

    @Test
    fun simDetection() {
        landscape { SimDetectionScreen(sim = SimSummary.of(TelephonyManager.SIM_STATE_READY, "Airtel")) }
        reachable("Verify Phone Number")
    }

    @Test
    fun phoneVerificationWithoutASignInService() {
        landscape {
            PhoneVerificationScreen(
                state = PhoneVerificationState(message = VerificationMessage.NOT_CONFIGURED),
                onNumberChange = {},
                onCodeChange = {},
                onSubmitNumber = {},
                onSubmitCode = {},
                onResendCode = {},
                onEditNumber = {},
            )
        }
        reachable("Sign-in is not available in this build.")
        reachable("Send Verification Code")
        compose.onNodeWithText("Send Verification Code").assertIsNotEnabled()
    }

    @Test
    fun aiScreeningSetup() {
        landscape { AiScreeningSetupScreen() }
        reachable("Save & Continue")
    }

    @Test
    fun callForwardingSetup() {
        landscape { CallForwardingSetupScreen() }
        reachable(RuntimeEnvironment.getApplication().getString(ForwardingCopy.CONTINUE))
    }

    @Test
    fun setupComplete() {
        landscape { SetupCompleteScreen(blockingOn = false) }
        reachable("Go to Calls Feed")
    }
}
