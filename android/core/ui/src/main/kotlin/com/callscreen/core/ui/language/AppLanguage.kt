package com.callscreen.core.ui.language

import java.util.Locale

/**
 * A language the app's screens can be shown in: the owner's six (OD-17), in
 * the owner's order, English first. It is exactly what the release packages
 * (app/release-locales.txt; the app's AppLanguageListTest holds the two equal).
 *
 * This is the language of the app's screens only. The language the assistant
 * speaks and understands on calls is a separate setting with its own limits
 * (A43, ADR-0005), and choosing a screen language never changes it.
 *
 * @property tag the BCP 47 language code, as packaged
 * @property translated whether every release screen has a human-reviewed
 *   translation in this language. English is the source. Until a language's
 *   translation is reviewed and in the app, it can still be chosen: the picker
 *   says it is not translated yet, and its screens show English.
 */
public enum class AppLanguage(public val tag: String, public val translated: Boolean) {
    ENGLISH("en", translated = true),
    HINDI("hi", translated = false),
    TAMIL("ta", translated = false),
    TELUGU("te", translated = false),
    MALAYALAM("ml", translated = false),
    KANNADA("kn", translated = false),
    ;

    /**
     * The locale a choice applies: the language in India, the product's
     * market (ta-IN), as Android's own language setting gives it ("Tamil
     * (India)"), so resources and formats kept for the Indian region match;
     * English stays plain "en". The app's translations are kept by language
     * (values-ta), which matches either.
     */
    public val locale: Locale get() = Locale.forLanguageTag(if (this == ENGLISH) tag else "$tag-IN")

    /**
     * The language's name in itself ("हिन्दी", "தமிழ்"), from the platform's
     * locale data (CLDR), so a reader finds their language whatever the
     * screens are shown in. It is not one of our translations.
     */
    public fun nativeName(): String = displayName(locale)

    /** The language's name in [inLocale], for the reader of the current screens. */
    public fun displayName(inLocale: Locale): String =
        locale.getDisplayLanguage(inLocale).replaceFirstChar { it.titlecase(inLocale) }

    public companion object {
        /**
         * The app language for [locale], matched on its language alone
         * ("hi-IN" is Hindi), or null when it is not one of the six: an
         * unsupported or unreadable value means "the phone's language".
         */
        public fun of(locale: Locale?): AppLanguage? =
            locale?.language?.takeIf { it.isNotEmpty() }?.let { language -> entries.firstOrNull { it.tag == language } }

        /** As [of], from a stored BCP 47 tag; null for null or anything that is not one of the six. */
        public fun ofTag(tag: String?): AppLanguage? = tag?.takeIf { it.isNotBlank() }?.let { of(Locale.forLanguageTag(it)) }
    }
}
