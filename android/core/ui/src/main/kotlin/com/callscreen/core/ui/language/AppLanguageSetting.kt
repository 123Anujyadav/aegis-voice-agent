package com.callscreen.core.ui.language

import android.app.LocaleManager
import android.content.Context
import android.content.SharedPreferences
import android.content.res.Configuration
import android.content.res.Resources
import android.os.Build
import android.os.LocaleList
import androidx.annotation.RequiresApi

/**
 * The language chosen for the app's screens, or none: the phone's language,
 * shown when the app has it, and English otherwise. English is `res/values`,
 * so any string a language lacks is shown in English (Android's own fallback).
 *
 * One place keeps the choice on each Android version, never two:
 * - Android 13 and later: Android's per-app language (LocaleManager). It
 *   survives restarts and app updates, it is the same setting as the system's
 *   App languages page, and Android re-creates the app's screens when it
 *   changes.
 * - Android 8 to 12 have no per-app language, so the choice is kept in this
 *   app's preferences file ([FILE]) and applied to each activity as it is
 *   created ([wrap]). The caller re-creates the activity after a change
 *   ([select] says when).
 *
 * Re-creating an activity is a configuration change: view models, the
 * navigation back stack and the stored session all survive it, so changing
 * language neither signs anyone out nor loses their place.
 */
public object AppLanguageSetting {

    /** Android 12 and earlier: the preferences file holding the choice, and its key. */
    internal const val FILE: String = "callscreen_app_language"
    internal const val KEY: String = "language"

    /** The chosen language, or null for the phone's language (also for anything unreadable or unsupported). */
    public fun selected(context: Context): AppLanguage? =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            AppLanguage.of(localeManager(context).applicationLocales.get(0))
        } else {
            AppLanguage.ofTag(prefs(context).getString(KEY, null))
        }

    /**
     * Chooses [language] (null: the phone's language). Returns true when the
     * caller must re-create the activity to show it: on Android 12 and
     * earlier, when the choice changed. Android 13 and later re-create it
     * themselves.
     */
    public fun select(context: Context, language: AppLanguage?): Boolean {
        if (language == selected(context)) return false
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            localeManager(context).applicationLocales = localeList(language)
            return false
        }
        // commit, not apply: the choice is on disk before the activity is re-created,
        // so a process that dies straight afterwards still starts in it.
        prefs(context).edit().apply { if (language == null) remove(KEY) else putString(KEY, language.tag) }.commit()
        return true
    }

    /**
     * For Activity.attachBaseContext. On Android 12 and earlier: [base] with
     * the chosen language applied, and the process's default locale (number
     * and date formats) set to match, as Android 13's per-app language does.
     * On Android 13 and later: [base], unchanged, because Android applies it.
     */
    public fun wrap(base: Context): Context {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) return base
        val language = selected(base)
        val locales = if (language == null) Resources.getSystem().configuration.locales else localeList(language)
        LocaleList.setDefault(locales)
        if (language == null) return base
        val configuration = Configuration(base.resources.configuration).apply { setLocales(locales) }
        return base.createConfigurationContext(configuration)
    }

    /**
     * Android 13 and later: a choice made on Android 12 or earlier, before the
     * phone was upgraded, moves into Android's own setting (unless one was
     * made there since) and leaves the preferences file, so there is still
     * only one. Nothing to do otherwise.
     */
    public fun migrate(context: Context) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) return
        val prefs = prefs(context)
        if (!prefs.contains(KEY)) return
        val language = AppLanguage.ofTag(prefs.getString(KEY, null))
        val manager = localeManager(context)
        if (language != null && manager.applicationLocales.isEmpty) manager.applicationLocales = localeList(language)
        prefs.edit().remove(KEY).commit()
    }

    private fun localeList(language: AppLanguage?): LocaleList =
        if (language == null) LocaleList.getEmptyLocaleList() else LocaleList(language.locale)

    private fun prefs(context: Context): SharedPreferences = context.getSharedPreferences(FILE, Context.MODE_PRIVATE)

    @RequiresApi(Build.VERSION_CODES.TIRAMISU)
    private fun localeManager(context: Context): LocaleManager = context.getSystemService(LocaleManager::class.java)
}
