import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.Test

class ReleaseEndpointsTest {

    private val pinA = "sha256/" + "A".repeat(42) + "A="
    private val pinB = "sha256/" + "B".repeat(42) + "A="

    private val complete = mapOf(
        ReleaseEndpoints.IDENTITY_BASE_URL to "https://identity.example.test",
        ReleaseEndpoints.EDGE_API_BASE_URL to "https://api.example.test:8443",
        ReleaseEndpoints.IDENTITY_CERT_PINS to "$pinA,$pinB",
        ReleaseEndpoints.EDGE_API_CERT_PINS to "$pinA, $pinB",
    )

    private fun problems(overrides: Map<String, String?>): List<String> {
        val values = complete + overrides
        return ReleaseEndpoints.problems { values[it] }
    }

    @Test
    fun aCompleteConfigurationPasses() {
        assertEquals(emptyList<String>(), problems(emptyMap()))
        assertEquals(emptyList<String>(), problems(mapOf(ReleaseEndpoints.IDENTITY_BASE_URL to "https://identity.example.test/")))
    }

    @Test
    fun nothingSetNamesEveryProperty() {
        val all = ReleaseEndpoints.problems { null }
        assertEquals(4, all.size)
        for (name in listOf(
            ReleaseEndpoints.IDENTITY_BASE_URL, ReleaseEndpoints.EDGE_API_BASE_URL,
            ReleaseEndpoints.IDENTITY_CERT_PINS, ReleaseEndpoints.EDGE_API_CERT_PINS,
        )) {
            assertTrue(all.any { it.startsWith("$name ") }, "$name is not named: $all")
        }
    }

    @Test
    fun originsMustBeHttpsOriginsWithALowerCaseHost() {
        for (bad in listOf(
            "http://identity.example.test", "identity.example.test", "https://", "https://identity.example.test/v1",
            "https://identity.example.test?x=1", "https://user@identity.example.test", "https://Identity.Example.test",
            "https://127.0.0.1", "https://identity.example.test:0", "https://identity.example.test:99999",
            "https://identity.example.test:", "https://identity.example.test:99999999999", "https://-bad.example.test",
        )) {
            val found = problems(mapOf(ReleaseEndpoints.IDENTITY_BASE_URL to bad))
            assertEquals(1, found.size, "$bad: $found")
            assertTrue(found.single().startsWith(ReleaseEndpoints.IDENTITY_BASE_URL), "$bad: $found")
        }
    }

    @Test
    fun pinsMustBeTwoDistinctSha256Pins() {
        for (bad in listOf(pinA, "$pinA,$pinA", "sha1/abc,$pinB", "$pinA,", "sha256/short=,$pinB")) {
            val found = problems(mapOf(ReleaseEndpoints.EDGE_API_CERT_PINS to bad))
            assertEquals(1, found.size, "$bad: $found")
            assertTrue(found.single().startsWith(ReleaseEndpoints.EDGE_API_CERT_PINS), "$bad: $found")
        }
    }

    @Test
    fun messagesNeverEchoAValue() {
        val secretLooking = "https://identity.example.test/leak-this-path"
        val found = problems(mapOf(ReleaseEndpoints.IDENTITY_BASE_URL to secretLooking, ReleaseEndpoints.IDENTITY_CERT_PINS to "leak-this-pin"))
        assertTrue(found.none { "leak-this" in it }, "$found")
    }
}
