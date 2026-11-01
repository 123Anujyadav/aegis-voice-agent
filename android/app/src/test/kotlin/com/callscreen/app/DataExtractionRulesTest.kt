package com.callscreen.app

import java.io.File
import javax.xml.parsers.DocumentBuilderFactory
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test
import org.w3c.dom.Element

/**
 * Application data stays on this phone (the manifest's note on allowBackup):
 * no cloud backup, and on Android 12+ no device-to-device transfer either.
 * Read from the sources, as the build packages them.
 */
@DisplayName("Backup and device-transfer rules")
class DataExtractionRulesTest {

    private val manifest = File("src/main/AndroidManifest.xml")
    private val rules = File("src/main/res/xml/data_extraction_rules.xml")

    private fun parse(file: File) = DocumentBuilderFactory.newInstance().apply { isNamespaceAware = true }
        .newDocumentBuilder().parse(file).documentElement

    @Test
    @DisplayName("backup is off and the manifest applies the extraction rules")
    fun manifestReferencesTheRules() {
        val application = parse(manifest).getElementsByTagName("application").item(0) as Element
        val android = "http://schemas.android.com/apk/res/android"
        assertEquals("false", application.getAttributeNS(android, "allowBackup"))
        assertEquals("@xml/data_extraction_rules", application.getAttributeNS(android, "dataExtractionRules"))
    }

    @Test
    @DisplayName("every domain is excluded from cloud backup and from device transfer, and nothing is included")
    fun everyDomainExcluded() {
        val root = parse(rules)
        assertEquals(0, root.getElementsByTagName("include").length, "an include would copy data off the phone")
        for (section in listOf("cloud-backup", "device-transfer")) {
            val element = root.getElementsByTagName(section).item(0) as? Element
            assertTrue(element != null, "$section is missing")
            val excluded = (0 until element!!.getElementsByTagName("exclude").length)
                .map { element.getElementsByTagName("exclude").item(it) as Element }
                .filter { it.getAttribute("path") == "." }
                .map { it.getAttribute("domain") }
                .toSet()
            assertEquals(setOf("root", "file", "database", "sharedpref", "external"), excluded, section)
        }
    }
}
