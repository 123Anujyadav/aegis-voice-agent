package com.callscreen.core.testing.language

import android.content.Context
import android.content.ContextWrapper
import android.content.res.Configuration
import android.content.res.Resources
import android.os.LocaleList
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.remember
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalContext
import java.util.Locale
import kotlin.math.ceil

/**
 * A stand-in for a translation, for layout tests only. It is never shipped and
 * is not a translation: no app text is translated, and nothing here leaves the
 * test.
 *
 * Under [tag]'s configuration (its locale, as the app would be with that
 * language chosen), every string a screen reads through [Resources] comes back
 * as its English, then filler in the language's own script, until it is
 * [growth] times as long. The filler is the language's name in itself, from the
 * platform's locale data. So a screen is laid out with longer words, in the
 * script's real glyphs and line heights, and a test can still find each string
 * by its English start.
 *
 * English ([tag] "en") is shown as it ships, unexpanded.
 */
public object PseudoTranslation {

    /**
     * Translated copy runs up to 40% longer than English (UX_FREEZE.md §9
     * item 2); 50%, plus at least four characters, leaves a margin, as short
     * labels grow the most.
     */
    public const val GROWTH: Float = 1.5f

    /** [english] lengthened as described above, with [filler] repeated. */
    public fun expand(english: String, filler: String, growth: Float = GROWTH): String {
        val target = maxOf(ceil(english.length * growth).toInt(), english.length + 4)
        val out = StringBuilder(english)
        while (out.length < target) out.append(' ').append(filler)
        return out.toString()
    }

    /** The filler for [tag]: the language's name in itself ("தமிழ்"). */
    public fun filler(tag: String): String {
        val locale = Locale.forLanguageTag(tag)
        return locale.getDisplayLanguage(locale)
    }
}

/**
 * Shows [content] as the app would with [tag] chosen, its strings replaced by
 * [PseudoTranslation]'s stand-ins (English as it ships).
 */
@Composable
public fun InPseudoTranslation(tag: String, growth: Float = PseudoTranslation.GROWTH, content: @Composable () -> Unit) {
    val base = LocalContext.current
    val configuration = remember(base, tag) {
        Configuration(base.resources.configuration).apply { setLocales(LocaleList.forLanguageTags(tag)) }
    }
    val context = remember(base, tag, growth) {
        val localized = base.createConfigurationContext(configuration)
        if (tag == "en") localized else PseudoContext(localized, PseudoTranslation.filler(tag), growth)
    }
    CompositionLocalProvider(LocalContext provides context, LocalConfiguration provides configuration) { content() }
}

private class PseudoContext(base: Context, filler: String, growth: Float) : ContextWrapper(base) {
    private val resources = PseudoResources(base.resources, filler, growth)
    override fun getResources(): Resources = resources
}

@Suppress("DEPRECATION") // The public constructor is the only way to subclass Resources.
private class PseudoResources(
    private val base: Resources,
    private val filler: String,
    private val growth: Float,
) : Resources(base.assets, base.displayMetrics, base.configuration) {

    private fun expand(text: CharSequence): String = PseudoTranslation.expand(text.toString(), filler, growth)

    override fun getText(id: Int): CharSequence = expand(base.getText(id))

    override fun getString(id: Int): String = expand(base.getString(id))

    override fun getString(id: Int, vararg formatArgs: Any?): String = expand(base.getString(id, *formatArgs))

    override fun getQuantityText(id: Int, quantity: Int): CharSequence = expand(base.getQuantityText(id, quantity))

    override fun getQuantityString(id: Int, quantity: Int): String = expand(base.getQuantityString(id, quantity))

    override fun getQuantityString(id: Int, quantity: Int, vararg formatArgs: Any?): String =
        expand(base.getQuantityString(id, quantity, *formatArgs))
}
