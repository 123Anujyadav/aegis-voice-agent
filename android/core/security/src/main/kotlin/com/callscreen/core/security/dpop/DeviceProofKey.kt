package com.callscreen.core.security.dpop

import java.security.KeyStore

/** Where the device key lives: Android Keystore in the app ([KeystoreProofKeys]), a software key in JVM tests. */
public interface ProofKeyStore {
    /** The current key, generating one if there is none. */
    public fun loadOrCreate(): ProofKey

    /** Destroys the current key. The next [loadOrCreate] generates a new one. */
    public fun delete()
}

/** The device key in Android Keystore under [alias] (see [KeystoreProofKey]). */
public class KeystoreProofKeys(private val alias: String = KeystoreProofKey.DEFAULT_ALIAS) : ProofKeyStore {
    override fun loadOrCreate(): ProofKey = KeystoreProofKey.loadOrCreate(alias)

    override fun delete() {
        KeyStore.getInstance("AndroidKeyStore").apply { load(null) }.deleteEntry(alias)
    }
}

/**
 * This device's proof key, shared by every signer (identity and edge-api),
 * and created on first use, off the main thread.
 *
 * [discard] destroys it, so the next proof comes from a NEW key. The app calls
 * it only after the server CONFIRMED the account's deletion (204): the key is
 * the credential a deletion retry needs, so it must survive any outcome that
 * is not a confirmed deletion. Afterwards a sign-in from this phone enrols a
 * new key, unlinkable to the erased account's (docs/phase19/ACCOUNT-DELETION.md
 * R3), and an access token issued before the deletion can no longer be
 * presented, since nothing can sign a proof with the destroyed key.
 */
public class DeviceProofKey(
    private val store: ProofKeyStore,
    private val factory: (ProofKey) -> DpopProofFactory = { DpopProofFactory(it) },
) {
    private val lock = Any()

    @Volatile
    private var current: DpopProofFactory? = null

    /** The proof factory for the current key. */
    public fun proofs(): DpopProofFactory =
        current ?: synchronized(lock) { current ?: factory(store.loadOrCreate()).also { current = it } }

    /** Destroys the key. The next [proofs] uses a new one. */
    public fun discard() {
        synchronized(lock) {
            current = null
            store.delete()
        }
    }
}
