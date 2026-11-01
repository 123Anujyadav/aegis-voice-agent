package com.callscreen.app.screening

import androidx.activity.compose.setContent
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performTextInput
import androidx.compose.ui.unit.dp
import androidx.test.espresso.Espresso
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.callscreen.app.MainActivity
import com.callscreen.app.navigation.AegisDestinations
import com.callscreen.app.navigation.AegisNavGraph
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.telephony.callerlist.AtomicRecordFile
import com.callscreen.core.telephony.callerlist.CallerListStore
import com.callscreen.core.telephony.prefilter.ListType
import com.callscreen.core.telephony.screening.TelephonyModule
import dagger.hilt.android.testing.BindValue
import dagger.hilt.android.testing.HiltAndroidRule
import dagger.hilt.android.testing.HiltAndroidTest
import dagger.hilt.android.testing.UninstallModules
import java.io.File
import java.util.UUID
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/**
 * The Protection lists on a device, reached through the app's real navigation
 * and Hilt graph. Only the caller list's file is a fixture: a fresh one per
 * test instead of the app's own (TelephonyModule is replaced).
 */
@HiltAndroidTest
@UninstallModules(TelephonyModule::class)
@RunWith(AndroidJUnit4::class)
class ProtectionListsTest {

    @get:Rule(order = 0)
    val hilt = HiltAndroidRule(this)

    @get:Rule(order = 1)
    val compose = createAndroidComposeRule<MainActivity>()

    private val file = File(
        InstrumentationRegistry.getInstrumentation().targetContext.cacheDir,
        "test-lists/${UUID.randomUUID()}.json",
    )

    @BindValue
    @JvmField
    val store: CallerListStore = CallerListStore(AtomicRecordFile(file))

    private val spoken = "+ 9 1 9 8 7 6 5 0 0 0 0 1"

    @Before
    fun start() {
        hilt.inject()
        // The role is held; contacts access is left to BlockingNoticeTest (revoking it would kill the app).
        ScreeningDevice.roleHolder(ScreeningDevice.pkg)
        compose.activity.setContent { CallScreenTheme { AegisNavGraph(startDestination = AegisDestinations.PROTECTION_DASHBOARD) } }
    }

    private fun openList(title: String) {
        compose.scrolledTo(title).performClick()
        // The button is found by its accessible name, which is what TalkBack announces.
        compose.waitUntil(5_000) { compose.onAllNodesWithContentDescription("Add number").fetchSemanticsNodes().isNotEmpty() }
    }

    @Test
    fun blockLabelMoveRemoveAndUndoThroughTheRealScreens() {
        openList("Blocked numbers")
        compose.onNodeWithText("No blocked numbers").assertExists()
        compose.onNodeWithText("Blocking works on this phone, even offline or if our service is down.").assertExists()

        // A35: add a block.
        compose.onNodeWithContentDescription("Add number").performClick()
        compose.awaitText("Phone number") // the add sheet opens asynchronously (slow on API 28)
        compose.onNodeWithText("Phone number").performTextInput("98765 00001")
        compose.onNodeWithText("Block").performClick()
        compose.awaitDescription("$spoken, Added by you")
        assertEquals(ListType.BLOCK, store.entries(ListType.BLOCK).single().type)

        // A32 row: one node with the digits, a 48 dp remove target.
        compose.onNode(hasContentDescription("$spoken, Added by you", substring = true)).assertExists()
        // The touch target, not the drawn icon (Material's 40 dp icon button gets a 48 dp touch area).
        val target = compose.onNodeWithContentDescription("Remove $spoken").fetchSemanticsNode().touchBoundsInRoot
        with(compose.density) {
            assertTrue("remove target ${target.width.toDp()} x ${target.height.toDp()}", target.width.toDp() >= 48.dp && target.height.toDp() >= 48.dp)
        }

        // A32d: label it.
        compose.onNode(hasContentDescription("$spoken, Added by you", substring = true)).performClick()
        compose.awaitText("Save label")
        compose.onNodeWithText("Label (optional)").performTextInput("Courier")
        compose.onNodeWithText("Save label").performScrollTo().performClick()
        compose.awaitText("Label saved.")
        assertEquals("Courier", store.entries(ListType.BLOCK).single().label)

        // A32d: move it to the allowed list; the sheet scrolls to the action on a small screen.
        compose.onNodeWithText("Move to allowed numbers").performScrollTo().performClick()
        compose.awaitText("Moved from your blocked numbers to your allowed numbers.")
        compose.awaitText("No blocked numbers")
        assertEquals("Courier", store.entries(ListType.ALLOW).single().label)

        // A33: remove it from the allowed list, then undo within 5 seconds.
        // The label's keyboard may still be open, or still on its way up, and Back would only
        // close it: close it and wait until the system records it hidden, then wait for
        // Protection before opening the other list.
        ScreeningDevice.closeKeyboard()
        Espresso.pressBack()
        compose.awaitText("Protection Dashboard", timeoutMillis = 15_000) // navigation is slow on the API 28 emulator
        openList("Allowed numbers")
        compose.onNodeWithText("If you don't answer, your carrier may still forward the call to be screened.").assertExists()
        compose.onNode(hasContentDescription("Courier, $spoken", substring = true)).assertExists()
        compose.onNodeWithContentDescription("Remove $spoken").performClick()
        compose.awaitText("Undo")
        assertEquals(emptyList<Any>(), store.entries(ListType.ALLOW))
        compose.onNodeWithText("Undo").performClick()
        compose.awaitDescription("Courier, $spoken")
        assertEquals("Courier", store.entries(ListType.ALLOW).single().label)

        // Persistence: a new store on the same file (as after a restart) reads the same list.
        assertEquals(store.entries(ListType.ALLOW), CallerListStore(AtomicRecordFile(file)).entries(ListType.ALLOW))
    }

    @Test
    fun aRefusedNumberIsExplainedAndNothingIsSaved() {
        openList("Blocked numbers")
        compose.onNodeWithContentDescription("Add number").performClick()
        compose.awaitText("Phone number")
        compose.onNodeWithText("Phone number").performTextInput("112")
        compose.onNodeWithText("Block").performClick()
        compose.awaitText("Enter a full phone number, with its area code.")
        assertEquals(emptyList<Any>(), store.entries(ListType.BLOCK))
    }
}
