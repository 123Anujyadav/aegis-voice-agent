package com.callscreen.core.telephony.screening

import com.callscreen.core.telephony.prefilter.PreFilter
import com.callscreen.core.telephony.prefilter.PreFilterDecision
import com.callscreen.core.telephony.prefilter.ScreeningResponse

/** The platform-free part of answering one incoming call, so it is tested on the JVM. */
public object ScreeningCall {
    /** Let the call ring, unchanged. */
    public val ALLOW: ScreeningResponse = ScreeningResponse.of(PreFilterDecision.RING_THROUGH)

    /**
     * The response for an incoming call from [rawNumber]: the `tel:` handle's
     * number as Android presents it, or null when there is none.
     */
    public fun respond(rawNumber: String?, filter: PreFilter): ScreeningResponse =
        ScreeningResponse.of(filter.decide(rawNumber))
}
