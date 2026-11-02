package com.callscreen.minifiedtest

import android.content.Intent
import android.os.ParcelFileDescriptor
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import androidx.test.uiautomator.By
import androidx.test.uiautomator.Direction
import androidx.test.uiautomator.UiDevice
import androidx.test.uiautomator.UiObject2
import androidx.test.uiautomator.Until
import java.io.FileInputStream
import org.junit.After
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
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
        awaitText("Welcome to Aegis AI")
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
        awaitText("Welcome to Aegis AI")
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
     * Waits for [text], scrolling the app's scrollable content down to it if
     * it is below the fold: what a user can reach, not only what is drawn.
     */
    private fun reach(text: String): UiObject2 {
        device.wait(Until.findObject(By.pkg(PKG).depth(0)), TIMEOUT)
        device.findObject(By.text(text))?.let { if (!it.visibleBounds.isEmpty) return it }
        // The screen's own scroll container is the largest scrollable node: a single-line
        // text field also reports scrollable (sideways), and scrolling it down does nothing.
        val scrollable = device.findObjects(By.pkg(PKG).scrollable(true))
            .maxByOrNull { it.visibleBounds.width().toLong() * it.visibleBounds.height() }
            ?: fail("\"$text\" is not on screen and nothing on the screen scrolls.\n" + screen()).let { error("unreachable") }
        scrollable.scrollUntil(Direction.DOWN, Until.findObject(By.text(text)))
        return awaitText(text)
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
        const val TIMEOUT = 15_000L

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
