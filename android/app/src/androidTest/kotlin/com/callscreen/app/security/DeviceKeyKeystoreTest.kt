package com.callscreen.app.security

import androidx.test.ext.junit.runners.AndroidJUnit4
import com.callscreen.core.security.dpop.DeviceProofKey
import com.callscreen.core.security.dpop.KeystoreProofKeys
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.fail
import org.junit.Test
import org.junit.runner.RunWith

/**
 * The device key's destruction in REAL Android Keystore (the JVM tests use a
 * software key): after discard, the destroyed key can no longer sign, so a
 * proof for an access token issued before the deletion cannot be made, and
 * the next key is a different one. A test alias is used, never the app's.
 */
@RunWith(AndroidJUnit4::class)
class DeviceKeyKeystoreTest {

    private val keys = KeystoreProofKeys(ALIAS)

    @After
    fun cleanUp() = keys.delete()

    @Test
    fun discardDestroysTheKeystoreKeyAndTheNextOneIsNew() {
        val device = DeviceProofKey(keys)
        val before = device.proofs()
        val thumbprint = before.jwk.thumbprint
        before.proof("POST", "https://identity.example.test/v1/account/delete") // signs with the Keystore key
        assertEquals("the key is kept until discarded", thumbprint, DeviceProofKey(keys).proofs().jwk.thumbprint)

        device.discard()

        try {
            before.proof("GET", "https://edge.example.test/v1/me", "an.access.token")
            fail("the destroyed Keystore key still signed a proof")
        } catch (expected: Exception) {
            // Keystore refuses the deleted key: nothing can present the old token.
        }
        assertNotEquals("a new key after discard", thumbprint, device.proofs().jwk.thumbprint)
    }

    private companion object {
        const val ALIAS = "callscreen.test.device-key-discard"
    }
}
