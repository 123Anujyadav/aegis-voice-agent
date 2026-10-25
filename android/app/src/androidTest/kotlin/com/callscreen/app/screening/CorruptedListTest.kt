package com.callscreen.app.screening

import androidx.activity.compose.setContent
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.callscreen.app.MainActivity
import com.callscreen.app.navigation.AegisDestinations
import com.callscreen.app.navigation.AegisNavGraph
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.telephony.callerlist.AtomicRecordFile
import com.callscreen.core.telephony.callerlist.CallerListStore
import com.callscreen.core.telephony.callerlist.StoreHealth
import com.callscreen.core.telephony.screening.TelephonyModule
import dagger.hilt.android.testing.BindValue
import dagger.hilt.android.testing.HiltAndroidRule
import dagger.hilt.android.testing.HiltAndroidTest
import dagger.hilt.android.testing.UninstallModules
import java.io.File
import java.util.UUID
import org.junit.Assert.assertEquals
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/** An unreadable stored list is shown as such and kept until the subscriber starts again. */
@HiltAndroidTest
@UninstallModules(TelephonyModule::class)
@RunWith(AndroidJUnit4::class)
class CorruptedListTest {

    @get:Rule(order = 0)
    val hilt = HiltAndroidRule(this)

    @get:Rule(order = 1)
    val compose = createAndroidComposeRule<MainActivity>()

    private val file = File(
        InstrumentationRegistry.getInstrumentation().targetContext.cacheDir,
        "test-lists/${UUID.randomUUID()}.json",
    ).apply {
        parentFile?.mkdirs()
        writeText("not a list")
    }

    @BindValue
    @JvmField
    val store: CallerListStore = CallerListStore(AtomicRecordFile(file))

    @Before
    fun start() {
        hilt.inject()
        compose.activity.setContent { CallScreenTheme { AegisNavGraph(startDestination = AegisDestinations.PROTECTION_DASHBOARD) } }
    }

    @Test
    fun unreadableListIsKeptUntilTheSubscriberStartsAgain() {
        compose.scrolledTo("Blocked numbers").performClick()
        compose.awaitText("can't be read on this phone", substring = true)
        assertEquals("not a list", file.readText())
        compose.onNodeWithText("Start again with empty lists").performClick()
        compose.awaitText("No blocked numbers")
        assertEquals(StoreHealth.READY, CallerListStore(AtomicRecordFile(file)).health())
    }
}
