package com.callscreen.core.datastore.session

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import androidx.datastore.core.DataStore
import androidx.datastore.core.handlers.ReplaceFileCorruptionHandler
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.emptyPreferences
import androidx.datastore.preferences.core.stringPreferencesKey
import androidx.datastore.preferences.preferencesDataStore
import java.security.GeneralSecurityException
import java.security.KeyStore
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import kotlinx.coroutines.flow.first

/**
 * The session-sealing key: AES-256 generated inside Android Keystore,
 * non-exportable, usable only for AES-GCM with a Keystore-generated IV.
 * Device-only: Android Keystore does not exist on the JVM, so the unit tests
 * exercise [SessionSealer] and [EncryptedSessionStore] with a javax.crypto key.
 */
public class KeystoreSessionKey(private val alias: String = DEFAULT_ALIAS) : SessionKey {

    override fun getOrCreate(): SecretKey = existing() ?: generate()

    override fun existing(): SecretKey? = try {
        (store().getEntry(alias, null) as? KeyStore.SecretKeyEntry)?.secretKey
    } catch (e: GeneralSecurityException) {
        null
    }

    override fun delete() {
        store().deleteEntry(alias)
    }

    private fun generate(): SecretKey = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, PROVIDER).run {
        init(
            KeyGenParameterSpec.Builder(alias, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .setRandomizedEncryptionRequired(true)
                .build(),
        )
        generateKey()
    }

    private fun store(): KeyStore = KeyStore.getInstance(PROVIDER).apply { load(null) }

    public companion object {
        private const val PROVIDER = "AndroidKeyStore"
        public const val DEFAULT_ALIAS: String = "callscreen.identity.session-key.v1"
    }
}

/**
 * What the session DataStore does with a file it cannot parse (OD-13,
 * production-readiness F3; owner decision 2026-11-07): replace it with an
 * empty one. Without it, every read and every write of the file fails, so
 * the phone could never store a session again.
 *
 * It runs only for a file that is not valid Preferences data. A file that
 * parses but holds an unopenable record still goes through
 * [EncryptedSessionStore]'s Unrecoverable path, and other I/O errors still
 * throw. The file holds only the sealed session, so the session is all that
 * is lost: the store then reads Absent, and the user signs in again. No key
 * is touched and nothing is sent.
 */
internal fun sessionStoreCorruptionHandler(): ReplaceFileCorruptionHandler<Preferences> =
    ReplaceFileCorruptionHandler { emptyPreferences() }

private val Context.sessionDataStore: DataStore<Preferences> by preferencesDataStore(
    name = "callscreen_identity_session",
    corruptionHandler = sessionStoreCorruptionHandler(),
)

/**
 * The sealed record in a Preferences DataStore. DataStore writes are atomic (a
 * temporary file is renamed over the old one), so an interrupted write leaves
 * the previous record, never a partial one. Only ciphertext is ever stored.
 */
public class DataStoreBlobStore(context: Context) : BlobStore {
    private val store = context.applicationContext.sessionDataStore
    private val key = stringPreferencesKey("sealed_session")

    override suspend fun read(): ByteArray? {
        val encoded = store.data.first()[key] ?: return null
        return try {
            Base64.decode(encoded, Base64.NO_WRAP)
        } catch (e: IllegalArgumentException) {
            ByteArray(0) // unreadable: EncryptedSessionStore treats it as an unopenable record
        }
    }

    override suspend fun write(bytes: ByteArray) {
        store.edit { it[key] = Base64.encodeToString(bytes, Base64.NO_WRAP) }
    }

    override suspend fun clear() {
        store.edit { it.remove(key) }
    }
}
