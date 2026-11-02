package com.callscreen.feature.protection

import androidx.annotation.StringRes
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
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
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
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
    val (notice, onNoticeAction) = rememberBlockingNotice()
    CallerListScreen(
        state = state,
        // Only the blocklist depends on the role and contacts access; an allowlist entry needs neither.
        notice = notice.takeIf { state.type == ListType.BLOCK },
        onNoticeAction = onNoticeAction,
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
    notice: BlockingNoticeState?,
    onNoticeAction: () -> Unit,
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
    // Resolved while composing, so the snackbar shows the current language's words.
    val messageText = state.message?.let { copy.message(it).text() }
    val undoLabel = stringResource(CallerListCopy.UNDO)
    val addNumber = stringResource(CallerListCopy.ADD_NUMBER)

    LaunchedEffect(state.message) {
        val message = state.message ?: return@LaunchedEffect
        val undo = message == ListMessage.REMOVED && state.undoable != null
        val result = snackbar.showSnackbar(
            message = messageText ?: return@LaunchedEffect,
            actionLabel = if (undo) undoLabel else null,
            // An undo stays until its window closes (below); anything else is brief.
            duration = if (undo) SnackbarDuration.Indefinite else SnackbarDuration.Short,
        )
        // Only once it has been shown: clearing it first changed this effect's key, which
        // cancelled the effect and dismissed the snackbar at once (no message, no Undo).
        // A newer message cancels this one instead, and is shown in its place.
        onMessageShown()
        if (result == SnackbarResult.ActionPerformed) onUndo()
    }
    LaunchedEffect(state.undoable) {
        if (state.undoable == null) snackbar.currentSnackbarData?.takeIf { it.visuals.actionLabel != null }?.dismiss()
    }

    Scaffold(
        topBar = { AegisTopAppBar(title = stringResource(copy.title)) },
        snackbarHost = { SnackbarHost(snackbar) },
        floatingActionButton = {
            if (!state.unreadable && !state.loading) {
                ExtendedFloatingActionButton(
                    onClick = { adding = true },
                    icon = { Icon(Icons.Default.Add, contentDescription = null) },
                    text = { Text(addNumber) },
                    // The extended FAB does not expose its text to accessibility (on a device the
                    // button was announced without a name), so it is labelled explicitly.
                    modifier = Modifier.semantics { contentDescription = addNumber },
                )
            }
        },
        modifier = modifier,
    ) { padding ->
        // One scrolling list for the whole body: the notice, the caveat and the search field
        // scroll with the entries, and the empty and unreadable states, the recovery button
        // among them, scroll too when they do not fit (ProtectionLargeTextTest).
        LazyColumn(
            modifier = Modifier
                .fillMaxSize()
                .background(CallScreenTheme.colors.surface.background)
                .padding(padding)
                .padding(horizontal = 16.dp),
        ) {
            if (notice != null && !state.loading && !state.unreadable) {
                item(key = "notice") { BlockingNotice(notice, onNoticeAction, modifier = Modifier.padding(top = 12.dp)) }
            }
            // An allowed call still rings unanswered into carrier forwarding (readiness §2a):
            // said whenever the allowlist is shown, not only while it is empty.
            if (state.type == ListType.ALLOW && !state.loading && !state.unreadable) {
                item(key = "caveat") {
                    Text(
                        text = stringResource(CallerListCopy.FORWARDING_CAVEAT),
                        style = CallScreenTheme.typography.bodyMedium,
                        color = CallScreenTheme.colors.content.secondary,
                        modifier = Modifier.padding(top = 12.dp),
                    )
                }
            }
            when {
                state.loading -> Unit
                state.unreadable -> item(key = "unreadable") { Unreadable(onDiscardUnreadable) }
                state.entries.isEmpty() -> item(key = "empty") { Empty(copy) }
                else -> {
                    item(key = "search") { SearchField(state.query, onQueryChange) }
                    itemsIndexed(state.visible, key = { _, entry -> entry.number.value }) { index, entry ->
                        // 4 dp between rows, as before.
                        Box(Modifier.padding(top = if (index == 0) 0.dp else 4.dp)) {
                            EntryRow(entry, onOpen = { openNumber = it.number.value }, onRemove = onRemove)
                        }
                    }
                }
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
private fun SearchField(query: String, onQueryChange: (String) -> Unit) {
    OutlinedTextField(
        value = query,
        onValueChange = onQueryChange,
        label = { Text(stringResource(CallerListCopy.SEARCH)) },
        singleLine = true,
        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Phone),
        modifier = Modifier
            .fillMaxWidth()
            .padding(vertical = 12.dp),
    )
}

@Composable
internal fun EntryRow(
    entry: CallerListEntry,
    onOpen: (CallerListEntry) -> Unit,
    onRemove: (CallerListEntry) -> Unit,
) {
    val spoken = CallerListCopy.spoken(entry.number.value)
    val detail = stringResource(
        CallerListCopy.ENTRY_DETAIL,
        stringResource(CallerListCopy.source(entry.source)),
        CallerListCopy.date(entry.addedAtEpochSeconds),
    )
    val openDetails = stringResource(CallerListCopy.OPEN_DETAILS)
    val description = listOfNotNull(entry.label, spoken, detail).joinToString(", ")
    Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth().padding(vertical = 8.dp)) {
        // One node for the row: label, the number digit by digit, its source and date (A32 accessibility).
        Column(
            modifier = Modifier
                .weight(1f)
                .clickable { onOpen(entry) }
                .clearAndSetSemantics {
                    contentDescription = description
                    onClick(label = openDetails) {
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
            Icon(Icons.Default.Close, contentDescription = stringResource(CallerListCopy.REMOVE_NUMBER, spoken))
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
            text = stringResource(copy.status),
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
            text = stringResource(
                CallerListCopy.ENTRY_DETAIL,
                stringResource(CallerListCopy.source(entry.source)),
                CallerListCopy.date(entry.addedAtEpochSeconds),
            ),
            style = CallScreenTheme.typography.bodyMedium,
            color = CallScreenTheme.colors.content.secondary,
        )
        for (line in copy.meaning) {
            Text(
                text = stringResource(line),
                style = CallScreenTheme.typography.bodyLarge,
                color = CallScreenTheme.colors.content.primary,
            )
        }
        OutlinedTextField(
            value = label,
            onValueChange = { label = it.take(CallerLabel.MAX_LENGTH) },
            label = { Text(stringResource(CallerListCopy.LABEL)) },
            supportingText = { Text("${label.length}/${CallerLabel.MAX_LENGTH}") },
            singleLine = true,
            modifier = Modifier.fillMaxWidth(),
        )
        AegisPrimaryButton(
            text = stringResource(CallerListCopy.SAVE_LABEL),
            onClick = { onSaveLabel(label) },
            enabled = CallerLabel.normalise(label) != entry.label,
        )
        TextButton(onClick = onMove, modifier = Modifier.fillMaxWidth().heightIn(min = 48.dp)) {
            Text(stringResource(copy.moveAction), style = CallScreenTheme.typography.titleMedium)
        }
        TextButton(onClick = onRemove, modifier = Modifier.fillMaxWidth().heightIn(min = 48.dp)) {
            Text(stringResource(CallerListCopy.REMOVE), style = CallScreenTheme.typography.titleMedium)
        }
    }
}

@Composable
private fun Empty(copy: CallerListCopy) {
    Column(verticalArrangement = Arrangement.spacedBy(8.dp), modifier = Modifier.padding(top = 32.dp)) {
        Text(
            text = stringResource(copy.emptyTitle),
            style = CallScreenTheme.typography.titleMedium,
            fontWeight = FontWeight.Bold,
            color = CallScreenTheme.colors.content.primary,
        )
        for (line in copy.emptyLines) {
            Text(
                text = stringResource(line),
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
            text = stringResource(CallerListCopy.UNREADABLE),
            style = CallScreenTheme.typography.bodyLarge,
            color = CallScreenTheme.colors.content.primary,
        )
        AegisPrimaryButton(text = stringResource(CallerListCopy.START_EMPTY), onClick = onDiscard)
    }
}

@Composable
private fun AddNumber(copy: CallerListCopy, onSubmit: (String) -> Unit) {
    var number by remember { mutableStateOf("") }
    Column(verticalArrangement = Arrangement.spacedBy(16.dp), modifier = Modifier.padding(16.dp)) {
        OutlinedTextField(
            value = number,
            onValueChange = { number = it },
            label = { Text(stringResource(CallerListCopy.PHONE_NUMBER)) },
            singleLine = true,
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Phone),
            modifier = Modifier.fillMaxWidth(),
        )
        AegisPrimaryButton(text = stringResource(copy.addAction), onClick = { onSubmit(number) }, enabled = number.isNotBlank())
    }
}

/**
 * Words resolved when the screen composes: a string resource, or, with a
 * [count], a plurals resource for that count, so each language can say
 * "1 character" and "40 characters" its own way.
 */
internal data class CopyText(val id: Int, val count: Int? = null)

@Composable
internal fun CopyText.text(): String = if (count != null) pluralStringResource(id, count, count) else stringResource(id)

/** The screens' words. Kept together so each list's wording is checked in one place. */
internal class CallerListCopy private constructor(private val type: ListType) {
    @StringRes val title: Int = if (type == ListType.BLOCK) R.string.protection_list_blocked_title else R.string.protection_list_allowed_title
    @StringRes val addAction: Int = if (type == ListType.BLOCK) R.string.protection_list_add_block else R.string.protection_list_add_allow

    /** A32d: which list, in words as well as colour. */
    @StringRes val status: Int = if (type == ListType.BLOCK) R.string.protection_list_status_blocked else R.string.protection_list_status_allowed
    @StringRes val moveAction: Int =
        if (type == ListType.BLOCK) R.string.protection_list_move_to_allowed else R.string.protection_list_move_to_blocked

    /**
     * What the list does to this number's calls, and no more. An allowed call
     * rings, but the carrier can still forward it to screening if it is not
     * answered: the phone cannot stop that (readiness §2a).
     */
    val meaning: List<Int> = if (type == ListType.BLOCK) {
        listOf(R.string.protection_list_meaning_blocked)
    } else {
        listOf(ALLOWED_NEVER_BLOCKED, FORWARDING_CAVEAT)
    }

    /** A32's first-run empty state, which states the offline guarantee once (A32 security). */
    @StringRes val emptyTitle: Int =
        if (type == ListType.BLOCK) R.string.protection_list_empty_blocked_title else R.string.protection_list_empty_allowed_title
    val emptyLines: List<Int> = if (type == ListType.BLOCK) {
        listOf(R.string.protection_list_empty_blocked_rings, R.string.protection_list_empty_blocked_offline)
    } else {
        listOf(R.string.protection_list_empty_allowed)
    }

    fun message(message: ListMessage): CopyText = when (message) {
        ListMessage.ADDED -> CopyText(if (type == ListType.BLOCK) R.string.protection_list_message_blocked else R.string.protection_list_message_allowed)
        ListMessage.MOVED_TO_BLOCKED -> CopyText(R.string.protection_list_message_moved_to_blocked)
        ListMessage.MOVED_TO_ALLOWED -> CopyText(R.string.protection_list_message_moved_to_allowed)
        ListMessage.ALREADY_LISTED -> CopyText(R.string.protection_list_message_already_listed)
        ListMessage.REMOVED -> CopyText(R.string.protection_list_message_removed)
        ListMessage.NOT_A_PHONE_NUMBER -> CopyText(R.string.protection_list_message_not_a_phone_number)
        ListMessage.OWN_NUMBER -> CopyText(R.string.protection_list_message_own_number)
        ListMessage.LIST_FULL -> CopyText(R.string.protection_list_message_list_full)
        ListMessage.COULD_NOT_SAVE -> CopyText(R.string.protection_list_message_could_not_save)
        ListMessage.UNREADABLE -> CopyText(UNREADABLE)
        ListMessage.LABEL_SAVED -> CopyText(R.string.protection_list_message_label_saved)
        ListMessage.INVALID_LABEL -> CopyText(R.plurals.protection_list_message_invalid_label, CallerLabel.MAX_LENGTH)
    }

    companion object {
        @StringRes val UNDO: Int = R.string.protection_list_undo
        @StringRes val ADD_NUMBER: Int = R.string.protection_list_add_number
        @StringRes val SEARCH: Int = R.string.protection_list_search
        @StringRes val PHONE_NUMBER: Int = R.string.protection_list_phone_number
        @StringRes val REMOVE: Int = R.string.protection_list_remove

        /** "Remove %1$s", with the number read digit by digit. */
        @StringRes val REMOVE_NUMBER: Int = R.string.protection_list_remove_number
        @StringRes val OPEN_DETAILS: Int = R.string.protection_list_open_details
        @StringRes val LABEL: Int = R.string.protection_list_label
        @StringRes val SAVE_LABEL: Int = R.string.protection_list_save_label
        @StringRes val ALLOWED_NEVER_BLOCKED: Int = R.string.protection_list_allowed_never_blocked
        @StringRes val FORWARDING_CAVEAT: Int = R.string.protection_list_forwarding_caveat
        @StringRes val UNREADABLE: Int = R.string.protection_list_unreadable
        @StringRes val START_EMPTY: Int = R.string.protection_list_start_empty

        /** "%1$s · %2$s": where the number came from, then when. */
        @StringRes val ENTRY_DETAIL: Int = R.string.protection_list_entry_detail

        fun of(type: ListType): CallerListCopy = CallerListCopy(type)

        @StringRes
        fun source(source: ListSource): Int = when (source) {
            ListSource.MANUAL -> R.string.protection_list_source_manual
            ListSource.FROM_CALL -> R.string.protection_list_source_from_call
            ListSource.IMPORTED -> R.string.protection_list_source_imported
            ListSource.SUGGESTED -> R.string.protection_list_source_suggested
        }

        fun date(epochSeconds: Long): String = Instant.ofEpochSecond(epochSeconds).atZone(ZoneId.systemDefault())
            .format(DateTimeFormatter.ofLocalizedDate(FormatStyle.MEDIUM))

        /** A number read digit by digit ("+ 9 1 9 8 …"), as A32's accessibility rules ask. */
        fun spoken(number: String): String = number.toList().joinToString(" ")
    }
}
