package com.callscreen.feature.protection

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Close
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.ExtendedFloatingActionButton
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarDuration
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.SnackbarResult
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.onClick
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.hilt.navigation.compose.hiltViewModel
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.telephony.callerlist.CallerLabel
import com.callscreen.core.telephony.callerlist.CallerListEntry
import com.callscreen.core.telephony.callerlist.ListSource
import com.callscreen.core.telephony.prefilter.ListType
import com.callscreen.core.ui.components.AegisPrimaryButton
import com.callscreen.core.ui.components.AegisStatusChip
import com.callscreen.core.ui.components.AegisTopAppBar
import com.callscreen.core.ui.components.StatusChipVariant
import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.time.format.FormatStyle

/**
 * A32 · Blocklist and A33 · Allowlist, with A35 · Add number
 * (docs/ux/android/05-screens-home-history-search.md). The list is the one the
 * on-device pre-filter reads: a block applies to the next call, offline.
 */
@Composable
public fun CallerListRoute(modifier: Modifier = Modifier, viewModel: CallerListViewModel = hiltViewModel()) {
    val state by viewModel.state.collectAsStateWithLifecycle()
    CallerListScreen(
        state = state,
        onQueryChange = viewModel::onQueryChange,
        onAdd = viewModel::add,
        onRemove = viewModel::remove,
        onSetLabel = viewModel::setLabel,
        onMove = viewModel::moveToOtherList,
        onUndo = viewModel::undo,
        onMessageShown = viewModel::onMessageShown,
        onDiscardUnreadable = viewModel::discardUnreadable,
        modifier = modifier,
    )
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun CallerListScreen(
    state: CallerListState,
    onQueryChange: (String) -> Unit,
    onAdd: (String) -> Unit,
    onRemove: (CallerListEntry) -> Unit,
    onSetLabel: (CallerListEntry, String) -> Unit,
    onMove: (CallerListEntry) -> Unit,
    onUndo: () -> Unit,
    onMessageShown: () -> Unit,
    onDiscardUnreadable: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val copy = CallerListCopy.of(state.type)
    val snackbar = remember { SnackbarHostState() }
    var adding by remember { mutableStateOf(false) }
    // A32d: the open entry, by number, so it follows a relabel and closes when the entry leaves this list.
    var openNumber by remember { mutableStateOf<String?>(null) }
    val open = state.entries.firstOrNull { it.number.value == openNumber }

    LaunchedEffect(state.message) {
        val message = state.message ?: return@LaunchedEffect
        onMessageShown()
        val undo = message == ListMessage.REMOVED && state.undoable != null
        val result = snackbar.showSnackbar(
            message = copy.message(message),
            actionLabel = if (undo) CallerListCopy.UNDO else null,
            // An undo stays until its window closes (below); anything else is brief.
            duration = if (undo) SnackbarDuration.Indefinite else SnackbarDuration.Short,
        )
        if (result == SnackbarResult.ActionPerformed) onUndo()
    }
    LaunchedEffect(state.undoable) {
        if (state.undoable == null) snackbar.currentSnackbarData?.takeIf { it.visuals.actionLabel != null }?.dismiss()
    }

    Scaffold(
        topBar = { AegisTopAppBar(title = copy.title) },
        snackbarHost = { SnackbarHost(snackbar) },
        floatingActionButton = {
            if (!state.unreadable && !state.loading) {
                ExtendedFloatingActionButton(
                    onClick = { adding = true },
                    icon = { Icon(Icons.Default.Add, contentDescription = null) },
                    text = { Text(CallerListCopy.ADD_NUMBER) },
                )
            }
        },
        modifier = modifier,
    ) { padding ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .background(CallScreenTheme.colors.surface.background)
                .padding(padding)
                .padding(horizontal = 16.dp),
        ) {
            when {
                state.loading -> Unit
                state.unreadable -> Unreadable(onDiscardUnreadable)
                state.entries.isEmpty() -> Empty(copy)
                else -> Entries(state, onQueryChange, onOpen = { openNumber = it.number.value }, onRemove = onRemove)
            }
        }
    }

    if (adding) {
        ModalBottomSheet(onDismissRequest = { adding = false }) {
            AddNumber(copy, onSubmit = { raw -> adding = false; onAdd(raw) })
        }
    }
    if (open != null) {
        ModalBottomSheet(onDismissRequest = { openNumber = null }) {
            EntryDetail(
                entry = open,
                onSaveLabel = { onSetLabel(open, it) },
                onMove = {
                    openNumber = null
                    onMove(open)
                },
                // The sheet closes and the list's 5-second undo takes over.
                onRemove = {
                    openNumber = null
                    onRemove(open)
                },
            )
        }
    }
}

