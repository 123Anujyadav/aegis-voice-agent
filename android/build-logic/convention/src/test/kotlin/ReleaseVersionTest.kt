import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertThrows
import org.junit.jupiter.api.Test

/** CS_VERSION_CODE / CS_VERSION_NAME: unset gives the developer defaults; set but invalid fails the build. */
class ReleaseVersionTest {

    @Test
    fun unsetMeansADeveloperBuild() {
        assertEquals(ReleaseVersion.DEFAULT_CODE, ReleaseVersion.code(null))
        assertEquals(ReleaseVersion.DEFAULT_NAME, ReleaseVersion.name(null))
    }

    @Test
    fun validCodes() {
        assertEquals(1, ReleaseVersion.code("1"))
        assertEquals(4217, ReleaseVersion.code("4217"))
        assertEquals(ReleaseVersion.MAX_CODE, ReleaseVersion.code("2100000000"))
    }

    @Test
    fun anInvalidCodeFailsInsteadOfShippingTheDefault() {
        for (bad in listOf("", " ", "0", "-1", "+5", "1.0", "12a", " 12", "0x10", "2100000001", "2147483648", "99999999999999999999")) {
            assertThrows(IllegalArgumentException::class.java, { ReleaseVersion.code(bad) }, bad)
        }
    }

    @Test
    fun validNames() {
        for (good in listOf("1.0.0", "1.4.2-rc.1", "2026.10.24+build.7", "0.1.0_beta")) {
            assertEquals(good, ReleaseVersion.name(good))
        }
    }

    @Test
    fun anInvalidNameFails() {
        for (bad in listOf("", " ", "-1.0", ".1", "1.0 beta", "1.0\n", "v1\"", "a".repeat(65))) {
            assertThrows(IllegalArgumentException::class.java, { ReleaseVersion.name(bad) }, bad)
        }
    }
}
