package com.callscreen.feature.onboarding

import com.callscreen.core.testing.language.TranslationLayout
import android.telephony.TelephonyManager
import androidx.compose.ui.test.junit4.createComposeRule
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.ParameterizedRobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

/**
 * Onboarding in each release language's configuration, with words half as long
 * again in that language's script (TranslationLayout, PseudoTranslation: a
 * stand-in, not a translation), at twice the font scale in 320 x 480 dp. Every
 * action and message must still be reachable. English runs as it ships.
 */
@RunWith(ParameterizedRobolectricTestRunner::class)
@Config(sdk = [34])
// Real text measurement, with each script's glyphs: Robolectric's default
// graphics give every glyph a fixed size and ignore the font scale.
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class OnboardingTranslationLayoutTest(tag: String) {

    @get:Rule
    val compose = createComposeRule()

    private val layout = TranslationLayout(compose, tag)

    private fun s(id: Int): String = RuntimeEnvironment.getApplication().getString(id)

    private fun reachable(english: String) = layout.reachable(english)

    @Test
    fun welcome() {
        layout.show { WelcomeScreen() }
        reachable(s(R.string.onboarding_welcome_language))
        reachable("Get Protected")
    }

    @Test
    fun simDetection() {
        layout.show { SimDetectionScreen(sim = SimSummary.of(TelephonyManager.SIM_STATE_READY, "Airtel")) }
        reachable("Verify Phone Number")
    }

    @Test
    fun phoneVerificationWithoutASignInService() {
        layout.show {
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
    }

    @Test
    fun aiScreeningSetup() {
        layout.show { AiScreeningSetupScreen() }
        reachable("Save & Continue")
    }

    @Test
    fun callForwardingSetup() {
        layout.show { CallForwardingSetupScreen() }
        reachable(s(ForwardingCopy.CONTINUE))
    }

    @Test
    fun setupComplete() {
        layout.show { SetupCompleteScreen(blockingOn = false) }
        reachable("Go to Calls Feed")
    }

    companion object {
        @JvmStatic
        @ParameterizedRobolectricTestRunner.Parameters(name = "{0}")
        fun tags(): List<Array<Any>> = TranslationLayout.TAGS.map { arrayOf<Any>(it) }
    }
}
