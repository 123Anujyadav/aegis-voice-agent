package com.callscreen.core.telephony.callerlist

import com.callscreen.core.telephony.prefilter.E164Number
import com.callscreen.core.telephony.prefilter.ListType
import com.callscreen.core.telephony.prefilter.PreFilter
import com.callscreen.core.telephony.prefilter.PreFilterDecision
import java.io.File
import java.io.IOException
import org.junit.jupiter.api.Assertions.assertArrayEquals
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertNull
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.assertThrows
import org.junit.jupiter.api.io.TempDir

@DisplayName("Caller list (blocklist and allowlist) on the handset")
class CallerListStoreTest {

    /** An in-memory record; [failWrites] makes the next writes fail as a full disk would. */
    private class MemoryRecord(var bytes: ByteArray? = null) : RecordFile {
        var failWrites = false
        var failReads = false
        var writes = 0

        override fun read(): ByteArray? {
            if (failReads) throw IOException("read failed")
            return bytes
        }

        override fun write(bytes: ByteArray) {
            if (failWrites) throw IOException("disk full")
            this.bytes = bytes.copyOf()
            writes++
        }
    }

    private val own = E164Number.of("+919876500009")
    private val record = MemoryRecord()
    private var now = 1_700_000_000L
    private val store = CallerListStore(record, { now }, { own })

    private fun e164(s: String) = requireNotNull(E164Number.of(s))

    @Test
    @DisplayName("a block applies to the next lookup at once, in any form the number is entered (P-CO-2)")
    fun blockAppliesImmediately() {
        val change = store.block("98765 00001", ListSource.MANUAL)
        assertEquals(ListChange.Added(CallerListEntry(e164("+919876500001"), ListType.BLOCK, ListSource.MANUAL, now)), change)
        assertEquals(ListType.BLOCK, store.listOf(e164("+919876500001")))
        val filter = PreFilter(store) { false }
        assertEquals(PreFilterDecision.REJECT, filter.decide("09876500001"))
        assertEquals(PreFilterDecision.SCREEN, filter.decide("09876500002"))
    }

    @Test
    @DisplayName("the lists survive a restart: a new store reads what the last one saved")
    fun persists() {
        store.block("+919876500001", ListSource.FROM_CALL)
        store.allow("+919876500002", ListSource.MANUAL)
        val reopened = CallerListStore(MemoryRecord(record.bytes))
        assertEquals(ListType.BLOCK, reopened.listOf(e164("+919876500001")))
        assertEquals(ListType.ALLOW, reopened.listOf(e164("+919876500002")))
        assertEquals(StoreHealth.READY, reopened.health())
        assertEquals(listOf(ListSource.FROM_CALL), reopened.entries(ListType.BLOCK).map { it.source })
    }

    @Test
    @DisplayName("moving a number between lists is one change, and it is stated (P-CO-1, INV-CO-1)")
    fun moveIsStated() {
        store.block("+919876500001", ListSource.MANUAL)
        now += 60
        val change = store.allow("+919876500001", ListSource.MANUAL)
        assertEquals(ListChange.Moved(CallerListEntry(e164("+919876500001"), ListType.ALLOW, ListSource.MANUAL, now), ListType.BLOCK), change)
        assertEquals(ListType.ALLOW, store.listOf(e164("+919876500001")))
        assertEquals(emptyList<CallerListEntry>(), store.entries(ListType.BLOCK))
        val writes = record.writes
        assertEquals(ListChange.AlreadyListed::class, store.allow("9876500001", ListSource.MANUAL)::class)
        assertEquals(writes, record.writes, "an unchanged list is not rewritten")
    }

    @Test
    @DisplayName("short codes and malformed input are refused; the subscriber's own number can be allowed but not blocked")
    fun refusals() {
        for (raw in listOf("112", "100", "", "not a number")) {
            assertEquals(ListChange.Refused(ListRefusal.NOT_A_PHONE_NUMBER), store.block(raw, ListSource.MANUAL), raw)
        }
        assertEquals(ListChange.Refused(ListRefusal.OWN_NUMBER), store.block("9876500009", ListSource.MANUAL))
        assertEquals(ListChange.Added::class, store.allow("9876500009", ListSource.MANUAL)::class)
        assertEquals(ListChange.NotListed, store.remove("+919876500001"))
        assertEquals(ListChange.NotListed, store.remove("112"))
    }

