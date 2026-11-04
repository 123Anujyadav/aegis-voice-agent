package com.callscreen.feature.calls

import com.callscreen.core.testing.language.TranslationLayout
import androidx.compose.ui.test.junit4.createComposeRule
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.ParameterizedRobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

/**
 * The Calls feed in each release language's configuration, with words half as long
 * again in that language's script (TranslationLayout, PseudoTranslation: a
 * stand-in, not a translation), at twice the font scale in 320 x 480 dp. Every
 * action and message must still be reachable. English runs as it ships.
 */
@RunWith(ParameterizedRobolectricTestRunner::class)
@Config(sdk = [34])
// Real text measurement, with each script's glyphs: Robolectric's default
// graphics give every glyph a fixed size and ignore the font scale.
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class CallsTranslationLayoutTest(tag: String) {

    @get:Rule
    val compose = createComposeRule()

    private val layout = TranslationLayout(compose, tag)

    private fun s(id: Int): String = RuntimeEnvironment.getApplication().getString(id)

    private fun reachable(english: String) = layout.reachable(english)

    @Test
    fun theEmptyFeedStillSaysWhy() {
        layout.show { CallsFeedScreen() }
        reachable(s(R.string.calls_feed_title)) // the top bar's title, wrapped whole
        reachable(s(R.string.calls_empty_title))
        reachable(s(R.string.calls_empty))
    }

    @Test
    fun withTheRoleLostTheBannerActionAndTheEmptyStateStayReachable() {
        layout.show { CallsFeedScreen(roleLost = true) }
        reachable(s(R.string.calls_role_switch_back))
        reachable(s(R.string.calls_empty))
    }

    companion object {
        @JvmStatic
        @ParameterizedRobolectricTestRunner.Parameters(name = "{0}")
        fun tags(): List<Array<Any>> = TranslationLayout.TAGS.map { arrayOf<Any>(it) }
    }
}
