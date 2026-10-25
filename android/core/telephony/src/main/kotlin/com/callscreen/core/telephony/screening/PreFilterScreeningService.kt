package com.callscreen.core.telephony.screening

import android.os.Build
import android.telecom.Call
import android.telecom.CallScreeningService
import com.callscreen.core.telephony.callerlist.CallerListStore
import com.callscreen.core.telephony.prefilter.ContactLookup
import com.callscreen.core.telephony.prefilter.PreFilter
import com.callscreen.core.telephony.prefilter.ScreeningResponse
import dagger.hilt.android.AndroidEntryPoint
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors
import javax.inject.Inject

/**
 * The on-device pre-filter as Android runs it (ADR-0002 Stage 1). The system
 * binds it for each incoming call before the phone rings, and only while this
 * app holds the call screening role.
 *
 * - The decision is made off the main thread: the first one after the process
 *   starts reads the caller list from storage.
 * - Android waits at most 5 seconds; after that it ignores the answer and the
 *   call rings, which is the safe direction.
 * - **Every incoming call is answered**, and a failure answers "allow": a
 *   fault here must never cost the subscriber a call.
 * - Outgoing calls are not screened (Android ignores a response to them).
 */
@AndroidEntryPoint
public class PreFilterScreeningService : CallScreeningService() {

    @Inject
    public lateinit var callerLists: CallerListStore

    private lateinit var executor: ExecutorService

    override fun onCreate() {
        super.onCreate()
        executor = Executors.newSingleThreadExecutor()
    }

    override fun onDestroy() {
        executor.shutdown()
        super.onDestroy()
    }

    override fun onScreenCall(callDetails: Call.Details) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.Q) {
            // Before Android 10 only the default dialer is bound; answer "allow" if ever bound.
            respondToCall(callDetails, ScreeningCall.ALLOW.toCallResponse())
            return
        }
        if (callDetails.callDirection != Call.Details.DIRECTION_INCOMING) return
        val rawNumber = callDetails.handle?.schemeSpecificPart
        val filter = PreFilter(callerLists, NO_CONTACTS)
        executor.execute {
            val response = runCatching { ScreeningCall.respond(rawNumber, filter) }.getOrDefault(ScreeningCall.ALLOW)
            respondToCall(callDetails, response.toCallResponse())
        }
    }

    private companion object {
        /**
         * Contacts are never read. Without contacts access Android rings them without binding us;
         * with it, a contact that is not blocked is allowed like any other caller.
         */
        val NO_CONTACTS = ContactLookup { false }

        fun ScreeningResponse.toCallResponse(): CallScreeningService.CallResponse =
            CallScreeningService.CallResponse.Builder()
                .setDisallowCall(disallowCall)
                .setRejectCall(rejectCall)
                .setSkipCallLog(false)
                .setSkipNotification(skipNotification)
                .apply { if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) setSilenceCall(silenceCall) }
                .build()
    }
}
