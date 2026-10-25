package com.callscreen.feature.protection

import androidx.lifecycle.SavedStateHandle
import com.callscreen.core.telephony.callerlist.CallerListStore
import com.callscreen.core.telephony.callerlist.ListSource
import com.callscreen.core.telephony.callerlist.RecordFile
import com.callscreen.core.telephony.prefilter.E164Number
import com.callscreen.core.telephony.prefilter.ListType
import java.io.IOException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.TestScope
import kotlinx.coroutines.test.advanceTimeBy
import kotlinx.coroutines.test.advanceUntilIdle
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.test.setMain
import org.junit.jupiter.api.AfterEach
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertFalse
import org.junit.jupiter.api.Assertions.assertNull
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.BeforeEach
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test

@OptIn(ExperimentalCoroutinesApi::class)
@DisplayName("Blocklist and allowlist screens")
class CallerListViewModelTest {

    private val dispatcher = StandardTestDispatcher()

    private class MemoryRecord(var bytes: ByteArray? = null) : RecordFile {
        var failWrites = false

        override fun read(): ByteArray? = bytes

        override fun write(bytes: ByteArray) {
            if (failWrites) throw IOException("disk full")
            this.bytes = bytes.copyOf()
        }
    }

    private val record = MemoryRecord()
    private var now = 1_700_000_000L
    private val store = CallerListStore(record, { now }, { E164Number.of("+919876500009") })

    @BeforeEach
    fun setUp() = Dispatchers.setMain(dispatcher)

    @AfterEach
    fun tearDown() = Dispatchers.resetMain()

    private fun TestScope.screen(type: ListType, on: CallerListStore = store): CallerListViewModel {
        val vm = CallerListViewModel(on, SavedStateHandle(mapOf(CallerListViewModel.LIST_ARG to CallerListViewModel.argOf(type))), dispatcher)
        advanceUntilIdle()
        return vm
    }

    private fun e164(s: String) = requireNotNull(E164Number.of(s))

    @Test
    @DisplayName("a block added on the screen is in the store the screening service reads, at once")
    fun addBlocks() = runTest(dispatcher) {
        val vm = screen(ListType.BLOCK)
        assertFalse(vm.state.value.loading)
        assertTrue(vm.state.value.entries.isEmpty())
        vm.add("98765 00001")
        advanceUntilIdle()
        assertEquals(ListMessage.ADDED, vm.state.value.message)
        assertEquals(listOf("+919876500001"), vm.state.value.entries.map { it.number.value })
        assertEquals(ListType.BLOCK, store.listOf(e164("+919876500001")))
    }

    @Test
    @DisplayName("adding a number that is on the other list moves it, and says so (P-CO-1)")
    fun moveIsStated() = runTest(dispatcher) {
        store.allow("+919876500001", ListSource.MANUAL)
        val blocklist = screen(ListType.BLOCK)
        blocklist.add("+919876500001")
        advanceUntilIdle()
        assertEquals(ListMessage.MOVED, blocklist.state.value.message)
        assertEquals(ListType.BLOCK, store.listOf(e164("+919876500001")))
        assertTrue(screen(ListType.ALLOW).state.value.entries.isEmpty())
    }

    @Test
    @DisplayName("refusals reach the screen as messages, and change nothing")
    fun refusals() = runTest(dispatcher) {
        val vm = screen(ListType.BLOCK)
        for ((raw, message) in listOf("112" to ListMessage.NOT_A_PHONE_NUMBER, "9876500009" to ListMessage.OWN_NUMBER)) {
            vm.add(raw)
            advanceUntilIdle()
            assertEquals(message, vm.state.value.message, raw)
            vm.onMessageShown()
            assertNull(vm.state.value.message)
        }
        assertTrue(vm.state.value.entries.isEmpty())
        record.failWrites = true
        vm.add("+919876500001")
        advanceUntilIdle()
        assertEquals(ListMessage.COULD_NOT_SAVE, vm.state.value.message)
        assertTrue(vm.state.value.entries.isEmpty())
        assertNull(store.listOf(e164("+919876500001")))
    }