    @Test
    @DisplayName("remove takes a number off its list")
    fun remove() {
        store.block("+919876500001", ListSource.MANUAL)
        assertEquals(ListChange.Removed::class, store.remove("09876500001")::class)
        assertNull(store.listOf(e164("+919876500001")))
        assertNull(CallerListStore(MemoryRecord(record.bytes)).listOf(e164("+919876500001")))
    }

    @Test
    @DisplayName("restore puts a removed entry back exactly (undo), under the same rules")
    fun restore() {
        store.block("+919876500001", ListSource.FROM_CALL)
        val entry = store.entries(ListType.BLOCK).single()
        store.remove("+919876500001")
        now += 3_600
        assertEquals(ListChange.Added(entry), store.restore(entry))
        assertEquals(listOf(entry), CallerListStore(MemoryRecord(record.bytes)).entries(ListType.BLOCK))
        val ownBlock = CallerListEntry(requireNotNull(own), ListType.BLOCK, ListSource.MANUAL, now)
        assertEquals(ListChange.Refused(ListRefusal.OWN_NUMBER), store.restore(ownBlock))
    }

    @Test
    @DisplayName("a label is the subscriber's note: normalised, persisted, cleared when blank")
    fun labels() {
        store.block("+919876500001", ListSource.MANUAL)
        val change = store.setLabel("09876500001", "  Insurance   calls ")
        assertEquals("Insurance calls", (change as ListChange.Relabelled).entry.label)
        assertEquals("Insurance calls", CallerListStore(MemoryRecord(record.bytes)).entries(ListType.BLOCK).single().label)
        assertEquals(ListChange.Relabelled::class, store.setLabel("+919876500001", "   ")::class)
        assertNull(store.entries(ListType.BLOCK).single().label)
        assertEquals(ListChange.NotListed, store.setLabel("+919876500002", "Someone"))
    }

    @Test
    @DisplayName("labels are bounded and plain text; a number keeps its label when it moves lists")
    fun labelRulesAndMoves() {
        store.block("+919876500001", ListSource.MANUAL)
        val tooLong = "x".repeat(CallerLabel.MAX_LENGTH + 1)
        assertEquals(ListChange.Refused(ListRefusal.INVALID_LABEL), store.setLabel("+919876500001", tooLong))
        assertEquals(ListChange.Relabelled::class, store.setLabel("+919876500001", "x".repeat(CallerLabel.MAX_LENGTH))::class)
        assertEquals(ListChange.Refused(ListRefusal.INVALID_LABEL), store.setLabel("+919876500001", "bell\u0007"))
        store.setLabel("+919876500001", "Courier")
        val moved = store.allow("+919876500001", ListSource.MANUAL) as ListChange.Moved
        assertEquals("Courier", moved.entry.label)
        assertEquals(ListType.ALLOW, store.listOf(e164("+919876500001")))
    }

    @Test
    @DisplayName("a list saved before labels existed (format 1) is still read")
    fun readsFormatOne() {
        val v1 = "{\"version\":1,\"entries\":[{\"number\":\"+919876500001\",\"type\":\"BLOCK\",\"source\":\"MANUAL\",\"addedAt\":5}]}"
        val s = CallerListStore(MemoryRecord(v1.toByteArray()))
        assertEquals(StoreHealth.READY, s.health())
        assertEquals(CallerListEntry(e164("+919876500001"), ListType.BLOCK, ListSource.MANUAL, 5), s.entries(ListType.BLOCK).single())
    }

    @Test
    @DisplayName("a failed save changes nothing: the pre-filter never acts on an unsaved list")
    fun failedSaveChangesNothing() {
        store.block("+919876500001", ListSource.MANUAL)
        record.failWrites = true
        assertThrows<IOException> { store.block("+919876500002", ListSource.MANUAL) }
        assertThrows<IOException> { store.allow("+919876500001", ListSource.MANUAL) }
        assertNull(store.listOf(e164("+919876500002")))
        assertEquals(ListType.BLOCK, store.listOf(e164("+919876500001")))
    }

