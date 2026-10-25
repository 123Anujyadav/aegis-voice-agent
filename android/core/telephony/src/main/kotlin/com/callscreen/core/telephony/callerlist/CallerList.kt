package com.callscreen.core.telephony.callerlist

import com.callscreen.core.telephony.prefilter.E164Number
import com.callscreen.core.telephony.prefilter.ListType

/** Where an entry came from (docs/domain/11-consumer.md §11.4, `ListSource`). */
public enum class ListSource { MANUAL, FROM_CALL, IMPORTED, SUGGESTED }

/** One number on the blocklist or the allowlist. */
public data class CallerListEntry(
    val number: E164Number,
    val type: ListType,
    val source: ListSource,
    val addedAtEpochSeconds: Long,
)

/** Why a change was refused. */
public enum class ListRefusal {
    /** Not a number the pre-filter can match. Short codes, emergency numbers included, are refused here. */
    NOT_A_PHONE_NUMBER,

    /** The subscriber's own number cannot be blocked (§11.4, validation rules). */
    OWN_NUMBER,

    /** The list is at [CallerList.MAX_ENTRIES]. */
    LIST_FULL,

    /** The stored list could not be read; see [StoreHealth.UNREADABLE]. */
    STORE_UNREADABLE,
}

/** What a change did. P-CO-1: a move between lists is stated, never silent. */
public sealed interface ListChange {
    public data class Added(val entry: CallerListEntry) : ListChange

    /** The number was on the other list and has been moved, in one change. */
    public data class Moved(val entry: CallerListEntry, val from: ListType) : ListChange

    /** Already on that list; nothing changed. */
    public data class AlreadyListed(val entry: CallerListEntry) : ListChange

    public data class Removed(val entry: CallerListEntry) : ListChange

    public data object NotListed : ListChange

    public data class Refused(val reason: ListRefusal) : ListChange
}

/**
 * The `CallerList` aggregate: the blocklist and the allowlist together, so
 * moving a number between them is one change (P-CO-1) and a number is on at
 * most one list (INV-CO-1). Immutable: every change returns a new list.
 */
public class CallerList private constructor(private val byNumber: Map<String, CallerListEntry>) {

    public val entries: Collection<CallerListEntry> get() = byNumber.values

    public fun typeOf(number: E164Number): ListType? = byNumber[number.value]?.type

    /**
     * Puts [entry] on its list. [ownNumber], when known, can be allowed but
     * not blocked.
     */
    public fun put(entry: CallerListEntry, ownNumber: E164Number?): Pair<CallerList, ListChange> {
        if (entry.type == ListType.BLOCK && entry.number == ownNumber) {
            return this to ListChange.Refused(ListRefusal.OWN_NUMBER)
        }
        val existing = byNumber[entry.number.value]
        return when {
            existing?.type == entry.type -> this to ListChange.AlreadyListed(existing)
            existing == null && byNumber.size >= MAX_ENTRIES -> this to ListChange.Refused(ListRefusal.LIST_FULL)
            else -> {
                val next = CallerList(byNumber + (entry.number.value to entry))
                next to if (existing == null) ListChange.Added(entry) else ListChange.Moved(entry, existing.type)
            }
        }
    }

    public fun remove(number: E164Number): Pair<CallerList, ListChange> {
        val existing = byNumber[number.value] ?: return this to ListChange.NotListed
        return CallerList(byNumber - number.value) to ListChange.Removed(existing)
    }

    public companion object {
        /**
         * An engineering bound, not a product rule: the whole list is read
         * before the first decision after the process starts, and Android
         * allows 5 seconds before the phone rings.
         */
        public const val MAX_ENTRIES: Int = 10_000

        public val EMPTY: CallerList = CallerList(emptyMap())

        /** A list from stored entries, or null if they break an invariant (a repeated number, or too many). */
        public fun of(entries: List<CallerListEntry>): CallerList? {
            val byNumber = entries.associateBy { it.number.value }
            return if (byNumber.size != entries.size || entries.size > MAX_ENTRIES) null else CallerList(byNumber)
        }
    }
}
