package com.callscreen.core.datastore.session

import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import kotlinx.coroutines.test.runTest
import org.junit.jupiter.api.Assertions.assertArrayEquals
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertFalse
import org.junit.jupiter.api.Assertions.assertNotNull
import org.junit.jupiter.api.Assertions.assertNull
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.assertThrows

/**
 * The session store's sealing and recovery rules, with a javax.crypto key and
 * an in-memory byte store standing in for Android Keystore and DataStore.
 */
@DisplayName("Encrypted session store")
class EncryptedSessionStoreTest {

    private class MemoryBlobs : BlobStore {
        var bytes: ByteArray? = null
        override suspend fun read() = bytes
        override suspend fun write(bytes: ByteArray) { this.bytes = bytes.copyOf() }
        override suspend fun clear() { bytes = null }
    }

    private class MemoryKey : SessionKey {
        var key: SecretKey? = null
        var failOnRead = false
        override fun getOrCreate(): SecretKey = key ?: aes().also { key = it }
        override fun existing(): SecretKey? = if (failOnRead) throw IllegalStateException("keystore unavailable") else key
        override fun delete() { key = null }
    }

    private val session = StoredSession(
        sessionId = "sess_" + "b".repeat(64),
        accessToken = "eyJ.access.token",
        refreshToken = "c".repeat(64),
        accessTokenExpiresAtEpochSeconds = 1_760_000_900,
    )

    @Test
    @DisplayName("a written session reads back exactly, and nothing is stored in plaintext")
    fun roundTripAndNoPlaintext() = runTest {
        val blobs = MemoryBlobs()
        val store = EncryptedSessionStore(MemoryKey(), blobs)
        store.write(session)
        assertEquals(StoredSessionResult.Present(session), store.read())
        val raw = String(blobs.bytes!!, Charsets.ISO_8859_1)
        for (secret in listOf(session.accessToken, session.refreshToken, session.sessionId)) {
            assertFalse(raw.contains(secret), "the stored record contains a token in plaintext")
        }
    }

    @Test
    @DisplayName("nothing stored reads as Absent")
    fun absent() = runTest {
        assertEquals(StoredSessionResult.Absent, EncryptedSessionStore(MemoryKey(), MemoryBlobs()).read())
    }

    @Test
    @DisplayName("two seals of one session differ (fresh IV) and both open")
    fun freshIv() {
        val key = aes()
        val a = SessionSealer.seal(key, "x".toByteArray())
        val b = SessionSealer.seal(key, "x".toByteArray())
        assertFalse(a.contentEquals(b))
        assertArrayEquals("x".toByteArray(), SessionSealer.open(key, a))
        assertArrayEquals("x".toByteArray(), SessionSealer.open(key, b))
        assertEquals(1 + 12 + 1 + 16, a.size, "version + IV + ciphertext + tag")
    }

    @Test
    @DisplayName("any tampered byte, truncation, unknown version or wrong key is refused")
    fun sealerRefusesTampering() {
        val key = aes()
        val sealed = SessionSealer.seal(key, "the session record".toByteArray())
        for (i in sealed.indices) {
            val bad = sealed.copyOf().also { it[i] = (it[i].toInt() xor 0x01).toByte() }
            assertThrows<UnsealException>("byte $i") { SessionSealer.open(key, bad) }
        }
        assertThrows<UnsealException> { SessionSealer.open(key, sealed.copyOf(sealed.size - 1)) }
        assertThrows<UnsealException> { SessionSealer.open(key, sealed.copyOf(20)) }
        assertThrows<UnsealException> { SessionSealer.open(key, ByteArray(0)) }
        assertThrows<UnsealException> { SessionSealer.open(aes(), sealed) }
    }

    @Test
    @DisplayName("a corrupted record is Unrecoverable, cleared, and its key discarded")
    fun corruptedRecord() = runTest {
        val blobs = MemoryBlobs()
        val key = MemoryKey()
        val store = EncryptedSessionStore(key, blobs)
        store.write(session)
        blobs.bytes!!.let { it[it.size - 1] = (it[it.size - 1].toInt() xor 0x01).toByte() }
        assertEquals(StoredSessionResult.Unrecoverable, store.read())
        assertNull(blobs.bytes, "the unopenable record was kept")
        assertNull(key.key, "the key was kept")
        assertEquals(StoredSessionResult.Absent, store.read())
        store.write(session) // signing in again works with a fresh key
        assertEquals(StoredSessionResult.Present(session), store.read())
    }

    @Test
    @DisplayName("a missing or unusable key with a record present is Unrecoverable, never a guess")
    fun missingOrUnusableKey() = runTest {
        val blobs = MemoryBlobs()
        val key = MemoryKey()
        val store = EncryptedSessionStore(key, blobs)
        store.write(session)
        key.key = null
        assertEquals(StoredSessionResult.Unrecoverable, store.read())
        assertNull(blobs.bytes)

        store.write(session)
        key.failOnRead = true
        assertEquals(StoredSessionResult.Unrecoverable, store.read())
        assertNull(blobs.bytes)
    }

    @Test
    @DisplayName("a record sealed under another key is Unrecoverable")
    fun anotherKey() = runTest {
        val blobs = MemoryBlobs()
        EncryptedSessionStore(MemoryKey(), blobs).write(session)
        val other = MemoryKey().also { it.getOrCreate() }
        assertEquals(StoredSessionResult.Unrecoverable, EncryptedSessionStore(other, blobs).read())
    }

    @Test
    @DisplayName("a validly sealed record that is not a session is Unrecoverable")
    fun notASession() = runTest {
        val blobs = MemoryBlobs()
        val key = MemoryKey()
        blobs.bytes = SessionSealer.seal(key.getOrCreate(), """{"unexpected":true}""".toByteArray())
        assertEquals(StoredSessionResult.Unrecoverable, EncryptedSessionStore(key, blobs).read())
    }

    @Test
    @DisplayName("clear removes the record and keeps the key for the next session")
    fun clear() = runTest {
        val blobs = MemoryBlobs()
        val key = MemoryKey()
        val store = EncryptedSessionStore(key, blobs)
        store.write(session)
        store.clear()
        assertEquals(StoredSessionResult.Absent, store.read())
        assertNotNull(key.key)
    }

    @Test
    @DisplayName("toString never shows a token, and errors carry no contents")
    fun nothingLeaks() {
        val shown = session.toString() + StoredSessionResult.Present(session).toString()
        assertTrue(listOf(session.accessToken, session.refreshToken, session.sessionId).none { shown.contains(it) }, shown)
        val key = aes()
        val e = assertThrows<UnsealException> { SessionSealer.open(key, SessionSealer.seal(aes(), "secret-payload".toByteArray())) }
        assertFalse(e.message.orEmpty().contains("secret-payload"))
    }

    private companion object {
        fun aes(): SecretKey = KeyGenerator.getInstance("AES").apply { init(256) }.generateKey()
    }
}
