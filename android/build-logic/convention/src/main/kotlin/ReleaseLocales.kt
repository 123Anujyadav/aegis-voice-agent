/**
 * The languages the app is packaged in, from `app/release-locales.txt`.
 *
 * One reviewed file feeds the build's `localeFilters`, the app's language list
 * (`BuildConfig.PACKAGED_LOCALES`) and CI's check of the built APK, so the three
 * cannot drift apart. A file that is not a clean list fails the build rather
 * than packaging a guessed set: a stray locale costs APK size, and a dropped
 * one silently strips a translation.
 */
internal object ReleaseLocales {

    /** The source language: listed first, because everything falls back to it. */
    const val SOURCE: String = "en"

    /** A bare ISO 639-1 language code. Regions and scripts are not packaged separately. */
    private val CODE = Regex("^[a-z]{2}$")

    /** The codes in [text], in order. Comments (`#`) and blank lines are ignored. */
    fun parse(text: String): List<String> {
        val codes = text.lineSequence()
            .map { it.substringBefore('#').trim() }
            .filter { it.isNotEmpty() }
            .toList()
        val bad = codes.filterNot { CODE.matches(it) }
        require(bad.isEmpty()) { "release-locales.txt: not a two-letter lower-case language code: $bad" }
        val repeated = codes.groupingBy { it }.eachCount().filterValues { it > 1 }.keys
        require(repeated.isEmpty()) { "release-locales.txt: listed more than once: $repeated" }
        require(codes.firstOrNull() == SOURCE) { "release-locales.txt: \"$SOURCE\", the source language, must be first, got $codes" }
        return codes
    }
}
