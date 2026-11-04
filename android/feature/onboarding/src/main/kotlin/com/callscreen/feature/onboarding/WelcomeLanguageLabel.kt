package com.callscreen.feature.onboarding

/**
 * Welcome's Language control label (owner decision D4, 2026-11-04): the fixed
 * bilingual "भाषा · Language", the same whatever language the screens are in,
 * so someone who cannot read that language still finds the way to change it.
 *
 * @property text what is shown and read
 * @property parts each part's BCP 47 language and its range in [text], so a
 *   screen reader reads the Hindi word with a Hindi voice and the English one
 *   with an English voice
 */
internal data class WelcomeLanguageLabel(val text: String, val parts: List<Pair<String, IntRange>>) {
    companion object {
        const val SEPARATOR: String = " · "

        fun of(hindi: String, english: String): WelcomeLanguageLabel {
            val text = hindi + SEPARATOR + english
            val englishStart = hindi.length + SEPARATOR.length
            return WelcomeLanguageLabel(text, listOf("hi" to hindi.indices, "en" to (englishStart until text.length)))
        }
    }
}
