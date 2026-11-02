import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertThrows
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.Test
import java.io.File

/** app/release-locales.txt: the owner's six languages, English first, or the build fails. */
class ReleaseLocalesTest {

    @Test
    fun theReviewedFileIsTheOwnersSixLanguages() {
        // Gradle runs this test with the build-logic project as its working directory.
        val file = File("../../app/release-locales.txt")
        assertTrue(file.isFile, "app/release-locales.txt not found from ${File(".").absolutePath}")
        assertEquals(listOf("en", "hi", "ta", "te", "ml", "kn"), ReleaseLocales.parse(file.readText()))
    }

    @Test
    fun commentsAndBlankLinesAreIgnored() {
        assertEquals(listOf("en", "hi"), ReleaseLocales.parse("# header\n\nen\nhi # Hindi\n"))
    }

    @Test
    fun anythingButACleanListFailsTheBuild() {
        for (bad in listOf(
            "", "# nothing\n", "hi\nen\n", "en\nen\n", "en\nHI\n", "en\nhi-IN\n", "en\nhin\n", "en\nb+hi+Deva\n", "en\nhi,ta\n",
        )) {
            assertThrows(IllegalArgumentException::class.java, { ReleaseLocales.parse(bad) }, bad)
        }
    }
}
