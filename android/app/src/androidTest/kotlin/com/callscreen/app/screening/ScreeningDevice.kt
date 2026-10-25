package com.callscreen.app.screening

import android.os.ParcelFileDescriptor
import androidx.compose.ui.test.ComposeTimeoutException
import androidx.compose.ui.test.SemanticsNodeInteraction
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.hasScrollAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isRoot
import androidx.compose.ui.test.printToString
import androidx.compose.ui.test.junit4.AndroidComposeTestRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performScrollToNode
import androidx.lifecycle.Lifecycle
import androidx.test.ext.junit.rules.ActivityScenarioRule
import androidx.test.platform.app.InstrumentationRegistry
import androidx.test.uiautomator.By
import androidx.test.uiautomator.UiDevice
import androidx.test.uiautomator.Until
import com.callscreen.app.MainActivity
import java.io.FileInputStream
import java.util.regex.Pattern

/**
 * The device state the screening screens depend on, set the way the system
 * would set it: the call screening role and contacts access, through the
 * instrumentation's shell (`cmd role`, `pm grant`). Nothing in the app is
 * bypassed or faked.
 */
internal object ScreeningDevice {
    const val ROLE = "android.app.role.CALL_SCREENING"
    private val instrumentation get() = InstrumentationRegistry.getInstrumentation()
    val pkg: String get() = instrumentation.targetContext.packageName
    val ui: UiDevice get() = UiDevice.getInstance(instrumentation)

    fun shell(command: String): String {
        val fd: ParcelFileDescriptor = instrumentation.uiAutomation.executeShellCommand(command)
        return FileInputStream(fd.fileDescriptor).use { it.bufferedReader().readText() }.also { fd.close() }
    }

    /** Gives the role to [holder] (this app, another app, or nobody when null). */
    fun roleHolder(holder: String?) {
        shell("cmd role remove-role-holder $ROLE $pkg 0")
        if (holder != null) shell("cmd role add-role-holder $ROLE $holder 0")
    }

    /** Another installed app that can hold the role: the default dialer. */
    fun otherApp(): String =
        shell("cmd role get-role-holders android.app.role.DIALER").trim().ifEmpty { "com.google.android.dialer" }

    /** Granting is safe at any time; revoking a granted permission kills the app (and this test), so it is not offered. */
    fun grantContacts() {
        shell("pm grant $pkg android.permission.READ_CONTACTS")
    }

    fun contactsGranted(): Boolean =
        instrumentation.targetContext.checkSelfPermission(android.Manifest.permission.READ_CONTACTS) == 0

    /** A screenshot and the window hierarchy, in the app's external cache (adb-pullable), for a failing step. */
    fun capture(name: String) {
        val dir = instrumentation.targetContext.externalCacheDir ?: return
        runCatching { ui.takeScreenshot(java.io.File(dir, "$name.png")) }
        runCatching { java.io.File(dir, "$name.xml").outputStream().use { ui.dumpWindowHierarchy(it) } }
    }

    /** The system's permission or role dialog, which belongs to the permission controller. */
    private val systemDialog: Pattern = Pattern.compile(".*permissioncontroller.*")

    fun systemDialogShown(timeoutMillis: Long = 5_000): Boolean = ui.wait(Until.hasObject(By.pkg(systemDialog)), timeoutMillis)

    /** Dismisses the system dialog without answering: the request returns "not granted". */
    fun dismissSystemDialog() {
        ui.pressBack()
        ui.wait(Until.gone(By.pkg(systemDialog)), 5_000)
    }
}

/** Takes the screen through the background and back, as the subscriber returning to the app does. */
internal fun AndroidComposeTestRule<ActivityScenarioRule<MainActivity>, MainActivity>.leaveAndReturn() {
    activityRule.scenario.moveToState(Lifecycle.State.CREATED)
    activityRule.scenario.moveToState(Lifecycle.State.RESUMED)
    waitForIdle()
}

/** Scrolls the screen's scrollable container to [text], then returns that node. */
internal fun AndroidComposeTestRule<*, *>.scrolledTo(text: String): SemanticsNodeInteraction {
    onNode(hasScrollAction()).performScrollToNode(hasText(text))
    return onNodeWithText(text)
}

/** Waits until a node with [text] exists (a snackbar, a sheet or a navigation is asynchronous). */
internal fun AndroidComposeTestRule<*, *>.awaitText(text: String, substring: Boolean = false, timeoutMillis: Long = 5_000) {
    try {
        waitUntil(timeoutMillis) {
            onAllNodes(hasText(text, substring = substring)).fetchSemanticsNodes().isNotEmpty()
        }
    } catch (e: ComposeTimeoutException) {
        ScreeningDevice.capture("await-${text.take(20).replace(Regex("[^A-Za-z0-9]"), "_")}")
        // Say what was on screen instead, so a failure explains itself.
        throw AssertionError("\"$text\" did not appear. Screen:\n" + onAllNodes(isRoot()).printToString(), e)
    }
}

/**
 * Waits for a node whose accessible description contains [description]. List rows are one
 * accessibility node with a spoken description (label, digits, source, date), so their
 * visible text is not separately exposed.
 */
internal fun AndroidComposeTestRule<*, *>.awaitDescription(description: String, timeoutMillis: Long = 5_000) {
    try {
        waitUntil(timeoutMillis) {
            onAllNodes(hasContentDescription(description, substring = true)).fetchSemanticsNodes().isNotEmpty()
        }
    } catch (e: ComposeTimeoutException) {
        ScreeningDevice.capture("desc-${description.take(20).replace(Regex("[^A-Za-z0-9]"), "_")}")
        throw AssertionError("no node described as \"$description\"", e)
    }
}
