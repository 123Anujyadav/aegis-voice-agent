package com.callscreen.minifiedtest

import android.content.Intent
import android.graphics.Rect
import android.os.Build
import android.os.ParcelFileDescriptor
import android.view.accessibility.AccessibilityNodeInfo
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import androidx.test.uiautomator.By
import androidx.test.uiautomator.UiDevice
import androidx.test.uiautomator.UiObject2
import androidx.test.uiautomator.Until
import java.io.FileInputStream
import org.junit.After
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Assume.assumeTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith

/**
 * The R8-minified app (:app's staging build) on a device, driven from outside
 * through UI Automator. Every step checks what is on screen, not merely that
 * the process lives, and after every test the app's own log is searched for
 * the failures R8 causes: a fatal exception, a class or method it removed or
 * renamed, a reflective lookup or dependency-injection binding that broke.
 *
 * What a build without identity endpoints can reach: cold start and the
 * splash's routing, Welcome, SIM detection and the sign-in entry screen,
 * which says sign-in is not available. Protection and the caller lists come
 * only after sign-in, so they are not reachable from here; :app's
 * instrumented tests cover them on the debug build.
 */
@RunWith(AndroidJUnit4::class)
class MinifiedAppTest {

    private val instrumentation = InstrumentationRegistry.getInstrumentation()
    private val device = UiDevice.getInstance(instrumentation)

    @Before
    fun coldStart() {
        device.pressHome()
        shell("am force-stop $PKG")
        shell("logcat -c") // best effort: some images refuse to clear
        launch(clearTask = true)
    }

    @After
    fun noRuntimeFailures() {
        device.setOrientationNatural()
        val pid = shell("pidof $PKG").trim()
        val crashes = shell("logcat -d -b crash").lines().filter { "Process: $PKG," in it }
        assertTrue("the minified app crashed:\n" + shell("logcat -d -b crash").takeLast(4_000), crashes.isEmpty())
        assertFalse("the minified app's process is gone", pid.isEmpty())
        val log = shell("logcat -d --pid=$pid")
        val failures = log.lines().filter { line -> R8_FAILURES.any { it in line } }
        assertTrue("runtime failures in the minified app's log:\n" + failures.joinToString("\n").take(4_000), failures.isEmpty())
    }

    @Test
    fun coldStartRoutesToWelcome() {
        // The splash reads the stored session (encrypted DataStore, Keystore) and routes a
        // phone with none to Welcome: Hilt, DataStore and navigation all ran minified.
        awaitText("Welcome to Lily AI")
        awaitText("Get Protected")
        awaitText("On-device blocking")
    }

    @Test
    fun onboardingReachesTheSignInEntryAndBackReturns() {
        awaitText("Get Protected").click()
        awaitText("Your SIM")
        awaitText("Verify Phone Number").click()

        // The sign-in entry (its visible heading; "Phone Verification" is only the icon's
        // description). A Hilt ViewModel (PhoneVerificationViewModel) over the identity graph, which a
        // build without endpoints leaves unconfigured: the screen says so and sends nothing.
        awaitText("Enter Phone Number")
        awaitText("Sign-in is not available in this build.")
        // Without a sign-in service nothing can be sent: a tap leaves the screen as it was.
        reach("Send Verification Code").click()
        awaitText("Sign-in is not available in this build.")
        assertTrue("a code was requested without a sign-in service", device.findObject(By.text("Enter Verification Code")) == null)

        device.pressBack()
        awaitText("Your SIM")
        device.pressBack()
        awaitText("Welcome to Lily AI")
    }

    @Test
    fun theScreenSurvivesBackgroundingAndRotation() {
        awaitText("Get Protected").click()
        awaitText("Verify Phone Number").click()
        awaitText("Enter Phone Number")

        // Home, then back through the launcher: the same task and screen, not a restart.
        device.pressHome()
        assertTrue("the app is still on screen after Home", device.wait(Until.gone(By.pkg(PKG).depth(0)), TIMEOUT))
        launch(clearTask = false)
        awaitText("Enter Phone Number")
        awaitText("Sign-in is not available in this build.")

        // A configuration change recreates the activity: the screen and its state are restored,
        // and in landscape (640 x 320 dp here) the rest of the screen can still be scrolled to.
        device.setOrientationLeft()
        awaitText("Enter Phone Number")
        reach("Sign-in is not available in this build.")
        reach("Send Verification Code")
        device.setOrientationNatural()
        awaitText("Enter Phone Number")

        // Back still leads where it did before.
        device.pressBack()
        awaitText("Your SIM")
    }

