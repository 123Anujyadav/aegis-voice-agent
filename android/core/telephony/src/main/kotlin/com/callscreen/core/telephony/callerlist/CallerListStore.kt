package com.callscreen.core.telephony.callerlist

import com.callscreen.core.telephony.prefilter.CallerListLookup
import com.callscreen.core.telephony.prefilter.E164Number
import com.callscreen.core.telephony.prefilter.ListType
import com.callscreen.core.telephony.prefilter.PhoneNumbers
import java.io.File
import java.io.FileOutputStream
import java.io.IOException
import java.nio.file.Files
import java.nio.file.StandardCopyOption
import kotlinx.serialization.Serializable
import kotlinx.serialization.SerializationException
import kotlinx.serialization.json.Json

/** One record's raw bytes. [write] is atomic: a reader sees the old record or the new one, never a mix. */
public interface RecordFile {
    /** The record, or null if none was ever written. */
    public fun read(): ByteArray?

    public fun write(bytes: ByteArray)
}

/**
 * A [RecordFile] in app-private storage: written to a temporary file in the
 * same directory, then moved over the record atomically.
 */
public class AtomicRecordFile(private val file: File) : RecordFile {
    override fun read(): ByteArray? = if (file.exists()) file.readBytes() else null

    override fun write(bytes: ByteArray) {
        val dir = requireNotNull(file.absoluteFile.parentFile) { "record file has no directory" }
        dir.mkdirs()
        val tmp = File.createTempFile(file.name, ".tmp", dir)
        try {
            FileOutputStream(tmp).use {
                it.write(bytes)
                it.fd.sync() // on disk before it replaces the record
            }
            Files.move(tmp.toPath(), file.toPath(), StandardCopyOption.ATOMIC_MOVE, StandardCopyOption.REPLACE_EXISTING)
        } finally {
            tmp.delete()
        }
    }
}

/** The store's state as the interface should show it. */
public enum class StoreHealth {
    READY,

    /**
     * The stored list could not be read. It is left untouched: the pre-filter
     * matches nothing (every caller is screened, none rejected) and changes are
     * refused until the subscriber chooses [CallerListStore.discardUnreadable].
     */
    UNREADABLE,
}

/**
 * The subscriber's lists on the handset: the pre-filter's [CallerListLookup]
 * and the place blocks and allows are recorded.
 *
 * - **P-CO-2:** a change is written locally and takes effect for the next call
 *   at once, with no network. Syncing to the server is separate and later.
 * - A change is visible to the pre-filter only once it is saved. A failed
 *   save throws [IOException] and changes nothing.
 * - **Nothing is guessed.** A stored list that cannot be read is never
 *   overwritten or cleared without the subscriber's choice (a lost blocklist
 *   is silent harm); meanwhile it matches nothing, which screens callers
 *   rather than rejecting them.
 *
 * Changes and the first read do blocking file I/O: call them off the main
 * thread. Lookups after the first are in memory.
 */
public class CallerListStore(
    private val file: RecordFile,
    private val nowSeconds: () -> Long = { System.currentTimeMillis() / 1000 },
    private val ownNumber: () -> E164Number? = { null },
) : CallerListLookup {

    private val lock = Any()

    @Volatile
    private var list: CallerList? = null

    @Volatile
    private var unreadable = false

    public fun health(): StoreHealth {
        loaded()
        return if (unreadable) StoreHealth.UNREADABLE else StoreHealth.READY
    }

    override fun listOf(number: E164Number): ListType? = loaded()?.typeOf(number)

    public fun entries(type: ListType): List<CallerListEntry> =
        loaded()?.entries?.filter { it.type == type }?.sortedByDescending { it.addedAtEpochSeconds }.orEmpty()

    /** Blocks [rawNumber]; if it was allowed, it moves (P-CO-1). */
    public fun block(rawNumber: String, source: ListSource): ListChange = put(rawNumber, ListType.BLOCK, source)

    /** Allows [rawNumber]; if it was blocked, it moves (P-CO-1). */
    public fun allow(rawNumber: String, source: ListSource): ListChange = put(rawNumber, ListType.ALLOW, source)

    public fun remove(rawNumber: String): ListChange {
        val number = PhoneNumbers.normalise(rawNumber) ?: return ListChange.NotListed
        return change { it.remove(number) }
    }

    /**
     * Replaces an unreadable stored list with an empty one, on the
     * subscriber's explicit choice. Does nothing when the list is readable.
     */
    public fun discardUnreadable() {
        synchronized(lock) {
            if (loaded() != null) return
            file.write(encode(CallerList.EMPTY))
            list = CallerList.EMPTY
            unreadable = false
        }
    }

    private fun put(rawNumber: String, type: ListType, source: ListSource): ListChange {
        val number = PhoneNumbers.normalise(rawNumber) ?: return ListChange.Refused(ListRefusal.NOT_A_PHONE_NUMBER)
        return change { it.put(CallerListEntry(number, type, source, nowSeconds()), ownNumber()) }
    }

    private fun change(edit: (CallerList) -> Pair<CallerList, ListChange>): ListChange {
        synchronized(lock) {
            val current = loaded() ?: return ListChange.Refused(ListRefusal.STORE_UNREADABLE)
            val (next, result) = edit(current)
            if (next !== current) {
                file.write(encode(next))
                list = next
            }
            return result
        }
    }

    /** The list, reading it on first use; null while the stored list is unreadable. */
    private fun loaded(): CallerList? {
        list?.let { return it }
        if (unreadable) return null
        synchronized(lock) {
            list?.let { return it }
            if (unreadable) return null
            val read = try {
                file.read().let { bytes -> if (bytes == null) CallerList.EMPTY else decode(bytes) }
            } catch (e: IOException) {
                null
            }
            if (read == null) unreadable = true else list = read
            return read
        }
    }

    private companion object {
        const val FORMAT_VERSION = 1
        val json = Json { ignoreUnknownKeys = false }

        fun encode(list: CallerList): ByteArray {
            val stored = StoredList(
                FORMAT_VERSION,
                list.entries.map { StoredEntry(it.number.value, it.type, it.source, it.addedAtEpochSeconds) },
            )
            return json.encodeToString(StoredList.serializer(), stored).toByteArray(Charsets.UTF_8)
        }

        /** The list, or null if the bytes are not exactly a valid stored list. */
        fun decode(bytes: ByteArray): CallerList? {
            val stored = try {
                json.decodeFromString(StoredList.serializer(), bytes.toString(Charsets.UTF_8))
            } catch (e: SerializationException) {
                return null
            } catch (e: IllegalArgumentException) {
                return null
            }
            if (stored.version != FORMAT_VERSION) return null
            val entries = stored.entries.map { e ->
                val number = E164Number.of(e.number) ?: return null
                CallerListEntry(number, e.type, e.source, e.addedAt)
            }
            return CallerList.of(entries)
        }
    }
}

@Serializable
private data class StoredList(val version: Int, val entries: List<StoredEntry>)

@Serializable
private data class StoredEntry(
    val number: String,
    val type: ListType,
    val source: ListSource,
    val addedAt: Long,
)