@Composable
private fun Entries(
    state: CallerListState,
    onQueryChange: (String) -> Unit,
    onOpen: (CallerListEntry) -> Unit,
    onRemove: (CallerListEntry) -> Unit,
) {
    OutlinedTextField(
        value = state.query,
        onValueChange = onQueryChange,
        label = { Text(CallerListCopy.SEARCH) },
        singleLine = true,
        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Phone),
        modifier = Modifier
            .fillMaxWidth()
            .padding(vertical = 12.dp),
    )
    LazyColumn(verticalArrangement = Arrangement.spacedBy(4.dp)) {
        items(state.visible, key = { it.number.value }) { entry -> EntryRow(entry, onOpen, onRemove) }
    }
}

@Composable
internal fun EntryRow(
    entry: CallerListEntry,
    onOpen: (CallerListEntry) -> Unit,
    onRemove: (CallerListEntry) -> Unit,
) {
    val spoken = CallerListCopy.spoken(entry.number.value)
    val detail = "${CallerListCopy.source(entry.source)} · ${CallerListCopy.date(entry.addedAtEpochSeconds)}"
    val description = listOfNotNull(entry.label, spoken, detail).joinToString(", ")
    Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth().padding(vertical = 8.dp)) {
        // One node for the row: label, the number digit by digit, its source and date (A32 accessibility).
        Column(
            modifier = Modifier
                .weight(1f)
                .clickable { onOpen(entry) }
                .clearAndSetSemantics {
                    contentDescription = description
                    onClick(label = CallerListCopy.OPEN_DETAILS) {
                        onOpen(entry)
                        true
                    }
                },
        ) {
            entry.label?.let {
                Text(
                    text = it,
                    style = CallScreenTheme.typography.titleMedium,
                    color = CallScreenTheme.colors.content.primary,
                )
            }
            Text(
                text = entry.number.value,
                style = with(CallScreenTheme.typography) { if (entry.label == null) titleMedium else bodyLarge },
                color = CallScreenTheme.colors.content.primary,
            )
            Text(
                text = detail,
                style = CallScreenTheme.typography.bodyMedium,
                color = CallScreenTheme.colors.content.secondary,
            )
        }
        // IconButton's touch target is 48 dp.
        IconButton(onClick = { onRemove(entry) }) {
            Icon(Icons.Default.Close, contentDescription = "${CallerListCopy.REMOVE} $spoken")
        }
    }
}

/**
 * A32d · the detail of one entry: only what is stored (its list, number,
 * label, source and date), what the list does to calls, and the actions.
 */