    @Test
    @DisplayName("a removal can be undone for 5 seconds, restoring the entry exactly")
    fun undoWithinWindow() = runTest(dispatcher) {
        store.block("+919876500001", ListSource.FROM_CALL)
        val original = store.entries(ListType.BLOCK).single()
        now += 3_600
        val vm = screen(ListType.BLOCK)
        vm.remove(original)
        runCurrent()
        assertEquals(ListMessage.REMOVED, vm.state.value.message)
        assertEquals(original, vm.state.value.undoable)
        assertNull(store.listOf(original.number))
        advanceTimeBy(CallerListViewModel.UNDO_WINDOW_MILLIS - 1)
        vm.undo()
        advanceUntilIdle()
        assertEquals(listOf(original), vm.state.value.entries, "same list, source and date")
        assertNull(vm.state.value.undoable)
    }

    @Test
    @DisplayName("after 5 seconds the removal is final")
    fun undoWindowCloses() = runTest(dispatcher) {
        store.block("+919876500001", ListSource.MANUAL)
        val vm = screen(ListType.BLOCK)
        vm.remove(vm.state.value.entries.single())
        runCurrent()
        advanceTimeBy(CallerListViewModel.UNDO_WINDOW_MILLIS + 1)
        assertNull(vm.state.value.undoable)
        vm.undo()
        advanceUntilIdle()
        assertTrue(vm.state.value.entries.isEmpty())
    }

    @Test
    @DisplayName("A32d: a label is saved, cleared, and refused when too long; the entry follows it")
    fun labels() = runTest(dispatcher) {
        store.block("+919876500001", ListSource.MANUAL)
        val vm = screen(ListType.BLOCK)
        val entry = vm.state.value.entries.single()
        vm.setLabel(entry, " Courier  ")
        advanceUntilIdle()
        assertEquals(ListMessage.LABEL_SAVED, vm.state.value.message)
        assertEquals("Courier", vm.state.value.entries.single().label)
        vm.setLabel(entry, "x".repeat(41))
        advanceUntilIdle()
        assertEquals(ListMessage.INVALID_LABEL, vm.state.value.message)
        assertEquals("Courier", vm.state.value.entries.single().label)
        vm.setLabel(entry, "")
        advanceUntilIdle()
        assertNull(vm.state.value.entries.single().label)
    }

    @Test
    @DisplayName("A32d: moving an entry to the other list keeps its label and source, and is stated")
    fun moveFromDetail() = runTest(dispatcher) {
        store.block("+919876500001", ListSource.FROM_CALL)
        store.setLabel("+919876500001", "Courier")
        val blocklist = screen(ListType.BLOCK)
        blocklist.moveToOtherList(blocklist.state.value.entries.single())
        advanceUntilIdle()
        assertEquals(ListMessage.MOVED, blocklist.state.value.message)
        assertTrue(blocklist.state.value.entries.isEmpty())
        val allowed = screen(ListType.ALLOW).state.value.entries.single()
        assertEquals(ListType.ALLOW to "Courier", allowed.type to allowed.label)
        assertEquals(ListSource.FROM_CALL, allowed.source)
        assertNull(store.listOf(e164("+919876500002")))
    }

    @Test
    @DisplayName("each screen shows only its own list, and search matches digits")
    fun listsAndSearch() = runTest(dispatcher) {
        store.block("+919876500001", ListSource.MANUAL)
        store.block("+919123400002", ListSource.MANUAL)
        store.allow("+919876500003", ListSource.MANUAL)
        val blocklist = screen(ListType.BLOCK)
        assertEquals(2, blocklist.state.value.entries.size)
        assertEquals(listOf("+919876500003"), screen(ListType.ALLOW).state.value.entries.map { it.number.value })
        blocklist.onQueryChange("98765")
        assertEquals(listOf("+919876500001"), blocklist.state.value.visible.map { it.number.value })
        blocklist.onQueryChange("")
        assertEquals(2, blocklist.state.value.visible.size)
    }

    @Test
    @DisplayName("unreadable lists are shown as such; nothing changes until the subscriber starts again")
    fun unreadable() = runTest(dispatcher) {
        val broken = MemoryRecord("not a list".toByteArray())
        val brokenStore = CallerListStore(broken)
        val vm = screen(ListType.BLOCK, brokenStore)
        assertTrue(vm.state.value.unreadable)
        vm.add("+919876500001")
        advanceUntilIdle()
        assertEquals(ListMessage.UNREADABLE, vm.state.value.message)
        assertEquals("not a list", broken.bytes?.toString(Charsets.UTF_8))
        vm.discardUnreadable()
        advanceUntilIdle()
        assertFalse(vm.state.value.unreadable)
        vm.add("+919876500001")
        advanceUntilIdle()
        assertEquals(ListMessage.ADDED, vm.state.value.message)
    }
}
