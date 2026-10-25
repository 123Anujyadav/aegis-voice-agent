package com.callscreen.core.telephony.prefilter

/**
 * The on-device pre-filter's decision (ADR-0002 §5, Stage 1): the shared
 * kernel's `PreFilterDecision` (docs/domain/11-consumer.md §11.4).
 */
public enum class PreFilterDecision {
    /** A known-good caller: the call rings normally and is not screened. */
    RING_THROUGH,

    /** A blocked caller: the call is ended on the handset, never forwarded. */
    REJECT,

    /**
     * The call rings without sound. Not produced by [PreFilter] yet: it is
     * reserved for a reputation verdict, whose mapping is the owner's to set.
     * A silenced call is still unanswered, so the carrier's forward-on-no-reply
     * sends it to screening; that is why a block is [REJECT], not this.
     */
    SILENCE,

    /** An unknown caller: the call rings, and if unanswered the carrier forwards it to be screened (Stage 2). */
    SCREEN,
}

/** The subscriber's own lists (the `CallerList` aggregate). A number is in at most one (INV-CO-1). */
public enum class ListType { BLOCK, ALLOW }

/** Which list, if any, holds [number]. Local and fast: it runs before the phone rings. */
public fun interface CallerListLookup {
    public fun listOf(number: E164Number): ListType?
}

/**
 * Whether [number] is one of the subscriber's contacts, read on the device
 * and never uploaded (docs/domain/11-consumer.md §11.16, constraint 1).
 *
 * Android passes a call from a contact to the screening service only if the
 * app holds READ_CONTACTS (`CallScreeningService.onScreenCall`), and the
 * platform lets such a call ring. Without that permission this lookup is
 * `{ false }` and contacts still ring through.
 */
public fun interface ContactLookup {
    public fun isContact(number: E164Number): Boolean
}

/**
 * The pre-filter: decides an incoming call from its number alone, on the
 * handset, with no network call (`EvaluatePreFilter`, §11.10). In order:
 *
 * 1. A number that cannot be normalised is [PreFilterDecision.SCREEN].
 * 2. The allowlist rings through (P-CO-3: the subscriber's instruction wins).
 * 3. The blocklist rejects, even for a contact: an explicit block is the
 *    subscriber's instruction, and blocks are enforced on the device
 *    (§11.16, constraint 5).
 * 4. A contact rings through.
 * 5. Anything else is screened.
 *
 * **Uncertainty never rejects.** A lookup that fails counts as "not on the
 * list" or "not a contact", so the call is screened: that costs a screening,
 * never a call the subscriber wanted (§11.15). There is no reputation input
 * yet: no reputation feed reaches the device, and the spec's degradation for
 * a missing reputation is [PreFilterDecision.SCREEN], which step 5 gives.
 */
public class PreFilter(
    private val lists: CallerListLookup,
    private val contacts: ContactLookup,
) {
    public fun decide(rawNumber: String?): PreFilterDecision {
        val number = rawNumber?.let(PhoneNumbers::normalise) ?: return PreFilterDecision.SCREEN
        when (orNull { lists.listOf(number) }) {
            ListType.ALLOW -> return PreFilterDecision.RING_THROUGH
            ListType.BLOCK -> return PreFilterDecision.REJECT
            null -> Unit
        }
        val isContact = orNull { contacts.isContact(number) } == true
        return if (isContact) PreFilterDecision.RING_THROUGH else PreFilterDecision.SCREEN
    }

    @Suppress("TooGenericExceptionCaught") // Any lookup failure must degrade to screening, never crash the call path.
    private inline fun <T> orNull(lookup: () -> T): T? = try {
        lookup()
    } catch (e: Exception) {
        null
    }
}

/**
 * How a decision is answered to Android (`CallScreeningService.CallResponse`).
 * A plain value, so the mapping is tested without the platform.
 *
 * - [disallowCall]: the call is blocked.
 * - [rejectCall]: a blocked call is ended "as if the user had manually
 *   rejected it"; the network then treats it as busy, and only no-reply
 *   forwarding is provisioned (ADR-0002 §5), so it is not forwarded.
 * - [silenceCall]: the call is not blocked but rings without sound.
 * - [skipNotification]: no missed-call notification for a blocked call, as
 *   for a number blocked in the phone's own settings. Android still records it
 *   in the call log as blocked; skipping the call log is reserved to system
 *   and carrier apps, so it is not offered here.
 */
public data class ScreeningResponse(
    val disallowCall: Boolean,
    val rejectCall: Boolean,
    val silenceCall: Boolean,
    val skipNotification: Boolean,
) {
    public companion object {
        private val ALLOW = ScreeningResponse(
            disallowCall = false,
            rejectCall = false,
            silenceCall = false,
            skipNotification = false,
        )
        private val BLOCK = ScreeningResponse(
            disallowCall = true,
            rejectCall = true,
            silenceCall = false,
            skipNotification = true,
        )

        public fun of(decision: PreFilterDecision): ScreeningResponse = when (decision) {
            PreFilterDecision.RING_THROUGH, PreFilterDecision.SCREEN -> ALLOW
            PreFilterDecision.REJECT -> BLOCK
            PreFilterDecision.SILENCE -> ALLOW.copy(silenceCall = true)
        }
    }
}
