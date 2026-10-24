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
    @DisplayName("a Cloud project number must be a positive decimal; anything else means no integrity tokens")
    fun cloudProjectNumber() {
        assertEquals(123456789012L, EndpointConfig.cloudProjectNumber("123456789012"))
        for (bad in listOf("", "0", "-1", "12a", "1 2", "99999999999999999999999")) {
            assertNull(EndpointConfig.cloudProjectNumber(bad), bad)
        }
    }
}
