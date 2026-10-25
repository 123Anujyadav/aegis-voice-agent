package com.callscreen.feature.protection

import androidx.lifecycle.SavedStateHandle
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.callscreen.core.telephony.callerlist.CallerListEntry
import com.callscreen.core.telephony.callerlist.CallerListStore
import com.callscreen.core.telephony.callerlist.ListChange
import com.callscreen.core.telephony.callerlist.ListRefusal
import com.callscreen.core.telephony.callerlist.ListSource
import com.callscreen.core.telephony.callerlist.StoreHealth
import com.callscreen.core.telephony.prefilter.ListType
import dagger.hilt.android.lifecycle.HiltViewModel
import java.io.IOException
import javax.inject.Inject
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/** A message for the user about the last change. */
public enum class ListMessage {
    ADDED,

    /** P-CO-1: the number was on the other list and has moved; the screen says so. */
    MOVED,
    ALREADY_LISTED,
    REMOVED,
    NOT_A_PHONE_NUMBER,
    OWN_NUMBER,
    LIST_FULL,
    COULD_NOT_SAVE,

    /** The stored lists cannot be read; nothing can change until the subscriber discards them. */
    UNREADABLE,
}

/** A32 (blocklist) or A33 (allowlist). */
public data class CallerListState(
    val type: ListType,
    val loading: Boolean = true,
    val entries: List<CallerListEntry> = emptyList(),
    val unreadable: Boolean = false,
    val query: String = "",
    val message: ListMessage? = null,
    /** The entry just removed, while it can still be undone. */
    val undoable: CallerListEntry? = null,
) {
    /** The entries matching [query] (its digits), newest first. */
    val visible: List<CallerListEntry>
        get() {
            val digits = query.filter(Char::isDigit)
            return if (digits.isEmpty()) entries else entries.filter { digits in it.number.value }
        }
}

/**
 * One of the subscriber's lists. Changes go to the same [CallerListStore] the
 * screening service reads, so a block applies to the next call at once, offline
 * (P-CO-2). Each change is saved before the screen shows it: the store is
 * local, so there is nothing to be optimistic about.
 */
@HiltViewModel
public class CallerListViewModel internal constructor(
    private val store: CallerListStore,
    savedState: SavedStateHandle,
    private val io: CoroutineDispatcher,
) : ViewModel() {

    @Inject
    public constructor(store: CallerListStore, savedState: SavedStateHandle) : this(store, savedState, Dispatchers.IO)

    private val type: ListType = typeOf(savedState.get<String>(LIST_ARG))
    private val mutable = MutableStateFlow(CallerListState(type))
    public val state: StateFlow<CallerListState> = mutable.asStateFlow()
    private var undoWindow: Job? = null

    init {
        viewModelScope.launch { reload() }
    }

    public fun onQueryChange(query: String) {
        mutable.update { it.copy(query = query) }
    }

    /** A35: add a number to this list (moving it from the other list, if there). */
    public fun add(rawNumber: String) {
        save {
            when (type) {
                ListType.BLOCK -> store.block(rawNumber, ListSource.MANUAL)
                ListType.ALLOW -> store.allow(rawNumber, ListSource.MANUAL)
            }
        }
    }

    /** Removes [entry]; it can be undone for [UNDO_WINDOW_MILLIS]. */
    public fun remove(entry: CallerListEntry) {
        save(undoable = true) { store.remove(entry.number.value) }
    }

    /** Puts back the entry last removed, if still within its undo window. */
    public fun undo() {
        val entry = mutable.value.undoable ?: return
        undoWindow?.cancel()
        mutable.update { it.copy(undoable = null) }
        save { store.restore(entry) }
    }

    public fun onMessageShown() {
        mutable.update { it.copy(message = null) }
    }

    /** The subscriber chose to replace unreadable lists with empty ones. */
    public fun discardUnreadable() {
        viewModelScope.launch {
            try {
                withContext(io) { store.discardUnreadable() }
            } catch (e: IOException) {
                mutable.update { it.copy(message = ListMessage.COULD_NOT_SAVE) }
            }
            reload()
        }
    }

    private fun save(undoable: Boolean = false, change: () -> ListChange) {
        viewModelScope.launch {
            val result = try {
                withContext(io) { change() }
            } catch (e: IOException) {
                null
            }
            val message = if (result == null) ListMessage.COULD_NOT_SAVE else messageFor(result)
            // The message and its undo appear together, so the screen offers Undo with "Removed".
            val removed = (result as? ListChange.Removed)?.entry?.takeIf { undoable }
            mutable.update { it.copy(message = message, undoable = removed ?: it.undoable) }
            if (removed != null) startUndoWindow(removed)
            reload()
        }
    }

    private fun startUndoWindow(entry: CallerListEntry) {
        undoWindow?.cancel()
        undoWindow = viewModelScope.launch {
            delay(UNDO_WINDOW_MILLIS)
            mutable.update { if (it.undoable == entry) it.copy(undoable = null) else it }
        }
    }

    private suspend fun reload() {
        val (health, entries) = withContext(io) { store.health() to store.entries(type) }
        mutable.update {
            it.copy(loading = false, entries = entries, unreadable = health == StoreHealth.UNREADABLE)
        }
    }

    public companion object {
        /** The navigation argument: "block" or "allow". */
        public const val LIST_ARG: String = "list"
        public const val UNDO_WINDOW_MILLIS: Long = 5_000

        public fun argOf(type: ListType): String = if (type == ListType.BLOCK) "block" else "allow"

        internal fun typeOf(arg: String?): ListType = when (arg) {
            "allow" -> ListType.ALLOW
            "block" -> ListType.BLOCK
            else -> throw IllegalArgumentException("unknown list: $arg")
        }

        internal fun messageFor(change: ListChange): ListMessage = when (change) {
            is ListChange.Added -> ListMessage.ADDED
            is ListChange.Moved -> ListMessage.MOVED
            is ListChange.AlreadyListed -> ListMessage.ALREADY_LISTED
            is ListChange.Removed -> ListMessage.REMOVED
            ListChange.NotListed -> ListMessage.REMOVED
            is ListChange.Refused -> when (change.reason) {
                ListRefusal.NOT_A_PHONE_NUMBER -> ListMessage.NOT_A_PHONE_NUMBER
                ListRefusal.OWN_NUMBER -> ListMessage.OWN_NUMBER
                ListRefusal.LIST_FULL -> ListMessage.LIST_FULL
                ListRefusal.STORE_UNREADABLE -> ListMessage.UNREADABLE
            }
        }
    }
}
