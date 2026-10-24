package com.callscreen.core.security.dpop

import android.os.Build
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import java.security.KeyPairGenerator
import java.security.KeyStore
import java.security.PrivateKey
import java.security.ProviderException
import java.security.Signature
import java.security.interfaces.ECPublicKey
import java.security.spec.ECGenParameterSpec

/**
 * The device key (ADR-0010 §5(c)): an EC P-256 key generated inside Android
 * Keystore, non-exportable, usable only to sign with SHA-256. StrongBox is
 * used where the device has it, and the TEE otherwise.
 *
 * Verified on a device only: Android Keystore does not exist on the JVM, so
 * the JVM unit tests exercise [DpopProofFactory] through a java.security key
 * instead.
 */
public class KeystoreProofKey private constructor(
    private val privateKey: PrivateKey,
    override val publicKey: ECPublicKey,
) : ProofKey {

    override fun signDer(data: ByteArray): ByteArray =
        Signature.getInstance("SHA256withECDSA").run {
            initSign(privateKey)
            update(data)
            sign()
        }

    public companion object {
        private const val PROVIDER = "AndroidKeyStore"

        /** The default alias of the identity device key. */
        public const val DEFAULT_ALIAS: String = "callscreen.identity.device-key.v1"

        /**
         * The key under [alias], generating it on first use. The key survives
         * app restarts and is lost on uninstall, which ends the enrolment it
         * was bound to (a new device, as far as identity can tell).
         */
        public fun loadOrCreate(alias: String = DEFAULT_ALIAS): KeystoreProofKey {
            val store = KeyStore.getInstance(PROVIDER).apply { load(null) }
            val existing = store.getEntry(alias, null) as? KeyStore.PrivateKeyEntry
            if (existing != null) {
                return KeystoreProofKey(existing.privateKey, existing.certificate.publicKey as ECPublicKey)
            }
            val pair = generate(alias, strongBox = Build.VERSION.SDK_INT >= Build.VERSION_CODES.P)
            return KeystoreProofKey(pair.private, pair.public as ECPublicKey)
        }

        private fun generate(alias: String, strongBox: Boolean): java.security.KeyPair {
            val spec = KeyGenParameterSpec.Builder(alias, KeyProperties.PURPOSE_SIGN)
                .setAlgorithmParameterSpec(ECGenParameterSpec("secp256r1"))
                .setDigests(KeyProperties.DIGEST_SHA256)
                .apply { if (strongBox && Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) setIsStrongBoxBacked(true) }
                .build()
            return try {
                KeyPairGenerator.getInstance(KeyProperties.KEY_ALGORITHM_EC, PROVIDER).run {
                    initialize(spec)
                    generateKeyPair()
                }
            } catch (e: ProviderException) {
                // StrongBoxUnavailableException (API 28+) is a ProviderException;
                // it is caught by its supertype so this class still loads on
                // API 26-27, where StrongBox is never requested.
                if (!strongBox) throw e
                generate(alias, strongBox = false)
            }
        }
    }
}
