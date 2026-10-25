package com.callscreen.core.telephony.screening

import android.app.role.RoleManager
import android.content.Context
import android.content.Intent
import android.os.Build

/** Whether this app can run the on-device pre-filter. */
public enum class ScreeningRoleStatus {
    /** The app holds the call screening role: [PreFilterScreeningService] is bound for incoming calls. */
    HELD,

    /** The role can be requested. */
    NOT_HELD,

    /** The role does not exist here (Android 9 and earlier, or a device without it). */
    UNAVAILABLE,
}

/**
 * The call screening role (`RoleManager.ROLE_CALL_SCREENING`, Android 10 and
 * later). Holding it is what lets [PreFilterScreeningService] run; another app
 * can take it at any time, so check [status] rather than remembering it.
 */
public object ScreeningRole {
    public fun status(context: Context): ScreeningRoleStatus {
        val roles = roleManager(context) ?: return ScreeningRoleStatus.UNAVAILABLE
        return when {
            !roles.isRoleAvailable(RoleManager.ROLE_CALL_SCREENING) -> ScreeningRoleStatus.UNAVAILABLE
            roles.isRoleHeld(RoleManager.ROLE_CALL_SCREENING) -> ScreeningRoleStatus.HELD
            else -> ScreeningRoleStatus.NOT_HELD
        }
    }

    /** The system's role request, or null where the role does not exist. Start it for a result. */
    public fun requestIntent(context: Context): Intent? =
        roleManager(context)?.createRequestRoleIntent(RoleManager.ROLE_CALL_SCREENING)

    private fun roleManager(context: Context): RoleManager? =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) context.getSystemService(RoleManager::class.java) else null
}
