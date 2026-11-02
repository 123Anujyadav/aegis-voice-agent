package com.callscreen.feature.onboarding

import android.telephony.TelephonyManager
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.onNodeWithText
import com.callscreen.core.designsystem.theme.CallScreenTheme
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config

/** Onboarding shows what the phone and the app actually report, and claims nothing they cannot do. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class HonestOnboardingUiTest {

    @get:Rule
    val compose = createComposeRule()

    /** The English text of a string resource, as the screen shows it. */
    private fun s(id: Int): String = RuntimeEnvironment.getApplication().getString(id)

    @Test
    fun welcomeClaimsOnlyWhatTheAppDoes() {
        compose.setContent { CallScreenTheme { WelcomeScreen() } }
        compose.onNodeWithText(s(WelcomeCopy.BLOCKING)).assertExists()
        compose.onNodeWithText(s(WelcomeCopy.SCREENING)).assertExists()
        compose.onNodeWithText("before your phone rings", substring = true).assertDoesNotExist()
        compose.onNodeWithText("speech vectors", substring = true).assertDoesNotExist()
        compose.onNodeWithText("deepfake", substring = true, ignoreCase = true).assertDoesNotExist()
    }

    @Test
    fun welcomeOffersTheAppLanguageBeforeAnythingElse() {
        var opened = 0
        var started = 0
        compose.setContent { CallScreenTheme { WelcomeScreen(onGetStartedClick = { started++ }, onLanguageClick = { opened++ }) } }
        compose.onNode(hasText(s(R.string.onboarding_welcome_language)) and hasClickAction()).performClick()
        assertEquals(1, opened)
        assertEquals("choosing a language does not start onboarding", 0, started)
    }

    @Test
    fun simScreenShowsOnlyWhatThePhoneReports() {
        compose.setContent { CallScreenTheme { SimDetectionScreen(sim = SimSummary.of(TelephonyManager.SIM_STATE_ABSENT, null)) } }
        compose.onNodeWithText("No SIM card found in this phone.").assertExists()
        compose.onNodeWithText(s(SimSummary.FORWARDING_UNKNOWN)).assertExists()
        compose.onNodeWithText("Jio", substring = true).assertDoesNotExist()
        compose.onNodeWithText("Forwarding Supported", substring = true, ignoreCase = true).assertDoesNotExist()
    }

    @Test
    fun simSummaryUsesTheReportedCarrierOrSaysItIsNotAvailable() {
        val res = RuntimeEnvironment.getApplication().resources
        assertEquals("Carrier: Airtel", SimSummary.of(TelephonyManager.SIM_STATE_READY, " Airtel ").carrierLine(res))
        assertEquals("Carrier: not available", SimSummary.of(TelephonyManager.SIM_STATE_READY, "").carrierLine(res))
        assertEquals("No SIM card found in this phone.", SimSummary.of(TelephonyManager.SIM_STATE_ABSENT, "Airtel").carrierLine(res))
        assertEquals("Not ready", s(SimSummary.of(TelephonyManager.SIM_STATE_PIN_REQUIRED, "Airtel").status))
        assertEquals(false, SimSummary.of(TelephonyManager.SIM_STATE_ABSENT, "Airtel").ready)
        assertEquals(SimSummary.Unknown, SimSummary.of(null))
    }

    @Test
    fun forwardingIsNotAvailableAndShowsNoDialCode() {
        compose.setContent { CallScreenTheme { CallForwardingSetupScreen() } }
        compose.onNodeWithText(s(ForwardingCopy.NOT_AVAILABLE), ignoreCase = true).assertExists()
        compose.onNodeWithText(s(ForwardingCopy.WHY_NOT)).assertExists()
        compose.onNodeWithText(s(ForwardingCopy.CONTINUE)).assertExists()
        compose.onNodeWithText("*71", substring = true).assertDoesNotExist()
        compose.onNodeWithText("#", substring = true).assertDoesNotExist()
        compose.onNodeWithText("automatically", substring = true).assertDoesNotExist()
    }

    @Test
    fun setupCompleteSaysWhatIsActuallyOn() {
        compose.setContent { CallScreenTheme { SetupCompleteScreen(blockingOn = false) } }
        compose.onNodeWithText(s(SetupCompleteCopy.BLOCKING_OFF)).assertExists()
        compose.onNodeWithText(s(SetupCompleteCopy.SCREENING)).assertExists()
        compose.onNodeWithText("Protection Active", ignoreCase = true).assertDoesNotExist()
        compose.onNodeWithText("automatically be screened", substring = true).assertDoesNotExist()
    }

    @Test
    fun setupCompleteWithTheRoleSaysBlockingIsOn() {
        compose.setContent { CallScreenTheme { SetupCompleteScreen(blockingOn = true) } }
        compose.onNodeWithText(s(SetupCompleteCopy.BLOCKING_ON)).assertExists()
        compose.onNodeWithText(s(SetupCompleteCopy.BLOCKING_OFF)).assertDoesNotExist()
    }

    @Test
    fun assistantPersonasAreNotSavedAndNeverSkipTheAnnouncement() {
        compose.setContent { CallScreenTheme { AiScreeningSetupScreen() } }
        compose.onNodeWithText("isn't saved yet", substring = true).assertExists()
        compose.onNodeWithText("without speaking", substring = true).assertDoesNotExist()
        compose.onNodeWithText("immediately", substring = true).assertDoesNotExist()
    }
}
