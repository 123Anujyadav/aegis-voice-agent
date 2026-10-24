/**
 * The release build's upload-key signing, from four CI environment variables.
 * The keystore and its passwords are secrets: they come only from the
 * environment of the release workflow, never from a build file (Phase 1 §12).
 *
 * None set means an unsigned release build, as before: debug, staging and
 * every PR build need no key. All set means release is signed with the upload
 * key. Some but not all set is a misconfigured release, and fails the build
 * rather than quietly producing an unsigned bundle. No message ever includes a
 * value, only variable names.
 */
internal object ReleaseSigning {

    const val KEYSTORE_FILE: String = "CS_UPLOAD_KEYSTORE_FILE"
    const val KEYSTORE_PASSWORD: String = "CS_UPLOAD_KEYSTORE_PASSWORD"
    const val KEY_ALIAS: String = "CS_UPLOAD_KEY_ALIAS"
    const val KEY_PASSWORD: String = "CS_UPLOAD_KEY_PASSWORD"

    val VARIABLES: List<String> = listOf(KEYSTORE_FILE, KEYSTORE_PASSWORD, KEY_ALIAS, KEY_PASSWORD)

    /**
     * The upload key described by [env], or null when none of the variables
     * is set. [isFile] checks the keystore path, which must be absolute: a
     * relative one would resolve against the Gradle daemon's directory.
     */
    fun uploadKey(env: (String) -> String?, isFile: (String) -> Boolean): UploadKey? {
        val values = VARIABLES.associateWith(env)
        if (values.values.all { it == null }) return null
        val missing = VARIABLES.filter { values[it].isNullOrEmpty() }
        require(missing.isEmpty()) {
            "Release signing needs all of ${VARIABLES.joinToString()}; missing or empty: ${missing.joinToString()}"
        }
        val storeFile = values.getValue(KEYSTORE_FILE)!!
        require(isFile(storeFile)) { "$KEYSTORE_FILE must be the absolute path of a readable keystore file" }
        return UploadKey(
            storeFile = storeFile,
            storePassword = values.getValue(KEYSTORE_PASSWORD)!!,
            keyAlias = values.getValue(KEY_ALIAS)!!,
            keyPassword = values.getValue(KEY_PASSWORD)!!,
        )
    }
}

/** An upload key's location and passwords. [toString] never shows them. */
internal class UploadKey(
    val storeFile: String,
    val storePassword: String,
    val keyAlias: String,
    val keyPassword: String,
) {
    override fun toString(): String = "UploadKey(<redacted>)"
}
