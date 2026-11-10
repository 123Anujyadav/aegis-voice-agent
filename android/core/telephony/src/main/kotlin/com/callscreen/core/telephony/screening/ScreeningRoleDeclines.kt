package com.callscreen.core.telephony.screening

import android.content.Context

/**
 * A09b's edge cases (docs/ux/android/06-screens-onboarding-auth-permissions.md):
 * "previously denied → rationale shows once more with the loss stated in past
 * tense · permanently denied → the screen is not shown". Android has no API
 * that says a role request will be refused without a dialog, so "permanently"
 * is read as "declined a second time": the rationale is offered at most twice.
 * Protection's "Turn on blocking" notice stays the way back after that.
 *
 * A decline is leaving the rationale without the role: "Not now", or the
 * system request answered without granting it. Two remembered yes/no flags,
 * never anything else (ANDROID-DATA-INVENTORY.md D10c). Decided under the
 * owner's delegation of routine UX choices, 2026-11-10 (decision packet §3c,
 * option A).
 */
public class ScreeningRoleDeclines(private val once: Flag, private val twice: Flag) {

    /** Whether onboarding should show the rationale at all: not after a second decline. */
    public fun shouldOffer(): Boolean = !twice.get()

    /** Whether the rationale is being shown again after a decline (its past-tense variant). */
    public fun previouslyDeclined(): Boolean = once.get()

    /**
     * Records how the rationale was left: a decline when [status] is not
     * HELD. Granting records nothing.
     */
    public fun recordOutcome(status: ScreeningRoleStatus) {
        if (status == ScreeningRoleStatus.HELD) return
        if (once.get()) twice.set() else once.set()
    }

    public companion object {
        public const val DECLINED_ONCE: String = "role_declined_once"
        public const val DECLINED_TWICE: String = "role_declined_twice"

        /** The flags in the screening-role preferences file, beside [PreferencesFlag.HELD_BEFORE]. */
        public fun on(context: Context): ScreeningRoleDeclines =
            ScreeningRoleDeclines(PreferencesFlag(context, DECLINED_ONCE), PreferencesFlag(context, DECLINED_TWICE))
    }
}
