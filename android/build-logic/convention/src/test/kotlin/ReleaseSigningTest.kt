import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertFalse
import org.junit.jupiter.api.Assertions.assertNull
import org.junit.jupiter.api.Assertions.assertThrows
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.Test

/** CS_UPLOAD_*: none set gives an unsigned release; all set signs it; anything else fails without leaking a value. */
class ReleaseSigningTest {

    private val complete = mapOf(
        ReleaseSigning.KEYSTORE_FILE to "/tmp/upload.jks",
        ReleaseSigning.KEYSTORE_PASSWORD to "store-secret-1",
        ReleaseSigning.KEY_ALIAS to "upload",
        ReleaseSigning.KEY_PASSWORD to "key-secret-2",
    )
    private val secrets = listOf("store-secret-1", "key-secret-2")

    @Test
    fun noneSetMeansAnUnsignedRelease() {
        assertNull(ReleaseSigning.uploadKey({ null }, { true }))
    }

    @Test
    fun allSetSignsWithThatKey() {
        val key = ReleaseSigning.uploadKey(complete::get) { it == "/tmp/upload.jks" }!!
        assertEquals("/tmp/upload.jks", key.storeFile)
        assertEquals("store-secret-1", key.storePassword)
        assertEquals("upload", key.keyAlias)
        assertEquals("key-secret-2", key.keyPassword)
    }

    @Test
    fun aPartialOrEmptyConfigurationFailsAndNamesOnlyTheVariables() {
        for (name in ReleaseSigning.VARIABLES) {
            for (broken in listOf(complete - name, complete + (name to ""))) {
                val error = assertThrows(IllegalArgumentException::class.java) {
                    ReleaseSigning.uploadKey(broken::get) { true }
                }
                val message = error.message.orEmpty()
                assertTrue(message.contains(name), "$name: $message")
                assertFalse(secrets.any { message.contains(it) }, "a secret leaked: $message")
            }
        }
    }

    @Test
    fun aMissingKeystoreFileFails() {
        val error = assertThrows(IllegalArgumentException::class.java) {
            ReleaseSigning.uploadKey(complete::get) { false }
        }
        assertTrue(error.message.orEmpty().contains(ReleaseSigning.KEYSTORE_FILE))
    }

    @Test
    fun theKeyNeverPrintsItsSecrets() {
        val shown = ReleaseSigning.uploadKey(complete::get) { true }.toString()
        assertFalse(secrets.any { shown.contains(it) }, shown)
    }
}
