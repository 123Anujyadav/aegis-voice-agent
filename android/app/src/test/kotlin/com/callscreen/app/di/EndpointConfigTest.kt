package com.callscreen.app.di

import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertNull
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test

/** The endpoint configuration that decides whether sign-in, edge calls and integrity tokens are on. */
@DisplayName("Endpoint configuration")
class EndpointConfigTest {

    @Test
    @DisplayName("only an https origin is accepted; anything else leaves the feature off")
    fun httpsOrigin() {
        assertEquals("https://identity.example.test", EndpointConfig.httpsOrigin("https://identity.example.test"))
        assertEquals("https://identity.example.test", EndpointConfig.httpsOrigin("https://identity.example.test/"))
        assertEquals("https://edge.example.test:8443", EndpointConfig.httpsOrigin("https://edge.example.test:8443"))
        for (bad in listOf(
            "", // unset: off, no host assumed
            "http://identity.example.test", // plain http, even for loopback, in the app
            "http://127.0.0.1:8080",
            "https://identity.example.test/api", // a path
            "https://identity.example.test/?x=1", // a query
            "https://user:pass@identity.example.test", // credentials in the URL
            "identity.example.test", // no scheme
            "not a url",
        )) {
            assertNull(EndpointConfig.httpsOrigin(bad), bad)
        }
    }

    @Test
    @DisplayName("with no valid project number no integrity work happens and no token is ever sent")
    fun integrityTokensOnlyWithAProject() {
        var created = 0
        for (bad in listOf("", "0", "abc")) {
            val tokens = EndpointConfig.integrityTokens(bad) { created++; { "token" } }
            assertNull(tokens.tokenFor("proof"), bad)
        }
        assertEquals(0, created, "Play Integrity was set up without a project")

        var boundTo: String? = null
        val tokens = EndpointConfig.integrityTokens("123456789012") { project ->
            assertEquals(123456789012L, project)
            { proof -> boundTo = proof; null } // a failed acquisition: still no token
        }
        assertNull(tokens.tokenFor("the-proof"))
        assertEquals("the-proof", boundTo, "the token must be requested for the proof it accompanies")
    }

    @Test
    @DisplayName("a Cloud project number must be a positive decimal; anything else means no integrity tokens")
    fun cloudProjectNumber() {
        assertEquals(123456789012L, EndpointConfig.cloudProjectNumber("123456789012"))
        for (bad in listOf("", "0", "-1", "12a", "1 2", "99999999999999999999999")) {
            assertNull(EndpointConfig.cloudProjectNumber(bad), bad)
        }
    }
}
