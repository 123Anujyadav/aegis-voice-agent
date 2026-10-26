package com.callscreen.app.screening

import android.content.Context
import androidx.activity.compose.setContent
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.espresso.Espresso
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.filters.SdkSuppress
import androidx.test.platform.app.InstrumentationRegistry
import com.callscreen.app.MainActivity
import com.callscreen.app.navigation.AegisDestinations
import com.callscreen.app.navigation.AegisNavGraph
import com.callscreen.core.designsystem.theme.CallScreenTheme
import dagger.hilt.android.testing.HiltAndroidRule
import dagger.hilt.android.testing.HiltAndroidTest
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/**
 * One device state, read the same way by every screen that reports it: the
 * Settings checklist, Protection's blocklist notice and the Calls feed's
 * role-loss banner (F14), as the subscriber moves between them and the system
 * changes the call screening role underneath. The role is set through the
 * shell (`cmd role`), as the system would set it; nothing in the app is
 * faked. Contacts access is only read, never granted here: revoking a grant
 * kills the app, and ScreeningRoleStatesTest needs it ungranted.
 */
@HiltAndroidTest
@SdkSuppress(minSdkVersion = 29)
@RunWith(AndroidJUnit4::class)
class ScreeningStateAcrossScreensTest {

    @get:Rule(order = 0)
    val hilt = HiltAndroidRule(this)

    @get:Rule(order = 1)
    val compose = createAndroidComposeRule<MainActivity>()

    // The banner's second sentence: what it claims about blocks. Its first
    // sentence names "another app", which the app cannot verify (OD-11).
    private val bannerConsequence = "Numbers you've blocked here won't be blocked until you switch back."

    // The Settings checklist's per-state descriptions (feature/settings SetupTasks).
    private val blockingDone = "Numbers you block are rejected on this phone."
    private val blockingToDo = "Turn it on from Protection, Blocked numbers."
    private val contactsDone = "Saved contacts you block are rejected too."
    private val contactsToDo = "Optional. Allow contacts access from Protection, Blocked numbers."
    private val forwardingUnavailable = "Needs a number from our service, which isn't set up yet."

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

    private fun gone(text: String) = compose.waitUntil(5_000) {
        compose.onAllNodes(hasText(text, substring = true)).fetchSemanticsNodes().isEmpty()
    }

    private fun tab(label: String) = compose.onNode(hasText(label) and hasClickAction()).performClick()

    /** The checklist row for contacts says what the phone actually allows. */
    private fun assertContactsRow() {
        compose.awaitText(if (ScreeningDevice.contactsGranted()) contactsDone else contactsToDo, substring = true)
    }

    @Test
    fun settingsChecklistReportsTheRoleAsItIsAndRechecksOnReturn() {
        ScreeningDevice.roleHolder(null)
        show(AegisDestinations.TASK_CENTER)
        compose.awaitText(blockingToDo)
        assertContactsRow()
        compose.onNodeWithText(forwardingUnavailable, substring = true).assertExists()
        compose.onNodeWithText(blockingDone).assertDoesNotExist()

        // The system grants the role: the checklist says so on return, and only then.
        ScreeningDevice.roleHolder(ScreeningDevice.pkg)
        compose.leaveAndReturn()
        compose.awaitText(blockingDone)
        gone(blockingToDo)
        compose.onNodeWithText(forwardingUnavailable, substring = true).assertExists()

        // Another app takes it: back to "to do", not a stale "done".
        ScreeningDevice.roleHolder(ScreeningDevice.otherApp())
        compose.leaveAndReturn()
        compose.awaitText(blockingToDo)
        gone(blockingDone)
    }

    /**
     * Blocking is turned on from Protection and lost before the subscriber ever
     * opens the Calls feed. The feed must still warn: the role WAS held, and the
     * blocks silently stopped. Then every screen agrees, and recovery clears all.
     */
    @Test
    fun roleLostAfterTurningBlockingOnFromProtectionIsReportedEverywhere() {
        ScreeningDevice.roleHolder(null)
        show(AegisDestinations.PROTECTION_DASHBOARD)
        compose.scrolledTo("Blocked numbers").performClick()
        compose.awaitText("Blocking is off.", substring = true)

        // The subscriber grants the role (the system's answer, set by the shell) and comes back.
        ScreeningDevice.roleHolder(ScreeningDevice.pkg)
        compose.leaveAndReturn()
        gone("Blocking is off.")

        // Another app takes it while the app is in the background.
        ScreeningDevice.roleHolder(ScreeningDevice.otherApp())
        compose.leaveAndReturn()
        compose.awaitText("Blocking is off.", substring = true)

        Espresso.pressBack() // the blocklist, back to the Protection dashboard
        compose.awaitText("Protection Dashboard")
        tab("Calls")
        compose.awaitText("No call data yet") // on the Calls feed
        compose.awaitText(bannerConsequence, substring = true)

        // Recovery: the role comes back; the banner clears on return.
        ScreeningDevice.roleHolder(ScreeningDevice.pkg)
        compose.leaveAndReturn()
        gone(bannerConsequence)
    }

    /**
     * The subscriber turns screening off (no app holds the role). Blocks have
     * stopped, so the feed warns and offers the role request; turning it back
     * on clears the warning. (Its first sentence still says "another app":
     * OD-11.)
     */
    @Test
    fun roleRevokedWithNoOtherHolderWarnsAndRecovers() {
        ScreeningDevice.roleHolder(ScreeningDevice.pkg)
        show(AegisDestinations.CALLS_FEED)
        compose.awaitText("No call data yet")
        compose.onNodeWithText(bannerConsequence, substring = true).assertDoesNotExist()

        ScreeningDevice.roleHolder(null)
        compose.leaveAndReturn()
        compose.awaitText(bannerConsequence, substring = true)
        compose.onNodeWithText("Switch back").performClick()
        assertTrue("the role request did not open", ScreeningDevice.systemDialogShown())
        ScreeningDevice.dismissSystemDialog()
        compose.awaitText(bannerConsequence, substring = true) // declined: still lost

        ScreeningDevice.roleHolder(ScreeningDevice.pkg)
        compose.leaveAndReturn()
        gone(bannerConsequence)
    }
}
