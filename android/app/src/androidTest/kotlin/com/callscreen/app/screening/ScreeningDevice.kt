package com.callscreen.app.screening

import android.os.ParcelFileDescriptor
import android.os.SystemClock
import android.util.Log
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
import androidx.test.espresso.Espresso
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
    private const val TAG = "ScreeningDevice"
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

    /**
     * The window hierarchy as UI Automator sees it: every window on screen,
     * including system dialogs and the keyboard, which the Compose tree does
     * not show. Capped, for an assertion message.
     */
    fun windowHierarchy(maxChars: Int = 8_000): String = runCatching {
        java.io.ByteArrayOutputStream().also { ui.dumpWindowHierarchy(it) }.toString("UTF-8").take(maxChars)
    }.getOrElse { "(window hierarchy unavailable: ${it.javaClass.simpleName})" }

    /**
     * Closes the keyboard, and returns only once the system records it hidden
     * with no show pending. Espresso's closeSoftKeyboard() does nothing while a
     * keyboard that a field requested is still on its way up, and a Back
     * pressed then closes that keyboard instead of reaching the app. On the API
     * 28 emulator that happened in 3 of 12 runs of ProtectionListsTest: the
     * label field's keyboard became visible 5 to 60 ms after Back, and in all
     * 9 passing runs it was visible before Back. If the system's record does
     * not carry these fields, this is closeSoftKeyboard() alone.
     */
    fun closeKeyboard(timeoutMillis: Long = 5_000) {
        val deadline = SystemClock.uptimeMillis() + timeoutMillis
        var hiddenReads = 0
        while (true) {
            val record = shell("dumpsys input_method")
            val shown = Regex("mInputShown=(true|false)").find(record)?.groupValues?.get(1)
            val requested = Regex("mShowRequested=(true|false)").find(record)?.groupValues?.get(1)
            Log.i(TAG, "keyboard: mInputShown=$shown mShowRequested=$requested")
            if (shown == null || requested == null) {
                Espresso.closeSoftKeyboard()
                return
            }
            if (shown == "true" || requested == "true") {
                hiddenReads = 0
                Espresso.closeSoftKeyboard()
            } else if (++hiddenReads >= 3) {
                return
            }
            if (SystemClock.uptimeMillis() >= deadline) {
                throw AssertionError("the keyboard did not settle closed: ${keyboardState()}")
            }
            SystemClock.sleep(100)
        }
    }

    /** Whether the input method is shown, from the system's own record (dumpsys input_method). */
    fun keyboardState(): String = runCatching {
        shell("dumpsys input_method").lines()
            .filter { it.contains("mInputShown") || it.contains("mShowRequested") || it.contains("mCurFocusedWindow") }
            .joinToString(" | ") { it.trim() }
    }.getOrElse { "(unavailable: ${it.javaClass.simpleName})" }

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
        // Say what was on screen instead, so a failure explains itself. The
        // screenshot stays on the device and is uninstalled with the app, so
        // the message carries the evidence: the merged and unmerged Compose
        // trees, and the windows UI Automator sees (dialogs, keyboard).
        throw AssertionError(
            "\"$text\" did not appear. Screen:\n" + onAllNodes(isRoot()).printToString(maxDepth = Int.MAX_VALUE) +
                "\nUnmerged:\n" + onAllNodes(isRoot(), useUnmergedTree = true).printToString(maxDepth = Int.MAX_VALUE) +
                "\nKeyboard: " + ScreeningDevice.keyboardState() +
                "\nWindows:\n" + ScreeningDevice.windowHierarchy(),
            e,
        )
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
