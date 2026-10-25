package com.callscreen.core.telephony.screening

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager

/**
 * Whether numbers on the blocklist are actually blocked on this phone, and if
 * not, what is missing. The blocklist screen says so: a list that does nothing
 * must not look as if it works.
 */
public enum class BlockingStatus {
    /** Android here has no call screening role (Android 9 and earlier): blocks cannot take effect. */
    UNSUPPORTED,

    /** The role exists but this app does not hold it (declined, or taken by another app). */
    ROLE_NOT_HELD,

    /**
     * Blocks take effect, except for numbers saved in contacts: without
     * contacts access, Android rings those without asking the app.
     */
    ACTIVE_EXCEPT_CONTACTS,

    /** Blocks take effect for every number. */
    ACTIVE,
    ;

    public companion object {
        public fun of(role: ScreeningRoleStatus, contactsAccess: Boolean): BlockingStatus = when (role) {
            ScreeningRoleStatus.UNAVAILABLE -> UNSUPPORTED
            ScreeningRoleStatus.NOT_HELD -> ROLE_NOT_HELD
            ScreeningRoleStatus.HELD -> if (contactsAccess) ACTIVE else ACTIVE_EXCEPT_CONTACTS
        }
    }
}

/**
 * Contacts access, requested only when the subscriber asks to block saved
 * contacts too. It is Android's switch for passing calls from contacts to the
 * screening service (`CallScreeningService.onScreenCall`). **The app never
 * reads the contacts provider and never uploads contacts** (docs/domain/11-consumer.md
 * §11.16, constraint 1); a test fails if production code queries it.
 */
public object ContactsAccess {
    public const val PERMISSION: String = Manifest.permission.READ_CONTACTS

    public fun granted(context: Context): Boolean =
        context.checkSelfPermission(PERMISSION) == PackageManager.PERMISSION_GRANTED
}
