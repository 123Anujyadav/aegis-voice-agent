package com.callscreen.core.ui.language

import android.app.Activity
import android.app.LocaleManager
import android.content.Context
import android.content.res.Resources
import android.os.LocaleList
import androidx.test.core.app.ApplicationProvider
import com.callscreen.core.ui.R
import java.io.File
import java.util.Locale
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/** An activity that applies the app language as MainActivity does. */
class LanguageTestActivity : Activity() {
    override fun attachBaseContext(newBase: Context) {
        super.attachBaseContext(AppLanguageSetting.wrap(newBase))
    }
}

private val context: Context get() = ApplicationProvider.getApplicationContext()

private fun prefs() = context.getSharedPreferences(AppLanguageSetting.FILE, Context.MODE_PRIVATE)

/**
 * Android 8 to 12 (here API 28 and 26): the choice is kept in the app's own
 * preferences file, applied to each activity as it is created, and survives
 * the process because it is written to disk before the activity is re-created.
 */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [26, 28])
class AppLanguageSettingBeforeAndroid13Test {

    private val phoneDefault: Locale = Locale.getDefault()

    @After
    fun restoreDefault() = Locale.setDefault(phoneDefault)

    @Test
    fun nothingChosenIsThePhonesLanguageAndChangesNothing() {
        assertNull(AppLanguageSetting.selected(context))
        assertSame(context, AppLanguageSetting.wrap(context))
    }

    @Test
    fun aChoiceIsAppliedToNewActivitiesAndIsOnDisk() {
        assertTrue("a change must re-create the activity", AppLanguageSetting.select(context, AppLanguage.TAMIL))
        assertFalse("choosing it again changes nothing", AppLanguageSetting.select(context, AppLanguage.TAMIL))
        assertEquals(AppLanguage.TAMIL, AppLanguageSetting.selected(context))

        val wrapped = AppLanguageSetting.wrap(context)
        assertEquals("ta", wrapped.resources.configuration.locales[0].language)
        assertEquals("number and date formats follow it", "ta", Locale.getDefault().language)

        // A new process reads the choice from this file (SharedPreferences are written by commit()).
        val file = File(context.applicationInfo.dataDir, "shared_prefs/${AppLanguageSetting.FILE}.xml")
        assertTrue("$file is not on disk", file.isFile)
        assertTrue(file.readText(), file.readText().contains(">ta<"))
    }

    @Test
    fun englishIsTheFallbackForStringsALanguageLacks() {
        AppLanguageSetting.select(context, AppLanguage.HINDI)
        val hindi = AppLanguageSetting.wrap(context).resources
        // The app has no Hindi translation yet: its words fall back to English (res/values)...
        assertEquals(context.getString(R.string.core_ui_tab_calls), hindi.getString(R.string.core_ui_tab_calls))
        // ...while the platform's own Hindi strings show the language really was applied.
        assertNotEquals(Resources.getSystem().getString(android.R.string.cancel), hindi.getString(android.R.string.cancel))
    }

    @Test
    fun choosingThePhonesLanguageAgainRemovesTheChoice() {
        AppLanguageSetting.select(context, AppLanguage.KANNADA)
        AppLanguageSetting.wrap(context)
        assertTrue(AppLanguageSetting.select(context, null))
        assertNull(AppLanguageSetting.selected(context))
        assertFalse(prefs().contains(AppLanguageSetting.KEY))
        assertSame(context, AppLanguageSetting.wrap(context))
        assertEquals(Resources.getSystem().configuration.locales[0], Locale.getDefault())
    }

    @Test
    fun anUnsupportedOrDamagedValueIsThePhonesLanguage() {
        for (stored in listOf("bn", "fr", "", "zz-ZZ", "%%")) {
            prefs().edit().putString(AppLanguageSetting.KEY, stored).commit()
            assertNull(stored, AppLanguageSetting.selected(context))
            assertSame(stored, context, AppLanguageSetting.wrap(context))
        }
    }

    @Test
    fun aReCreatedActivityIsInTheChosenLanguage() {
        val controller = Robolectric.buildActivity(LanguageTestActivity::class.java).setup()
        assertNotEquals("ml", controller.get().resources.configuration.locales[0].language)
        assertTrue(AppLanguageSetting.select(controller.get(), AppLanguage.MALAYALAM))
        controller.recreate()
        assertEquals("ml", controller.get().resources.configuration.locales[0].language)
        controller.pause().stop().destroy()
    }
}

/**
 * Android 13 and later: Android's per-app language holds the choice, so the
 * app keeps nothing of its own, and a choice made before an OS upgrade moves
 * into it once.
 */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [33, 35])
class AppLanguageSettingAndroid13Test {

    private val manager: LocaleManager get() = context.getSystemService(LocaleManager::class.java)

    @Before
    fun nothingChosen() {
        manager.applicationLocales = LocaleList.getEmptyLocaleList()
    }

    @Test
    fun theChoiceIsAndroidsPerAppLanguageAndTheAppKeepsNoCopy() {
        assertNull(AppLanguageSetting.selected(context))
        assertFalse("Android re-creates the activity itself", AppLanguageSetting.select(context, AppLanguage.TELUGU))
        assertEquals(LocaleList.forLanguageTags("te"), manager.applicationLocales)
        assertEquals(AppLanguage.TELUGU, AppLanguageSetting.selected(context))
        assertFalse(prefs().contains(AppLanguageSetting.KEY))
        assertSame("Android applies it, not the app", context, AppLanguageSetting.wrap(context))

        AppLanguageSetting.select(context, null)
        assertTrue(manager.applicationLocales.isEmpty)
        assertNull(AppLanguageSetting.selected(context))
    }

    @Test
    fun aLanguageSetInAndroidsSettingsIsReadAndAnUnsupportedOneIsThePhonesLanguage() {
        manager.applicationLocales = LocaleList.forLanguageTags("hi-IN")
        assertEquals(AppLanguage.HINDI, AppLanguageSetting.selected(context))
        manager.applicationLocales = LocaleList.forLanguageTags("fr")
        assertNull(AppLanguageSetting.selected(context))
    }

    @Test
    fun aChoiceFromBeforeTheUpgradeMovesIntoAndroidsSettingOnce() {
        prefs().edit().putString(AppLanguageSetting.KEY, "kn").commit()
        AppLanguageSetting.migrate(context)
        assertEquals(LocaleList.forLanguageTags("kn"), manager.applicationLocales)
        assertFalse(prefs().contains(AppLanguageSetting.KEY))
    }

    @Test
    fun aChoiceMadeInAndroidSinceTheUpgradeWins() {
        manager.applicationLocales = LocaleList.forLanguageTags("ta")
        prefs().edit().putString(AppLanguageSetting.KEY, "kn").commit()
        AppLanguageSetting.migrate(context)
        assertEquals(LocaleList.forLanguageTags("ta"), manager.applicationLocales)
        assertFalse(prefs().contains(AppLanguageSetting.KEY))
    }

    @Test
    fun anUnsupportedOldChoiceIsDroppedNotApplied() {
        prefs().edit().putString(AppLanguageSetting.KEY, "bn").commit()
        AppLanguageSetting.migrate(context)
        assertTrue(manager.applicationLocales.isEmpty)
        assertFalse(prefs().contains(AppLanguageSetting.KEY))
    }
}
