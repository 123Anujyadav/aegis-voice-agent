package com.callscreen.feature.onboarding

import android.telephony.TelephonyManager

/**
 * The SIM as the phone reports it: `TelephonyManager.getSimState()` and
 * `getSimOperatorName()`, neither of which needs a permission. Nothing is
 * assumed: no carrier name the phone did not give, and no claim about call
 * forwarding, which is only known once it is set up and checked (A08).
 */
public data class SimSummary(val ready: Boolean, val status: String, val carrierLine: String) {
    public companion object {
        public const val FORWARDING_UNKNOWN: String = "Whether your carrier supports call forwarding is checked when you set it up."
        public val Unknown: SimSummary = SimSummary(false, "Unknown", "Carrier: not available")

        public fun of(telephony: TelephonyManager?): SimSummary =
            if (telephony == null) Unknown else of(telephony.simState, telephony.simOperatorName)

        public fun of(simState: Int, operatorName: String?): SimSummary {
            val carrier = operatorName?.trim()?.takeIf { it.isNotEmpty() }?.let { "Carrier: $it" } ?: "Carrier: not available"
            return when (simState) {
                TelephonyManager.SIM_STATE_READY -> SimSummary(true, "Ready", carrier)
                TelephonyManager.SIM_STATE_ABSENT -> SimSummary(false, "No SIM", "No SIM card found in this phone.")
                else -> SimSummary(false, "Not ready", "Carrier: not available")
            }
        }
    }
}
