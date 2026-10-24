package com.callscreen.core.datastore.session

import java.security.GeneralSecurityException
import javax.crypto.Cipher
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec
import kotlinx.serialization.Serializable
import kotlinx.serialization.SerializationException
import kotlinx.serialization.json.Json

/*
 * The identity session at rest, encrypted.
 *
 * Plain Kotlin and javax.crypto only: the model, the sealing format and the
 * recovery rules are shared logic, testable on the JVM. The platform supplies
 * two pieces, a secret key (KeystoreSessionKey on Android: non-exportable
 * AES-256 in Android Keystore) and a byte store (DataStoreBlobStore on
 * Android). A future iOS client would supply a Keychain key and storage
 * instead.
 *
 * The session is NEVER stored in plaintext. Anything that cannot be opened
 * safely (a failed authentication tag, a truncated or corrupted record, an
 * unknown format, a key that is missing or no longer usable) is cleared and
 * reported as Unrecoverable: the user must sign in again. Nothing is guessed,
 * and no error carries record contents.
 */

/** What identity issued for this device. toString never shows a token. */
@Serializable
public data class StoredSession(
    val sessionId: String,
    val accessToken: String,
    val refreshToken: String,
    /** When the access token expires, in epoch seconds, from the server's expiresIn. */
    val accessTokenExpiresAtEpochSeconds: Long,
) {
    override fun toString(): String =
        "StoredSession(sessionId=<redacted>, accessToken=<redacted>, refreshToken=<redacted>, " +
            "accessTokenExpiresAtEpochSeconds=$accessTokenExpiresAtEpochSeconds)"
}

/** The outcome of reading the store. */
public sealed interface StoredSessionResult {
    public data class Present(val session: StoredSession) : StoredSessionResult
    public data object Absent : StoredSessionResult

    /** A record existed but could not be opened safely. It has been cleared: sign in again. */
    public data object Unrecoverable : StoredSessionResult
}

/** Raw persistence of one opaque record. Writes must be atomic (all or nothing). */
public interface BlobStore {
    public suspend fun read(): ByteArray?
    public suspend fun write(bytes: ByteArray)
    public suspend fun clear()
}

/** Supplies the sealing key. */
public interface SessionKey {
    /** The key, creating it if absent. */
    public fun getOrCreate(): SecretKey

    /** The key if it exists and is usable, or null. Never creates one. */
    public fun existing(): SecretKey?

    /** Deletes the key, for example after it stopped being usable. */
    public fun delete()
}

/** A record that could not be opened. Carries no contents. */
public class UnsealException internal constructor(reason: String) : Exception(reason)

/**
 * AES-256-GCM sealing with a fixed associated-data context, so a record
 * sealed for another purpose is refused.
 *
 * Format: version (1 byte, 0x01) || IV (12 bytes) || ciphertext || tag (16 bytes).
 * The IV comes from the cipher (Android Keystore requires that it generate it).
 */
public object SessionSealer {
    private const val VERSION: Byte = 0x01
    private const val IV_BYTES = 12
    private const val TAG_BITS = 128
    private const val TRANSFORMATION = "AES/GCM/NoPadding"
    private val AAD = "callscreen.identity.session.v1".toByteArray(Charsets.US_ASCII)

    public fun seal(key: SecretKey, plaintext: ByteArray): ByteArray {
        val cipher = Cipher.getInstance(TRANSFORMATION)
        cipher.init(Cipher.ENCRYPT_MODE, key)
        cipher.updateAAD(AAD)
        val iv = cipher.iv
        check(iv != null && iv.size == IV_BYTES) { "the cipher did not supply a 12-byte IV" }
        val body = cipher.doFinal(plaintext)
        return byteArrayOf(VERSION) + iv + body
    }

    public fun open(key: SecretKey, sealed: ByteArray): ByteArray {
        if (sealed.size < 1 + IV_BYTES + TAG_BITS / 8) throw UnsealException("record too short")
        if (sealed[0] != VERSION) throw UnsealException("unknown record format")
        return try {
            Cipher.getInstance(TRANSFORMATION).run {
                init(Cipher.DECRYPT_MODE, key, GCMParameterSpec(TAG_BITS, sealed, 1, IV_BYTES))
                updateAAD(AAD)
                doFinal(sealed, 1 + IV_BYTES, sealed.size - 1 - IV_BYTES)
            }
        } catch (e: GeneralSecurityException) {
            // AEADBadTagException for a tampered or wrong-key record; other
            // subclasses for a key the platform will no longer use.
            throw UnsealException("record could not be opened")
        }
    }
}

/** The encrypted session store over a platform [key] and [blobs]. */
public class EncryptedSessionStore(
    private val key: SessionKey,
    private val blobs: BlobStore,
) {
    private val json = Json { ignoreUnknownKeys = false }

    public suspend fun read(): StoredSessionResult {
        val sealed = blobs.read() ?: return StoredSessionResult.Absent
        val k = runCatching { key.existing() }.getOrNull() ?: return unrecoverable()
        val plaintext = try {
            SessionSealer.open(k, sealed)
        } catch (e: UnsealException) {
            return unrecoverable()
        }
        val session = try {
            json.decodeFromString(StoredSession.serializer(), plaintext.toString(Charsets.UTF_8))
        } catch (e: SerializationException) {
            return unrecoverable()
        } catch (e: IllegalArgumentException) {
            return unrecoverable()
        }
        return StoredSessionResult.Present(session)
    }

    public suspend fun write(session: StoredSession) {
        val plaintext = json.encodeToString(StoredSession.serializer(), session).toByteArray(Charsets.UTF_8)
        blobs.write(SessionSealer.seal(key.getOrCreate(), plaintext))
    }

    /** Signs out locally: the record is removed. The key is kept for the next session. */
    public suspend fun clear() {
        blobs.clear()
    }

    /** Clears an unopenable record and its unusable key, so the next sign-in starts clean. */
    private suspend fun unrecoverable(): StoredSessionResult {
        blobs.clear()
        runCatching { key.delete() }
        return StoredSessionResult.Unrecoverable
    }
}
