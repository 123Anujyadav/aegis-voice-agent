package com.callscreen.core.security.dpop

import java.security.KeyPairGenerator
import java.security.Signature
import java.security.interfaces.ECPublicKey
import java.security.spec.ECGenParameterSpec
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertNotEquals
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test

@DisplayName("Device proof key")
class DeviceProofKeyTest {

    /** A software key store, standing in for Android Keystore on the JVM. */
    private class SoftwareKeys : ProofKeyStore {
        var key: ProofKey? = null
        var created = 0
        var deleted = 0

        override fun loadOrCreate(): ProofKey = key ?: software().also { key = it; created++ }

        override fun delete() {
            key = null
            deleted++
        }

        private fun software(): ProofKey {
            val pair = KeyPairGenerator.getInstance("EC").apply { initialize(ECGenParameterSpec("secp256r1")) }.generateKeyPair()
            return object : ProofKey {
                override val publicKey = pair.public as ECPublicKey
                override fun signDer(data: ByteArray): ByteArray =
                    Signature.getInstance("SHA256withECDSA").run { initSign(pair.private); update(data); sign() }
            }
        }
    }

    @Test
    @DisplayName("the key is created once, on first use, and kept")
    fun stable() {
        val keys = SoftwareKeys()
        val device = DeviceProofKey(keys)
        assertEquals(0, keys.created, "nothing is created before the first proof")
        val first = device.proofs().jwk.thumbprint
        assertEquals(first, device.proofs().jwk.thumbprint)
        assertEquals(1, keys.created)
    }

    @Test
    @DisplayName("discard destroys the key, and the next proof is by a new one")
    fun discard() {
        val keys = SoftwareKeys()
        val device = DeviceProofKey(keys)
        val before = device.proofs().jwk.thumbprint
        device.discard()
        assertEquals(1, keys.deleted)
        val after = device.proofs().jwk.thumbprint
        assertNotEquals(before, after, "a new key after discard")
        assertEquals(2, keys.created)
    }
}
