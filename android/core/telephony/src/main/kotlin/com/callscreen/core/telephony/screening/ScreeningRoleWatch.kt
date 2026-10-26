package com.callscreen.core.telephony.screening

import android.content.Context

/** One remembered yes/no. */
public interface Flag {
    public fun get(): Boolean

    public fun set()
}

/** A [Flag] in app-private shared preferences (not backed up: the app sets allowBackup false). */
public class PreferencesFlag(context: Context, private val key: String) : Flag {
    private val prefs = context.applicationContext.getSharedPreferences(FILE, Context.MODE_PRIVATE)

    override fun get(): Boolean = prefs.getBoolean(key, false)

    override fun set() {
        prefs.edit().putBoolean(key, true).apply()
    }

    public companion object {
        private const val FILE = "callscreen_screening_role"
        public const val HELD_BEFORE: String = "held_before"
    }
}

/**
 * F14 · Losing the screening role (docs/ux/android/08-user-flows.md): Android
 * lets another app take `ROLE_CALL_SCREENING` at any time, and then the
 * subscriber's blocks silently stop working. Checked on each foreground.
 */
public class ScreeningRoleWatch(private val heldBefore: Flag) {
    /**
     * Records that this app holds the role, when [status] says so. It must be
     * called wherever the app can see the role held (each foreground, not only
     * the Calls feed): a role granted and lost before the feed was ever shown
     * would otherwise never count as held, and its loss would go unreported.
     */
    public fun observe(status: ScreeningRoleStatus) {
        if (status == ScreeningRoleStatus.HELD && !heldBefore.get()) heldBefore.set()
    }

    /**
     * Whether the role was lost: this app held it once and does not now. A
     * subscriber who never granted it has nothing to lose; that case is the
     * onboarding prompt's, not this banner's.
     */
    public fun lost(status: ScreeningRoleStatus): Boolean {
        observe(status)
        return status != ScreeningRoleStatus.HELD && heldBefore.get()
    }
}
