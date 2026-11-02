package com.callscreen.feature.onboarding

import android.content.res.Resources
import android.telephony.TelephonyManager
import androidx.annotation.StringRes

/**
 * The SIM as the phone reports it: `TelephonyManager.getSimState()` and
 * `getSimOperatorName()`, neither of which needs a permission. Nothing is
 * assumed: no carrier name the phone did not give, and no claim about call
 * forwarding, which is only known once it is set up and checked (A08).
 */
public data class SimSummary(
    val ready: Boolean,
    @StringRes val status: Int,
    /** The carrier name the phone reported, trimmed; null when it reported none. */
    val carrier: String?,
    val absent: Boolean = false,
) {
    /** The line under the status: the reported carrier, or why there is none. */
    public fun carrierLine(resources: Resources): String = when {
        absent -> resources.getString(R.string.onboarding_sim_absent)
        carrier != null -> resources.getString(R.string.onboarding_sim_carrier, carrier)
        else -> resources.getString(R.string.onboarding_sim_carrier_unavailable)
    }

    public companion object {
        @StringRes public val FORWARDING_UNKNOWN: Int = R.string.onboarding_sim_forwarding_unknown
        public val Unknown: SimSummary = SimSummary(false, R.string.onboarding_sim_status_unknown, null)

        public fun of(telephony: TelephonyManager?): SimSummary =
            if (telephony == null) Unknown else of(telephony.simState, telephony.simOperatorName)

        public fun of(simState: Int, operatorName: String?): SimSummary {
            val carrier = operatorName?.trim()?.takeIf { it.isNotEmpty() }
            return when (simState) {
                TelephonyManager.SIM_STATE_READY -> SimSummary(true, R.string.onboarding_sim_status_ready, carrier)
                TelephonyManager.SIM_STATE_ABSENT -> SimSummary(false, R.string.onboarding_sim_status_absent, null, absent = true)
                else -> SimSummary(false, R.string.onboarding_sim_status_not_ready, null)
            }
        }
    }
}
