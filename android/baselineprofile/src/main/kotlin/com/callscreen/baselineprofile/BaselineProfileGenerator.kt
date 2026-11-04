package com.callscreen.baselineprofile

import androidx.benchmark.macro.junit4.BaselineProfileRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.uiautomator.By
import androidx.test.uiautomator.UiDevice
import androidx.test.uiautomator.UiObject2
import androidx.test.uiautomator.Until
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/**
 * What a new subscriber runs first, recorded as a baseline profile: a cold
 * start through the splash to Welcome, the app-language screen, and
 * onboarding to the sign-in entry. Without a sign-in service (any build
 * without identity endpoints) that is as far as the app goes, so later
 * screens rely on the profile's shared code (Compose, navigation, the
 * design system) and on runtime compilation.
 */
@RunWith(AndroidJUnit4::class)
class BaselineProfileGenerator {

    @get:Rule
    val rule = BaselineProfileRule()

    @Test
    fun generate() = rule.collect(packageName = PKG) {
        pressHome()
        startActivityAndWait()
        device.text("Welcome to Aegis AI")

        device.text("Language").click()
        device.text("Phone's language")
        device.pressBack()

        device.text("Get Protected").click()
        device.text("Your SIM")
        device.text("Verify Phone Number").click()
        device.text("Enter Phone Number")
        device.pressBack()
        device.text("Your SIM")
        device.pressBack()
        device.text("Welcome to Aegis AI")
    }

    private fun UiDevice.text(text: String): UiObject2 =
        checkNotNull(wait(Until.findObject(By.text(text)), TIMEOUT_MS)) { "\"$text\" is not on screen" }

    private companion object {
        const val PKG = "com.callscreen.profile"
        const val TIMEOUT_MS = 10_000L
    }
}
