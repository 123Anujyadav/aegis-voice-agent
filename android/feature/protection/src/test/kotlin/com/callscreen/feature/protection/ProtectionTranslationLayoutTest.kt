package com.callscreen.feature.protection

import com.callscreen.core.testing.language.TranslationLayout
import com.callscreen.core.telephony.callerlist.CallerListEntry
import com.callscreen.core.telephony.callerlist.ListSource
import com.callscreen.core.telephony.prefilter.E164Number
import com.callscreen.core.telephony.prefilter.ListType
import com.callscreen.core.telephony.screening.BlockingStatus
import androidx.compose.ui.test.junit4.createComposeRule
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.ParameterizedRobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

/**
 * Protection in each release language's configuration, with words half as long
 * again in that language's script (TranslationLayout, PseudoTranslation: a
 * stand-in, not a translation), at twice the font scale in 320 x 480 dp. Every
 * action and message must still be reachable. English runs as it ships.
 */
@RunWith(ParameterizedRobolectricTestRunner::class)
@Config(sdk = [34])
// Real text measurement, with each script's glyphs: Robolectric's default
// graphics give every glyph a fixed size and ignore the font scale.
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class ProtectionTranslationLayoutTest(tag: String) {

    @get:Rule
    val compose = createComposeRule()

    private val layout = TranslationLayout(compose, tag)

    private fun s(id: Int): String = RuntimeEnvironment.getApplication().getString(id)

    private fun reachable(english: String) = layout.reachable(english)

    private val entry = CallerListEntry(
        requireNotNull(E164Number.of("+919876500001")), ListType.BLOCK, ListSource.FROM_CALL, 1_700_000_000, label = "Courier",
    )

    private fun list(state: CallerListState, notice: BlockingNoticeState? = null) = layout.show {
        CallerListScreen(
            state = state,
            notice = notice,
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

    @Test
    fun dashboardLinksStayReachable() {
        layout.show { ProtectionDashboardScreen() }
        reachable(s(R.string.protection_dashboard_app_bar)) // the top bar's title, wrapped whole
        reachable(s(R.string.protection_list_blocked_title))
        reachable(s(R.string.protection_list_allowed_title))
        reachable(s(DashboardCopy.NO_THREATS))
    }

    @Test
    fun entryDetailActionsStayReachable() {
        layout.show { EntryDetail(entry, onSaveLabel = {}, onMove = {}, onRemove = {}) }
        reachable(s(CallerListCopy.SAVE_LABEL))
        reachable(s(R.string.protection_list_move_to_allowed))
        reachable(s(CallerListCopy.REMOVE))
    }

    @Test
    fun anEmptyListStillStatesItsGuarantee() {
        list(CallerListState(type = ListType.BLOCK, loading = false))
        reachable(s(R.string.protection_list_empty_blocked_rings))
        reachable(s(R.string.protection_list_empty_blocked_offline))
    }

    @Test
    fun theLongestNoticeKeepsItsWordsAndItsAction() {
        list(
            CallerListState(type = ListType.BLOCK, loading = false, entries = listOf(entry)),
            notice = BlockingNoticeState(BlockingStatus.ACTIVE_EXCEPT_CONTACTS, contactsDenied = true),
        )
        reachable(s(BlockingNoticeCopy.CONTACTS))
        reachable(s(BlockingNoticeCopy.CONTACTS_DENIED))
        reachable(s(BlockingNoticeCopy.OPEN_SETTINGS))
    }

    @Test
    fun anUnreadableListCanStillBeRecovered() {
        list(CallerListState(type = ListType.BLOCK, loading = false, unreadable = true))
        reachable(s(CallerListCopy.START_EMPTY))
    }

    companion object {
        @JvmStatic
        @ParameterizedRobolectricTestRunner.Parameters(name = "{0}")
        fun tags(): List<Array<Any>> = TranslationLayout.TAGS.map { arrayOf<Any>(it) }
    }
}