@Composable
internal fun EntryDetail(
    entry: CallerListEntry,
    onSaveLabel: (String) -> Unit,
    onMove: () -> Unit,
    onRemove: () -> Unit,
) {
    val copy = CallerListCopy.of(entry.type)
    var label by remember(entry.number, entry.label) { mutableStateOf(entry.label.orEmpty()) }
    // Scrolls: on a small screen the actions would otherwise be cut off below the fold.
    Column(
        verticalArrangement = Arrangement.spacedBy(12.dp),
        modifier = Modifier
            .verticalScroll(rememberScrollState())
            .padding(16.dp),
    ) {
        AegisStatusChip(
            text = copy.status,
            variant = if (entry.type == ListType.BLOCK) StatusChipVariant.Spam else StatusChipVariant.Verified,
        )
        Text(
            text = entry.number.value,
            style = CallScreenTheme.typography.headlineMedium,
            fontWeight = FontWeight.Bold,
            color = CallScreenTheme.colors.content.primary,
            modifier = Modifier.semantics {
                heading()
                contentDescription = CallerListCopy.spoken(entry.number.value)
            },
        )
        Text(
            text = "${CallerListCopy.source(entry.source)} · ${CallerListCopy.date(entry.addedAtEpochSeconds)}",
            style = CallScreenTheme.typography.bodyMedium,
            color = CallScreenTheme.colors.content.secondary,
        )
        for (line in copy.meaning) {
            Text(
                text = line,
                style = CallScreenTheme.typography.bodyLarge,
                color = CallScreenTheme.colors.content.primary,
            )
        }
        OutlinedTextField(
            value = label,
            onValueChange = { label = it.take(CallerLabel.MAX_LENGTH) },
            label = { Text(CallerListCopy.LABEL) },
            supportingText = { Text("${label.length}/${CallerLabel.MAX_LENGTH}") },
            singleLine = true,
            modifier = Modifier.fillMaxWidth(),
        )
        AegisPrimaryButton(
            text = CallerListCopy.SAVE_LABEL,
            onClick = { onSaveLabel(label) },
            enabled = CallerLabel.normalise(label) != entry.label,
        )
        TextButton(onClick = onMove, modifier = Modifier.fillMaxWidth().heightIn(min = 48.dp)) {
            Text(copy.moveAction, style = CallScreenTheme.typography.titleMedium)
        }
        TextButton(onClick = onRemove, modifier = Modifier.fillMaxWidth().heightIn(min = 48.dp)) {
            Text(CallerListCopy.REMOVE, style = CallScreenTheme.typography.titleMedium)
        }
    }
}

@Composable
private fun Empty(copy: CallerListCopy) {
    Column(verticalArrangement = Arrangement.spacedBy(8.dp), modifier = Modifier.padding(top = 32.dp)) {
        Text(
            text = copy.emptyTitle,
            style = CallScreenTheme.typography.titleMedium,
            fontWeight = FontWeight.Bold,
            color = CallScreenTheme.colors.content.primary,
        )
        for (line in copy.emptyLines) {
            Text(
                text = line,
                style = CallScreenTheme.typography.bodyLarge,
                color = CallScreenTheme.colors.content.secondary,
            )
        }
    }
}

@Composable
private fun Unreadable(onDiscard: () -> Unit) {
    Column(verticalArrangement = Arrangement.spacedBy(16.dp), modifier = Modifier.padding(top = 32.dp)) {
        Text(
            text = CallerListCopy.UNREADABLE,
            style = CallScreenTheme.typography.bodyLarge,
            color = CallScreenTheme.colors.content.primary,
        )
        AegisPrimaryButton(text = CallerListCopy.START_EMPTY, onClick = onDiscard)
    }
}

@Composable
private fun AddNumber(copy: CallerListCopy, onSubmit: (String) -> Unit) {
    var number by remember { mutableStateOf("") }
    Column(verticalArrangement = Arrangement.spacedBy(16.dp), modifier = Modifier.padding(16.dp)) {
        OutlinedTextField(
            value = number,
            onValueChange = { number = it },
            label = { Text(CallerListCopy.PHONE_NUMBER) },
            singleLine = true,
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Phone),
            modifier = Modifier.fillMaxWidth(),
        )
        AegisPrimaryButton(text = copy.addAction, onClick = { onSubmit(number) }, enabled = number.isNotBlank())
    }
}

/** The screens' words. Kept together so each list's wording is checked in one place. */
internal class CallerListCopy private constructor(private val type: ListType) {
    val title: String = if (type == ListType.BLOCK) "Blocked numbers" else "Allowed numbers"
    val addAction: String = if (type == ListType.BLOCK) "Block" else "Allow"

