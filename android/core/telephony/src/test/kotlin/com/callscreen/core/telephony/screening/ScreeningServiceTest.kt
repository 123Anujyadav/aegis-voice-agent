package com.callscreen.core.telephony.screening

import com.callscreen.core.telephony.prefilter.ListType
import com.callscreen.core.telephony.prefilter.PreFilter
import com.callscreen.core.telephony.prefilter.PreFilterDecision
import com.callscreen.core.telephony.prefilter.ScreeningResponse
import java.io.File
import javax.xml.parsers.DocumentBuilderFactory
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test
import org.w3c.dom.Element

@DisplayName("Screening service")
class ScreeningServiceTest {

    private val filter = PreFilter({ if (it.value == "+919876500001") ListType.BLOCK else null }, { false })

    @Test
    @DisplayName("a blocked number, however Android presents it, is rejected; anything else rings")
    fun responses() {
        val reject = ScreeningResponse.of(PreFilterDecision.REJECT)
        for (raw in listOf("+919876500001", "+91 98765 00001", "09876500001")) {
            assertEquals(reject, ScreeningCall.respond(raw, filter), raw)
        }
        for (raw in listOf("+919876500002", null, "", "112")) {
            assertEquals(ScreeningCall.ALLOW, ScreeningCall.respond(raw, filter), raw.toString())
        }
        assertEquals(
            ScreeningResponse(disallowCall = false, rejectCall = false, silenceCall = false, skipNotification = false),
            ScreeningCall.ALLOW,
        )
    }

    @Test
    @DisplayName("the manifest exports the service only to the system, and asks for no permission")
    fun manifest() {
        // Unit tests run in the module directory.
        val file = File("src/main/AndroidManifest.xml")
        assertTrue(file.isFile, "run from android/core/telephony: ${file.absolutePath}")
        val doc = DocumentBuilderFactory.newInstance().apply { isNamespaceAware = true }.newDocumentBuilder().parse(file)
        val android = "http://schemas.android.com/apk/res/android"

        assertEquals(0, doc.getElementsByTagName("uses-permission").length, "no user permission")
        val services = doc.getElementsByTagName("service")
        assertEquals(1, services.length)
        val service = services.item(0) as Element
        assertEquals(PreFilterScreeningService::class.java.name, service.getAttributeNS(android, "name"))
        // Exported for the system to bind, so it MUST require the permission only the system holds.
        assertEquals("true", service.getAttributeNS(android, "exported"))
        assertEquals("android.permission.BIND_SCREENING_SERVICE", service.getAttributeNS(android, "permission"))
        val actions = service.getElementsByTagName("action")
        assertEquals(1, actions.length)
        assertEquals("android.telecom.CallScreeningService", (actions.item(0) as Element).getAttributeNS(android, "name"))
    }
}
