package com.callscreen.feature.protection

import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.hasScrollAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.performScrollToNode
import com.callscreen.core.designsystem.theme.CallScreenTheme
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/** Protection shows no score, count or threat report it has no source for. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class HonestDashboardUiTest {

    @get:Rule
    val compose = createComposeRule()

    @Test
    fun noInventedScoreCountsOrThreats() {
        compose.setContent { CallScreenTheme { ProtectionDashboardScreen() } }
        val invented = listOf("98/100", "142", "89", "Optimal", "412")
        compose.onNodeWithText(DashboardCopy.NO_DATA).assertExists()
        compose.onAllNodesWithText(DashboardCopy.NOT_AVAILABLE).assertCountEquals(2)
        invented.forEach { compose.onNodeWithText(it, substring = true).assertDoesNotExist() }
        // The list is lazy: each item is brought into composition before it is checked,
        // and the invented values are checked again at each point.
        for (item in listOf("Blocked numbers", "Allowed numbers", DashboardCopy.NO_THREATS)) {
            compose.onNode(hasScrollAction()).performScrollToNode(hasText(item))
            compose.onNodeWithText(item).assertExists()
            invented.forEach { compose.onNodeWithText(it, substring = true).assertDoesNotExist() }
        }
    }
}