    /**
     * Welcome's Language control opens the app-language screen (A58l) in the
     * minified build, with the phone's language chosen and every language
     * named in its own script; Back returns to Welcome. Nothing is chosen.
     */
    @Test
    fun welcomeOpensTheAppLanguageScreenAndBackReturns() {
        // Owner decisions D4 (the bilingual control) and D7 (the phone's language named: English here).
        awaitText("भाषा · Language").click()
        awaitText("Phone's language (English)")
        reach("தமிழ்")
        reach("ಕನ್ನಡ")
        device.pressBack()
        awaitText("Welcome to Lily AI")
    }

    /**
     * Android 13 and later: with a per-app language set as Android's App
     * languages page sets it, the minified app starts in that language and
     * shows English, its fallback, for words it has no translation of (none
     * yet). Android 8 to 12 keep the choice in the app, which only the debug
     * build's tests can reach (AppLanguageOnDeviceTest).
     */
    @Test
    fun anAppLanguageWithoutATranslationStartsInEnglish() {
        assumeTrue("per-app languages need Android 13", Build.VERSION.SDK_INT >= 33)
        try {
            shell("cmd locale set-app-locales $PKG --locales ta")
            val set = shell("cmd locale get-app-locales $PKG")
            assertTrue("the app language was not set: $set", "ta" in set)
            device.pressHome()
            shell("am force-stop $PKG")
            launch(clearTask = true)
            awaitText("Welcome to Lily AI")
            awaitText("Get Protected")
        } finally {
            // No --locales: back to the phone's language.
            shell("cmd locale set-app-locales $PKG")
        }
    }

    /**
     * A language chosen in the app survives the app's process being killed
     * (docs/phase19/LOCALISATION.md §4, "Not tested: real process death on a
     * device"). The choice is made in the app's own picker, then the process
     * is force-stopped and the app started cold: the picker shows the same
     * choice. On Android 8 to 12 the app keeps the choice in its own file; on
     * 13 and later Android keeps it as the per-app language, which is checked
     * as well. The words stay English, the fallback, until a translation
     * exists.
     */
    @Test
    fun aLanguageChosenInTheAppSurvivesTheProcessBeingKilled() {
        try {
            awaitText("भाषा · Language").click()
            awaitText("Phone's language (English)")
            // The baseline, which also shows that chosen() tells the rows apart.
            assertTrue("the phone's language is not chosen at the start", chosen("Phone's language (English)"))
            assertFalse("Tamil is chosen before it was chosen", chosen(TAMIL))
            reach(TAMIL).click()
            // Choosing re-creates the screen in the language (a configuration change).
            assertTrue("Tamil is not shown as chosen after choosing it.\n" + screen(), chosen(TAMIL))

            device.pressHome()
            shell("am force-stop $PKG")
            assertTrue("the app's process survived force-stop", shell("pidof $PKG").isBlank())
            launch(clearTask = true)

            // A new process: the screens start in the chosen language (English words, no
            // translation yet), and the picker still has Tamil chosen.
            awaitText("Welcome to Lily AI")
            awaitText("भाषा · Language").click()
            reach(TAMIL)
            assertTrue("Tamil is no longer chosen after the process was killed.\n" + screen(), chosen(TAMIL))
            assertFalse("the phone's language is chosen after the process was killed", chosen(phoneLanguageRow()))
            if (Build.VERSION.SDK_INT >= 33) {
                val set = shell("cmd locale get-app-locales $PKG")
                assertTrue("Android does not hold the per-app language: $set", "ta" in set)
            }
        } finally {
            restorePhoneLanguage()
        }
    }

    /** True if the picker row holding [label] is chosen (a radio button's checked state). */
    private fun chosen(label: String): Boolean {
        var node: UiObject2? = device.wait(Until.findObject(By.text(label)), TIMEOUT) ?: return false
        repeat(4) {
            val current = node ?: return false
            if (current.isChecked || current.isSelected) return true
            node = current.parent
        }
        return false
    }

    /**
     * The picker's first row, "Phone's language (…)". Its parenthesis names
     * the phone's language in the language currently chosen, so only the
     * start of the label is fixed.
     */
    private fun phoneLanguageRow(): String {
        val row = device.wait(Until.findObject(By.textStartsWith("Phone's language")), TIMEOUT)
            ?: run {
                fail("the phone's-language row is not on screen.\n" + screen())
                error("unreachable")
            }
        return row.text
    }

    /**
     * Back to the phone's language, so that no other test starts in Tamil.
     * Through the picker first, as a person would. If that cannot be done, the
     * app's data is cleared (which also clears the choice the app keeps on
     * Android 8 to 12) and the app started again, so that the run's checks of
     * the app's process and log still apply.
     */
    private fun restorePhoneLanguage() {
        if (Build.VERSION.SDK_INT >= 33) shell("cmd locale set-app-locales $PKG")
        val restored = runCatching {
            if (device.findObject(By.textStartsWith("Phone's language")) == null) {
                device.pressHome()
                shell("am force-stop $PKG")
                launch(clearTask = true)
                awaitText("भाषा · Language").click()
            }
            device.wait(Until.findObject(By.textStartsWith("Phone's language")), TIMEOUT)?.click()
            device.wait(Until.findObject(By.text("Phone's language (English)")), TIMEOUT) != null &&
                chosen("Phone's language (English)")
        }.getOrDefault(false)
        if (!restored) {
            shell("pm clear $PKG")
            launch(clearTask = true)
        }
    }

