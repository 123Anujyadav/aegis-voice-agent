package com.callscreen.app.screening

import androidx.activity.compose.setContent
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.filters.SdkSuppress
import com.callscreen.app.MainActivity
import com.callscreen.app.navigation.AegisDestinations
import com.callscreen.app.navigation.AegisNavGraph
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.telephony.screening.ScreeningRole
import com.callscreen.core.telephony.screening.ScreeningRoleStatus
import dagger.hilt.android.testing.HiltAndroidRule
import dagger.hilt.android.testing.HiltAndroidTest
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/** On a device, onboarding shows what the phone reports and claims nothing the app cannot do. */
@HiltAndroidTest
@RunWith(AndroidJUnit4::class)
class HonestScreensDeviceTest {

    @get:Rule(order = 0)
    val hilt = HiltAndroidRule(this)

    @get:Rule(order = 1)
    val compose = createAndroidComposeRule<MainActivity>()

    @Before
    fun start() = hilt.inject()

    private fun show(start: String) {
        compose.activity.setContent { CallScreenTheme { AegisNavGraph(startDestination = start) } }
        compose.waitForIdle()
    }

    @Test
    fun onboardingFromWelcomeShowsOnlyRealStatus() {
        show(AegisDestinations.WELCOME)
        compose.onNodeWithText("before your phone rings", substring = true).assertDoesNotExist()
        compose.onNodeWithText("Get Protected").performClick()
        // A05: the SIM as this phone reports it (an emulator reports its own test operator).
        compose.awaitText("This is what your phone reports about its SIM.")
        compose.onNodeWithText("Whether your carrier supports call forwarding is checked when you set it up.").assertExists()
        compose.onNodeWithText("Jio", substring = true).assertDoesNotExist()
        compose.onNodeWithText("Forwarding Supported", substring = true, ignoreCase = true).assertDoesNotExist()
    }

    @Test
    fun forwardingIsNotAvailableAndNothingIsDialled() {
        show(AegisDestinations.CALL_FORWARDING)
        compose.onNodeWithText("Not available yet", ignoreCase = true).assertExists()
        compose.onNodeWithText("*71", substring = true).assertDoesNotExist()
        compose.onNodeWithText("Continue without forwarding").performClick()
        compose.awaitText("Setup finished")
    }

    @Test
    fun setupCompleteSaysWhetherBlockingIsActuallyOn() {
        show(AegisDestinations.SETUP_COMPLETE)
        val held = ScreeningRole.status(compose.activity) == ScreeningRoleStatus.HELD
        compose.awaitText(if (held) "On-device blocking is on for the numbers you block." else "On-device blocking is off.", substring = true)
        compose.onNodeWithText("Protection Active", ignoreCase = true).assertDoesNotExist()
    }
}

/** Android 8 and 9 have no call screening role: the app says blocking cannot work here. */
@HiltAndroidTest
@SdkSuppress(maxSdkVersion = 28)
@RunWith(AndroidJUnit4::class)
class AndroidNineFallbackTest {

    @get:Rule(order = 0)
    val hilt = HiltAndroidRule(this)

    @get:Rule(order = 1)
    val compose = createAndroidComposeRule<MainActivity>()

    @Before
    fun start() = hilt.inject()

    @Test
    fun blocklistSaysBlocksCannotApplyAndOffersNoRoleRequest() {
        compose.activity.setContent { CallScreenTheme { AegisNavGraph(startDestination = AegisDestinations.PROTECTION_DASHBOARD) } }
        compose.scrolledTo("Blocked numbers").performClick()
        compose.awaitText("This version of Android doesn't let apps block calls", substring = true)
        compose.onNodeWithText("Turn on blocking").assertDoesNotExist()
    }

    @Test
    fun setupCompleteSaysBlockingIsOff() {
        compose.activity.setContent { CallScreenTheme { AegisNavGraph(startDestination = AegisDestinations.SETUP_COMPLETE) } }
        compose.awaitText("On-device blocking is off.", substring = true)
    }
}
