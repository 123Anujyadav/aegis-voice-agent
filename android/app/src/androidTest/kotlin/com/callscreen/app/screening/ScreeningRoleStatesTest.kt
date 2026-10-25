package com.callscreen.app.screening

import android.content.Context
import androidx.activity.compose.setContent
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.filters.SdkSuppress
import androidx.test.platform.app.InstrumentationRegistry
import com.callscreen.app.MainActivity
import com.callscreen.app.navigation.AegisDestinations
import com.callscreen.app.navigation.AegisNavGraph
import com.callscreen.core.designsystem.theme.CallScreenTheme
import dagger.hilt.android.testing.HiltAndroidRule
import dagger.hilt.android.testing.HiltAndroidTest
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeFalse
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/**
 * The screening role and contacts access as the system changes them, and what
 * the app's screens say about each state. Android 10 and later: the role does
 * not exist before (AndroidNineFallbackTest covers that).
 */
@HiltAndroidTest
@SdkSuppress(minSdkVersion = 29)
@RunWith(AndroidJUnit4::class)
class ScreeningRoleStatesTest {

    @get:Rule(order = 0)
    val hilt = HiltAndroidRule(this)

    @get:Rule(order = 1)
    val compose = createAndroidComposeRule<MainActivity>()

    private val bannerText = "Another app is now screening your calls."

    @Before
    fun start() {
        hilt.inject()
        // F14 remembers whether the role was ever held; each test starts from "never".
        InstrumentationRegistry.getInstrumentation().targetContext
            .getSharedPreferences("callscreen_screening_role", Context.MODE_PRIVATE).edit().clear().commit()
    }

    private fun show(start: String) {
        compose.activity.setContent { CallScreenTheme { AegisNavGraph(startDestination = start) } }
        compose.waitForIdle()
    }

    private fun gone(text: String, substring: Boolean = false) = compose.waitUntil(5_000) {
        compose.onAllNodes(hasText(text, substring = substring)).fetchSemanticsNodes().isEmpty()
    }

    @Test
    fun blocklistNoticeFollowsTheRoleAndAsksForContactsOnlyOnRequest() {
        // Revoking a granted permission kills the app, so this flow needs a run where it was never granted.
        assumeFalse("contacts access was already granted in this run", ScreeningDevice.contactsGranted())

        // Declined (no holder): blocking is off, and the action opens the system's role request.
        ScreeningDevice.roleHolder(null)
        show(AegisDestinations.PROTECTION_DASHBOARD)
        compose.scrolledTo("Blocked numbers").performClick()
        compose.awaitText("Blocking is off.", substring = true)
        compose.onNodeWithText("Turn on blocking").performClick()
        assertTrue("the role request did not open", ScreeningDevice.systemDialogShown())
        ScreeningDevice.dismissSystemDialog()
        compose.awaitText("Blocking is off.", substring = true)

        // Taken by another app: still off.
        ScreeningDevice.roleHolder(ScreeningDevice.otherApp())
        compose.leaveAndReturn()
        compose.awaitText("Blocking is off.", substring = true)

        // Held, no contacts access: the contacts notice, and no permission prompt until the button is tapped.
        ScreeningDevice.roleHolder(ScreeningDevice.pkg)
        compose.leaveAndReturn()
        compose.awaitText("Numbers saved in your contacts", substring = true)
        gone("Blocking is off.", substring = true)
        assertFalse("a permission prompt appeared without a tap", ScreeningDevice.systemDialogShown(timeoutMillis = 1_500))
        compose.onNodeWithText("Allow contacts access").performClick()
        assertTrue("the contacts request did not open", ScreeningDevice.systemDialogShown())
        ScreeningDevice.dismissSystemDialog()
        compose.awaitText("Contacts access is off.", substring = true)
        compose.onNodeWithText("Open settings").assertExists()
        assertFalse(ScreeningDevice.contactsGranted())

        // Granted (here through the shell, as Settings would): every block applies, so no notice.
        ScreeningDevice.grantContacts()
        compose.leaveAndReturn()
        gone("Numbers saved in your contacts", substring = true)
        gone("Blocking is off.", substring = true)
    }

    @Test
    fun roleLossBannerAppearsWhenAnotherAppTakesTheRoleAndClearsWhenItReturns() {
        ScreeningDevice.roleHolder(ScreeningDevice.pkg)
        show(AegisDestinations.CALLS_FEED)
        compose.awaitText("No call data yet")
        compose.onNodeWithText(bannerText, substring = true).assertDoesNotExist()

        ScreeningDevice.roleHolder(ScreeningDevice.otherApp())
        compose.leaveAndReturn()
        compose.awaitText(bannerText, substring = true)
        compose.onNodeWithText("Switch back").performClick()
        assertTrue("the role request did not open", ScreeningDevice.systemDialogShown())
        ScreeningDevice.dismissSystemDialog()
        compose.awaitText(bannerText, substring = true)

        ScreeningDevice.roleHolder(ScreeningDevice.pkg)
        compose.leaveAndReturn()
        gone(bannerText, substring = true)
    }

    @Test
    fun aSubscriberWhoNeverHeldTheRoleSeesNoBanner() {
        ScreeningDevice.roleHolder(null)
        show(AegisDestinations.CALLS_FEED)
        compose.awaitText("No call data yet")
        compose.onNodeWithText(bannerText, substring = true).assertDoesNotExist()
    }

    @Test
    fun onboardingRoleRequestNotNowMovesOn() {
        ScreeningDevice.roleHolder(null)
        show(AegisDestinations.SCREENING_ROLE)
        for (heading in listOf("What Android will ask", "What it enables", "What we'll never do", "If you say no")) {
            compose.onNodeWithText(heading).assertExists()
        }
        compose.onNodeWithText("We can't hear your calls.", substring = true).assertExists()
        compose.onNodeWithText("Not now").performClick()
        compose.awaitText("AI Screening Persona")
    }

    @Test
    fun onboardingRoleRequestContinueOpensTheSystemRequestThenMovesOn() {
        ScreeningDevice.roleHolder(null)
        show(AegisDestinations.SCREENING_ROLE)
        compose.onNodeWithText("Continue").performClick()
        assertTrue("the role request did not open", ScreeningDevice.systemDialogShown())
        ScreeningDevice.dismissSystemDialog()
        compose.awaitText("AI Screening Persona")
    }
}
