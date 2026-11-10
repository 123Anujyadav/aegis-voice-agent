package com.callscreen.feature.protection

import android.content.Context
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.telephony.prefilter.ListType
import com.callscreen.core.telephony.screening.PreferencesFlag
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config

/**
 * A32's "cleared" tier (owner delegation 2026-11-10, decision packet §3b, option A):
 * a list the subscriber emptied shows its heading alone; a first-run list keeps its
 * explanation. The remembered flag is one yes or no per list, in its own file.
 */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class CallerListClearedTierTest {

    @get:Rule
    val compose = createComposeRule()

    private val app: Context get() = RuntimeEnvironment.getApplication()

    private fun s(id: Int): String = app.getString(id)

    private fun show(state: CallerListState) {
        compose.setContent {
            CallScreenTheme {
                CallerListScreen(
                    state = state,
                    notice = null,
                    onNoticeAction = {},
                    onQueryChange = {},
                    onAdd = {},
                    onRemove = {},
                    onSetLabel = { _, _ -> },
                    onMove = {},
                    onUndo = {},
                    onMessageShown = {},
                    onDiscardUnreadable = {},
                )
            }
        }
    }

    @Test
    fun firstRunBlocklistExplainsItself() {
        show(CallerListState(ListType.BLOCK, loading = false))
        compose.onNodeWithText(s(R.string.protection_list_empty_blocked_title)).assertExists()
        compose.onNodeWithText(s(R.string.protection_list_empty_blocked_rings)).assertExists()
        compose.onNodeWithText(s(R.string.protection_list_empty_blocked_offline)).assertExists()
    }

    @Test
    fun clearedBlocklistShowsItsHeadingAlone() {
        show(CallerListState(ListType.BLOCK, loading = false, cleared = true))
        compose.onNodeWithText(s(R.string.protection_list_empty_blocked_title)).assertExists()
        compose.onNodeWithText(s(R.string.protection_list_empty_blocked_rings)).assertDoesNotExist()
        compose.onNodeWithText(s(R.string.protection_list_empty_blocked_offline)).assertDoesNotExist()
    }

    @Test
    fun clearedAllowlistShowsItsHeadingAlone() {
        show(CallerListState(ListType.ALLOW, loading = false, cleared = true))
        compose.onNodeWithText(s(R.string.protection_list_empty_allowed_title)).assertExists()
        compose.onNodeWithText(s(R.string.protection_list_empty_allowed)).assertDoesNotExist()
    }

    @Test
    fun theFlagsPersistApartPerListAndApartFromTheScreeningRoleFile() {
        val block = CallerListViewModel.hadEntriesFlag(app, ListType.BLOCK)
        assertFalse(block.get())
        block.set()
        assertTrue("a new instance reads the stored flag", CallerListViewModel.hadEntriesFlag(app, ListType.BLOCK).get())
        assertFalse("the allowlist has its own flag", CallerListViewModel.hadEntriesFlag(app, ListType.ALLOW).get())
        assertFalse(
            "the screening-role file is untouched",
            app.getSharedPreferences("callscreen_screening_role", Context.MODE_PRIVATE).contains("blocklist_had_entries"),
        )
        assertTrue(app.getSharedPreferences(CallerListViewModel.LISTS_FILE, Context.MODE_PRIVATE).getBoolean("blocklist_had_entries", false))
        assertFalse(PreferencesFlag(app, PreferencesFlag.HELD_BEFORE).get())
    }
}