    private fun launch(clearTask: Boolean) {
        val context = instrumentation.context
        val intent = requireNotNull(context.packageManager.getLaunchIntentForPackage(PKG)) {
            "$PKG is not installed or has no launcher activity"
        }.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK).apply {
            if (clearTask) addFlags(Intent.FLAG_ACTIVITY_CLEAR_TASK)
        }
        context.startActivity(intent)
        assertTrue("the minified app did not come to the foreground", device.wait(Until.hasObject(By.pkg(PKG).depth(0)), TIMEOUT))
    }

    /** Waits for [text] on screen; on failure, says what was on screen instead. */
    private fun awaitText(text: String): UiObject2 =
        device.wait(Until.findObject(By.text(text)), TIMEOUT) ?: run {
            fail("\"$text\" did not appear on the minified app's screen.\n" + screen())
            error("unreachable")
        }

    /**
     * Waits for [text], scrolling the app's content forward to it if it is
     * below the fold: what a user can reach, not only what is drawn. It
     * scrolls with the accessibility action (as TalkBack does), on the largest
     * scrollable node, which is the screen's own container; a single-line text
     * field also reports scrollable, and a swipe can land on it instead.
     */
    private fun reach(text: String): UiObject2 {
        device.wait(Until.findObject(By.pkg(PKG).depth(0)), TIMEOUT)
        repeat(MAX_SCROLLS) {
            device.findObject(By.text(text))?.let { if (!it.visibleBounds.isEmpty) return it }
            val container = largestScrollable()
                ?: fail("\"$text\" is not on screen and nothing on the screen scrolls.\n" + screen()).let { error("unreachable") }
            val moved = container.performAction(AccessibilityNodeInfo.ACTION_SCROLL_FORWARD)
            device.waitForIdle()
            if (!moved) return awaitText(text)
        }
        return awaitText(text)
    }

    private fun largestScrollable(): AccessibilityNodeInfo? {
        val root = instrumentation.uiAutomation.rootInActiveWindow ?: return null
        val found = mutableListOf<AccessibilityNodeInfo>()
        val queue = ArrayDeque(listOf(root))
        while (queue.isNotEmpty()) {
            val node = queue.removeFirst()
            if (node.isScrollable && node.packageName == PKG) found += node
            for (i in 0 until node.childCount) node.getChild(i)?.let { queue.addLast(it) }
        }
        return found.maxByOrNull { node -> Rect().also { node.getBoundsInScreen(it) }.let { it.width().toLong() * it.height() } }
    }

    /**
     * What is on screen, compactly: the package in front, the display size,
     * and every node of the app that has text or a description, with its
     * visible bounds. A node that exists but lies outside the display shows
     * empty or clipped bounds here.
     */
    private fun screen(): String = buildString {
        append("front: ").append(device.currentPackageName)
        append("; display ").append(device.displayWidth).append('x').append(device.displayHeight).append('\n')
        for (node in device.findObjects(By.pkg(PKG))) {
            val kind = if (node.text != null) "text" else "desc"
            val label = node.text ?: node.contentDescription ?: continue
            append("  ").append(kind).append(" \"").append(label.take(80)).append("\" visible=").append(node.visibleBounds)
            if (node.isClickable) append(" clickable")
            if (node.isScrollable) append(" scrollable")
            if (!node.isEnabled) append(" disabled")
            append('\n')
        }
    }

    private fun shell(command: String): String {
        val fd: ParcelFileDescriptor = instrumentation.uiAutomation.executeShellCommand(command)
        return FileInputStream(fd.fileDescriptor).use { it.bufferedReader().readText() }.also { fd.close() }
    }

    private companion object {
        const val PKG = "com.callscreen.staging"
        const val TAMIL = "தமிழ்"
        const val TIMEOUT = 15_000L
        const val MAX_SCROLLS = 6

        /** What R8 breaks at run time, as the log reports it. */
        val R8_FAILURES = listOf(
            "FATAL EXCEPTION",
            "ClassNotFoundException",
            "NoClassDefFoundError",
            "NoSuchMethodError",
            "NoSuchMethodException",
            "NoSuchFieldError",
            "NoSuchFieldException",
            "AbstractMethodError",
            "IncompatibleClassChangeError",
            "ExceptionInInitializerError",
            "InstantiationException",
            "SerializationException",
            "MissingBinding",
        )
    }
}