    /** A32d: which list, in words as well as colour. */
    val status: String = if (type == ListType.BLOCK) "Blocked" else "Allowed"
    val moveAction: String = if (type == ListType.BLOCK) "Move to allowed numbers" else "Move to blocked numbers"

    /**
     * What the list does to this number's calls, and no more. An allowed call
     * rings, but the carrier can still forward it to screening if it is not
     * answered: the phone cannot stop that (readiness §2a).
     */
    val meaning: List<String> = if (type == ListType.BLOCK) {
        listOf("While this app screens your calls, calls from this number are rejected on this phone.")
    } else {
        listOf(ALLOWED_NEVER_BLOCKED, FORWARDING_CAVEAT)
    }

    /** A32's first-run empty state, which states the offline guarantee once (A32 security). */
    val emptyTitle: String = if (type == ListType.BLOCK) "No blocked numbers" else "No allowed numbers"
    val emptyLines: List<String> = if (type == ListType.BLOCK) {
        listOf(
            "Numbers you block won't ring or be screened.",
            "Blocking works on this phone, even offline or if our service is down.",
        )
    } else {
        listOf("Numbers you allow are never blocked here, even if they're flagged as spam.", FORWARDING_CAVEAT)
    }

    fun message(message: ListMessage): String = when (message) {
        ListMessage.ADDED -> if (type == ListType.BLOCK) "Blocked." else "Allowed."
        ListMessage.MOVED ->
            if (type == ListType.BLOCK) "Moved from your allowed numbers to your blocked numbers."
            else "Moved from your blocked numbers to your allowed numbers."
        ListMessage.ALREADY_LISTED -> "That number is already on this list."
        ListMessage.REMOVED -> "Removed."
        ListMessage.NOT_A_PHONE_NUMBER -> "Enter a full phone number, with its area code."
        ListMessage.OWN_NUMBER -> "You can't block your own number."
        ListMessage.LIST_FULL -> "This list is full. Remove some numbers first."
        ListMessage.COULD_NOT_SAVE -> "Couldn't save. Nothing changed."
        ListMessage.UNREADABLE -> UNREADABLE
        ListMessage.LABEL_SAVED -> "Label saved."
        ListMessage.INVALID_LABEL -> "A label can be up to ${CallerLabel.MAX_LENGTH} characters of plain text."
    }

    companion object {
        const val UNDO = "Undo"
        const val ADD_NUMBER = "Add number"
        const val SEARCH = "Search numbers"
        const val PHONE_NUMBER = "Phone number"
        const val REMOVE = "Remove"
        const val OPEN_DETAILS = "Open details"
        const val LABEL = "Label (optional)"
        const val SAVE_LABEL = "Save label"
        const val ALLOWED_NEVER_BLOCKED =
            "Calls from this number are never blocked here, even if they're flagged as spam."
        const val FORWARDING_CAVEAT = "If you don't answer, your carrier may still forward the call to be screened."
        const val UNREADABLE =
            "Your blocked and allowed numbers can't be read on this phone, so no number is blocked. " +
                "Nothing has been deleted."
        const val START_EMPTY = "Start again with empty lists"

        fun of(type: ListType): CallerListCopy = CallerListCopy(type)

        fun source(source: ListSource): String = when (source) {
            ListSource.MANUAL -> "Added by you"
            ListSource.FROM_CALL -> "From a call"
            ListSource.IMPORTED -> "Imported"
            ListSource.SUGGESTED -> "Suggested"
        }

        fun date(epochSeconds: Long): String = Instant.ofEpochSecond(epochSeconds).atZone(ZoneId.systemDefault())
            .format(DateTimeFormatter.ofLocalizedDate(FormatStyle.MEDIUM))

        /** A number read digit by digit ("+ 9 1 9 8 …"), as A32's accessibility rules ask. */
        fun spoken(number: String): String = number.toList().joinToString(" ")
    }
}