    @Test
    @DisplayName("an unreadable stored list is never overwritten: it matches nothing and changes are refused until discarded")
    fun unreadableIsKept() {
        val n = "\"number\":\"+919876500001\",\"type\":\"BLOCK\",\"source\":\"MANUAL\",\"addedAt\":1"
        val unreadable = listOf(
            "not json",
            "{\"version\":3,\"entries\":[]}",
            "{\"version\":1,\"entries\":[{\"number\":\"112\",\"type\":\"BLOCK\",\"source\":\"MANUAL\",\"addedAt\":1}]}",
            "{\"version\":1,\"entries\":[{$n},{$n}]}",
            "{\"version\":1,\"entries\":[{$n,\"extra\":true}]}",
            "{\"version\":2,\"entries\":[{$n,\"label\":\"tab\\there\"}]}",
            "{\"version\":1,\"entries\":[{\"number\":\"+919876500001\",\"type\":\"MUTE\",\"source\":\"MANUAL\",\"addedAt\":1}]}",
        )
        for (text in unreadable) {
            val bytes = text.toByteArray()
            val file = MemoryRecord(bytes)
            val s = CallerListStore(file)
            assertEquals(StoreHealth.UNREADABLE, s.health(), text)
            assertNull(s.listOf(e164("+919876500001")), text)
            assertEquals(PreFilterDecision.SCREEN, PreFilter(s) { false }.decide("+919876500001"), text)
            assertEquals(ListChange.Refused(ListRefusal.STORE_UNREADABLE), s.block("+919876500002", ListSource.MANUAL), text)
            assertArrayEquals(bytes, file.bytes, text)
            s.discardUnreadable()
            assertEquals(StoreHealth.READY, s.health(), text)
            assertEquals(ListChange.Added::class, s.block("+919876500002", ListSource.MANUAL)::class, text)
        }
        // A list that was readable is never discarded by that call.
        store.block("+919876500001", ListSource.MANUAL)
        store.discardUnreadable()
        assertEquals(ListType.BLOCK, store.listOf(e164("+919876500001")))
    }

    @Test
    @DisplayName("a read error is treated as unreadable, not as an empty list")
    fun readErrorIsUnreadable() {
        val file = MemoryRecord("{\"version\":1,\"entries\":[]}".toByteArray()).apply { failReads = true }
        val s = CallerListStore(file)
        assertEquals(StoreHealth.UNREADABLE, s.health())
        assertEquals(ListChange.Refused(ListRefusal.STORE_UNREADABLE), s.block("+919876500002", ListSource.MANUAL))
    }

    @Test
    @DisplayName("the list is bounded; at the bound a new number is refused but an existing one can still move")
    fun bounded() {
        val entries = (0 until CallerList.MAX_ENTRIES).map {
            CallerListEntry(e164("+9198" + it.toString().padStart(8, '0')), ListType.BLOCK, ListSource.IMPORTED, 1)
        }
        val full = requireNotNull(CallerList.of(entries))
        val extra = CallerListEntry(e164("+919876500001"), ListType.BLOCK, ListSource.MANUAL, 1)
        assertEquals(ListChange.Refused(ListRefusal.LIST_FULL), full.put(extra, null).second)
        assertEquals(ListChange.Moved::class, full.put(entries[0].copy(type = ListType.ALLOW), null).second::class)
        assertNull(CallerList.of(entries + extra))
        assertNull(CallerList.of(listOf(extra, extra)))
    }

    @Test
    @DisplayName("the file record replaces atomically and leaves no temporary files")
    fun atomicFile(@TempDir dir: File) {
        val file = AtomicRecordFile(File(dir, "lists/caller_list.json"))
        assertNull(file.read())
        file.write("one".toByteArray())
        file.write("two".toByteArray())
        assertEquals("two", file.read()?.toString(Charsets.UTF_8))
        assertEquals(listOf("caller_list.json"), File(dir, "lists").list()?.toList())
        // "two" is not a stored list, so a store on that file is unreadable.
        assertEquals(StoreHealth.UNREADABLE, CallerListStore(file).health())
        val listFile = AtomicRecordFile(File(dir, "caller_list.json"))
        assertEquals(ListChange.Added::class, CallerListStore(listFile).block("+919876500001", ListSource.MANUAL)::class)
        assertEquals(ListType.BLOCK, CallerListStore(listFile).listOf(e164("+919876500001")))
    }
}
