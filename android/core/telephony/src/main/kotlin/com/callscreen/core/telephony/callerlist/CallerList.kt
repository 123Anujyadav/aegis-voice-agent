package com.callscreen.core.telephony.callerlist

import com.callscreen.core.telephony.prefilter.E164Number
import com.callscreen.core.telephony.prefilter.ListType

/** Where an entry came from (docs/domain/11-consumer.md §11.4, `ListSource`). */
public enum class ListSource { MANUAL, FROM_CALL, IMPORTED, SUGGESTED }

/**
 * One number on the blocklist or the allowlist. [label] is the subscriber's
 * own note for it ("Insurance calls"), optional, kept on this phone only.
 */
public data class CallerListEntry(
    val number: E164Number,
    val type: ListType,
    val source: ListSource,
    val addedAtEpochSeconds: Long,
    val label: String? = null,
)

/** The rules for an entry's label. */
public object CallerLabel {
    public const val MAX_LENGTH: Int = 40
    private val control = Regex("\\p{Cntrl}")
    private val spaces = Regex("\\s+")

    /** [raw] trimmed, with runs of whitespace made single spaces; null when nothing is left. */
    public fun normalise(raw: String): String? = raw.replace(spaces, " ").trim().ifEmpty { null }

    /** Null (no label), or a normalised label of at most [MAX_LENGTH] characters with no control characters. */
    public fun isValid(label: String?): Boolean =
        label == null || (label.length <= MAX_LENGTH && !control.containsMatchIn(label) && normalise(label) == label)
}

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

    /** A label longer than [CallerLabel.MAX_LENGTH], or with control characters. */
    INVALID_LABEL,
}

/** What a change did. P-CO-1: a move between lists is stated, never silent. */
public sealed interface ListChange {
    public data class Added(val entry: CallerListEntry) : ListChange

    /** The number was on the other list and has been moved, in one change. */
    public data class Moved(val entry: CallerListEntry, val from: ListType) : ListChange

    /** Already on that list; nothing changed. */
    public data class AlreadyListed(val entry: CallerListEntry) : ListChange

    public data class Removed(val entry: CallerListEntry) : ListChange

    /** The entry's label was set or cleared (or was already that). */
    public data class Relabelled(val entry: CallerListEntry) : ListChange

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
     * not blocked. A number moving from the other list keeps its label unless
     * [entry] brings one.
     */
    public fun put(entry: CallerListEntry, ownNumber: E164Number?): Pair<CallerList, ListChange> {
        if (entry.type == ListType.BLOCK && entry.number == ownNumber) {
            return this to ListChange.Refused(ListRefusal.OWN_NUMBER)
        }
        if (!CallerLabel.isValid(entry.label)) return this to ListChange.Refused(ListRefusal.INVALID_LABEL)
        val existing = byNumber[entry.number.value]
        return when {
            existing?.type == entry.type -> this to ListChange.AlreadyListed(existing)
            existing == null && byNumber.size >= MAX_ENTRIES -> this to ListChange.Refused(ListRefusal.LIST_FULL)
            existing == null -> CallerList(byNumber + (entry.number.value to entry)) to ListChange.Added(entry)
            else -> {
                val moved = if (entry.label == null) entry.copy(label = existing.label) else entry
                CallerList(byNumber + (moved.number.value to moved)) to ListChange.Moved(moved, existing.type)
            }
        }
    }

    /** Sets [number]'s label to [label] (already normalised), or clears it with null. */
    public fun relabel(number: E164Number, label: String?): Pair<CallerList, ListChange> {
        val existing = byNumber[number.value] ?: return this to ListChange.NotListed
        if (!CallerLabel.isValid(label)) return this to ListChange.Refused(ListRefusal.INVALID_LABEL)
        if (existing.label == label) return this to ListChange.Relabelled(existing)
        val relabelled = existing.copy(label = label)
        return CallerList(byNumber + (number.value to relabelled)) to ListChange.Relabelled(relabelled)
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

        /** A list from stored entries, or null if they break an invariant (repeated number, too many, bad label). */
        public fun of(entries: List<CallerListEntry>): CallerList? {
            val byNumber = entries.associateBy { it.number.value }
            val valid = byNumber.size == entries.size && entries.size <= MAX_ENTRIES &&
                entries.all { CallerLabel.isValid(it.label) }
            return if (valid) CallerList(byNumber) else null
        }
    }
}
