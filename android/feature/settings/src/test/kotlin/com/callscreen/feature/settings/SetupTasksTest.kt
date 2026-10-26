package com.callscreen.feature.settings

import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.telephony.screening.ScreeningRoleStatus
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/** The Settings checklist reports the phone's actual state, and nothing invented. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class SetupTasksTest {

    @get:Rule
    val compose = createComposeRule()

    private fun statuses(role: ScreeningRoleStatus, contacts: Boolean) = SetupTasks.of(role, contacts).map { it.status }

    @Test
    fun eachStateIsReportedAsItIs() {
        assertEquals(listOf(TaskStatus.DONE, TaskStatus.DONE, TaskStatus.NOT_AVAILABLE), statuses(ScreeningRoleStatus.HELD, true))
        assertEquals(listOf(TaskStatus.DONE, TaskStatus.TO_DO, TaskStatus.NOT_AVAILABLE), statuses(ScreeningRoleStatus.HELD, false))
        assertEquals(listOf(TaskStatus.TO_DO, TaskStatus.TO_DO, TaskStatus.NOT_AVAILABLE), statuses(ScreeningRoleStatus.NOT_HELD, false))
        // Android 8/9: blocking cannot work, so neither can blocking saved contacts.
        assertEquals(
            listOf(TaskStatus.NOT_AVAILABLE, TaskStatus.NOT_AVAILABLE, TaskStatus.NOT_AVAILABLE),
            statuses(ScreeningRoleStatus.UNAVAILABLE, true),
        )
        // Forwarding is never claimed: no platform number exists.
        for (role in ScreeningRoleStatus.entries) {
            assertEquals(TaskStatus.NOT_AVAILABLE, SetupTasks.of(role, true).single { it.title == SetupTasks.FORWARDING }.status)
        }
    }

    @Test
    fun theScreenShowsTheComputedTasksAndNoSampleOnes() {
        compose.setContent { CallScreenTheme { TaskCenterScreen(tasks = SetupTasks.of(ScreeningRoleStatus.NOT_HELD, false)) } }
        compose.onNodeWithText(SetupTasks.BLOCKING).assertExists()
        compose.onNodeWithText(SetupTasks.FORWARDING).assertExists()
        compose.onNodeWithText("Not available", ignoreCase = true).assertExists()
        for (sample in listOf("David", "Fraud Database", "telemetry", "Microphone Calibration")) {
            compose.onNodeWithText(sample, substring = true).assertDoesNotExist()
        }
    }
}
