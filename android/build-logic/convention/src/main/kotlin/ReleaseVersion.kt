/**
 * The app's versionCode and versionName, from the CI environment variables
 * CS_VERSION_CODE and CS_VERSION_NAME (configuration, NOT secrets).
 *
 * Unset means a developer build, which gets the local defaults. A value that
 * IS set but invalid fails the build: silently falling back to the default
 * would ship versionCode 1, which Google Play rejects for any app that has
 * ever been uploaded, or a release whose version nobody chose.
 */
internal object ReleaseVersion {

    const val DEFAULT_CODE: Int = 1
    const val DEFAULT_NAME: String = "0.1.0-dev"

    /**
     * "The greatest value Google Play allows for versionCode is 2100000000."
     * https://developer.android.com/studio/publish/versioning
     */
    const val MAX_CODE: Int = 2_100_000_000

    private const val MAX_NAME_LENGTH = 64
    private val NAME = Regex("^[0-9A-Za-z][0-9A-Za-z.+_-]*$")
    private val DIGITS = Regex("^[0-9]+$")

    /** [raw] (CS_VERSION_CODE) as a versionCode; [DEFAULT_CODE] when unset. */
    fun code(raw: String?): Int {
        if (raw == null) return DEFAULT_CODE
        val code = raw.takeIf { DIGITS.matches(it) && it.length <= 10 }?.toLongOrNull()
        require(code != null && code in 1..MAX_CODE.toLong()) {
            "CS_VERSION_CODE must be a whole number from 1 to $MAX_CODE, got \"$raw\""
        }
        return code.toInt()
    }

    /** [raw] (CS_VERSION_NAME) as a versionName; [DEFAULT_NAME] when unset. */
    fun name(raw: String?): String {
        if (raw == null) return DEFAULT_NAME
        require(raw.length <= MAX_NAME_LENGTH && NAME.matches(raw)) {
            "CS_VERSION_NAME must be 1-$MAX_NAME_LENGTH characters of letters, digits and . + _ -, " +
                "starting with a letter or digit, got \"$raw\""
        }
        return raw
    }
}
