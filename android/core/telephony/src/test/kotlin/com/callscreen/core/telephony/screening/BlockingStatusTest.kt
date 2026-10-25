package com.callscreen.core.telephony.screening

import java.io.File
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test

@DisplayName("Whether blocks take effect")
class BlockingStatusTest {

    @Test
    @DisplayName("Android 9 and earlier cannot block; a declined or revoked role is off; contacts need access")
    fun status() {
        for (contacts in listOf(true, false)) {
            assertEquals(BlockingStatus.UNSUPPORTED, BlockingStatus.of(ScreeningRoleStatus.UNAVAILABLE, contacts))
            assertEquals(BlockingStatus.ROLE_NOT_HELD, BlockingStatus.of(ScreeningRoleStatus.NOT_HELD, contacts))
        }
        assertEquals(BlockingStatus.ACTIVE_EXCEPT_CONTACTS, BlockingStatus.of(ScreeningRoleStatus.HELD, false))
        assertEquals(BlockingStatus.ACTIVE, BlockingStatus.of(ScreeningRoleStatus.HELD, true))
    }

    @Test
    @DisplayName("no production code reads the contacts provider (contacts stay on the phone, unread)")
    fun contactsAreNeverRead() {
        // Unit tests run in the module directory: android/core/telephony.
        val android = File("../..").canonicalFile
        val sources = android.walkTopDown()
            .onEnter { it.name != "build" && it.name != ".gradle" && it.name != "test" && it.name != "androidTest" }
            .filter { it.isFile && it.extension in setOf("kt", "java") }
            .filter { "${File.separator}main${File.separator}" in it.path }
            .toList()
        assertTrue(sources.size > 50, "found only ${sources.size} sources under $android")
        val readers = sources.filter { f ->
            val text = f.readText()
            "ContactsContract" in text || "content://com.android.contacts" in text
        }
        assertEquals(emptyList<File>(), readers)
    }
}
