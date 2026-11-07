package com.callscreen.core.datastore.session

import androidx.datastore.core.CorruptionException
import androidx.datastore.core.DataStore
import androidx.datastore.core.handlers.ReplaceFileCorruptionHandler
import androidx.datastore.preferences.core.PreferenceDataStoreFactory
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.stringPreferencesKey
import java.io.File
import java.io.IOException
import kotlin.random.Random
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import org.junit.jupiter.api.Assertions.assertArrayEquals
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertFalse
import org.junit.jupiter.api.Assertions.assertNotNull
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.io.TempDir

/**
 * The session DataStore's handling of a file it cannot parse (OD-13,
 * production-readiness F3), on real DataStore files on the JVM, with the
 * handler the app's session store uses ([sessionStoreCorruptionHandler]).
 * The app-level wiring, through the real delegate and [DataStoreBlobStore],
 * is tested in :app's SessionFileCorruptionTest.
 */
@DisplayName("Session DataStore file corruption")
class SessionStoreCorruptionTest {

    @TempDir
    lateinit var dir: File

    /** The session file's name, as the delegate names it ("<name>.preferences_pb"). */
    private val file: File get() = File(dir, "callscreen_identity_session.preferences_pb")

    /** DataStoreBlobStore's only key. */
    private val sealed = stringPreferencesKey("sealed_session")

    /**
     * Opens one DataStore on [file] with [handler] (none for the control),
     * runs [block] within a time limit, and closes it: DataStore allows only
     * one active instance per file, so the next one may then open it.
     */
    private fun <T> withStore(
        handler: ReplaceFileCorruptionHandler<Preferences>? = sessionStoreCorruptionHandler(),
        block: suspend (DataStore<Preferences>) -> T,
    ): T = runBlocking {
        val job = Job()
        val store = PreferenceDataStoreFactory.create(
            corruptionHandler = handler,
            scope = CoroutineScope(Dispatchers.IO + job),
            produceFile = { file },
        )
        try {
            withTimeout(10_000) { block(store) }
        } finally {
            job.cancelAndJoin()
        }
    }

    private suspend fun DataStore<Preferences>.readError(): Throwable? = runCatching { data.first() }.exceptionOrNull()

    /** A valid session file holding a long record, cut in half. */
    private fun truncatedFile(): ByteArray {
        withStore(handler = null) { store -> store.edit { it[sealed] = "A".repeat(2_000) } }
        val whole = file.readBytes()
        file.delete()
        return whole.copyOf(whole.size / 2)
    }

    /** Unparseable session files: an invalid field tag, seeded random bytes, and a truncated record. */
    private fun corruptFiles(): Map<String, ByteArray> = linkedMapOf(
        "invalid tag" to ByteArray(64) { 0xFF.toByte() },
        "random bytes" to Random(20261107).nextBytes(512),
        "truncated" to truncatedFile(),
    )

    @Test
    @DisplayName("A0 control: without the handler, every corrupt file fails every read and write")
    fun controlWithoutHandler() {
        for ((name, bytes) in corruptFiles()) {
            file.writeBytes(bytes)
            withStore(handler = null) { store ->
                assertTrue(store.readError() is CorruptionException, "$name: read should fail with CorruptionException")
                val write = runCatching { store.edit { it[sealed] = "new" } }.exceptionOrNull()
                assertTrue(write is CorruptionException, "$name: write should fail with CorruptionException")
                // No caching of the failure: the next read fails again.
                assertTrue(store.readError() is CorruptionException, "$name: second read should fail too")
            }
            assertArrayEquals(bytes, file.readBytes(), "$name: the control must leave the file as it was")
            file.delete()
        }
    }

    @Test
    @DisplayName("A1 a corrupt file (invalid tag, random, truncated) recovers to empty and is rewritten as a valid file")
    fun corruptFileRecoversToEmpty() {
        for ((name, bytes) in corruptFiles()) {
            file.writeBytes(bytes)
            val recovered = withStore { it.data.first() }
            assertTrue(recovered.asMap().isEmpty(), "$name: recovers to empty preferences")
            assertFalse(file.readBytes().contentEquals(bytes), "$name: the corrupt bytes are replaced on disk")
            // Valid on disk: a store WITHOUT the handler now reads it.
            val reread = withStore(handler = null) { it.data.first() }
            assertTrue(reread.asMap().isEmpty(), "$name: the replacement is a valid, empty file")
            file.delete()
        }
    }

    @Test
    @DisplayName("A2 a missing file is the normal empty state, and reading it creates nothing")
    fun missingFileIsEmpty() {
        assertFalse(file.exists())
        val read = withStore { it.data.first() }
        assertTrue(read.asMap().isEmpty())
        assertFalse(file.exists(), "reading a missing file must not create one")
    }

    @Test
    @DisplayName("A3 a valid file is read as it is and left byte-for-byte unchanged")
    fun validFileUnchanged() {
        withStore(handler = null) { store -> store.edit { it[sealed] = "c2VhbGVkLXJlY29yZA" } }
        val before = file.readBytes()
        val stamp = 1_000_000_000_000L
        assertTrue(file.setLastModified(stamp))

        val value = withStore { it.data.first()[sealed] }

        assertEquals("c2VhbGVkLXJlY29yZA", value)
        assertArrayEquals(before, file.readBytes(), "a valid file must not be rewritten")
        assertEquals(stamp, file.lastModified(), "a valid file must not be replaced")
    }

    @Test
    @DisplayName("A4 after recovery, a new session is written and persists")
    fun freshSessionPersistsAfterRecovery() {
        file.writeBytes(ByteArray(64) { 0xFF.toByte() })
        withStore { store ->
            assertTrue(store.data.first().asMap().isEmpty())
            store.edit { it[sealed] = "fresh-record" }
        }
        // A new instance, as after a restart, with and without the handler.
        assertEquals("fresh-record", withStore(handler = null) { it.data.first()[sealed] })
        assertEquals("fresh-record", withStore { it.data.first()[sealed] })
    }

    @Test
    @DisplayName("A5 after recovery, repeated reads neither fail nor rewrite the file")
    fun noRepeatedRewrites() {
        file.writeBytes(Random(7).nextBytes(256).also { it[0] = 0xFF.toByte() })
        val stamp = 1_000_000_000_000L
        withStore { store ->
            assertTrue(store.data.first().asMap().isEmpty())
            // Mark the recovered file: a rewrite replaces the file and its time.
            assertTrue(file.setLastModified(stamp))
            repeat(3) { assertTrue(store.data.first().asMap().isEmpty()) }
        }
        assertEquals(stamp, file.lastModified(), "repeated reads must not rewrite the file")
        // A new instance finds a valid file: the handler has nothing to replace.
        repeat(2) { assertTrue(withStore { it.data.first() }.asMap().isEmpty()) }
        assertEquals(stamp, file.lastModified(), "reopening must not rewrite the file")
    }

    @Test
    @DisplayName("A6 an I/O error that is not corruption still fails, and nothing is replaced")
    fun otherIoErrorsPropagate() {
        // A directory where the file should be: unreadable, but not corrupt data.
        assertTrue(file.mkdirs())
        val error = withStore { it.readError() }
        assertNotNull(error)
        assertTrue(error is IOException, "an I/O error: $error")
        assertFalse(error is CorruptionException, "must not be treated as corruption")
        assertTrue(file.isDirectory, "nothing may be replaced")
    }
}
