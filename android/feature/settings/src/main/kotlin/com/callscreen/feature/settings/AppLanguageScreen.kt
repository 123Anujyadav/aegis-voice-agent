package com.callscreen.feature.settings

import android.app.Activity
import android.content.Context
import android.content.ContextWrapper
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.selection.selectableGroup
import androidx.compose.material3.RadioButton
import androidx.compose.material3.RadioButtonDefaults
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.text
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.intl.LocaleList
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.components.AegisScreenColumn
import com.callscreen.core.ui.components.AegisTopAppBar
import com.callscreen.core.ui.language.AppLanguage
import com.callscreen.core.ui.language.AppLanguageSetting
import java.util.Locale

/**
 * A58l App language: the language of the app's screens. Not the language the
 * assistant speaks on calls (A43), which this never changes.
 *
 * A choice applies at once. Android 13 and later re-create the screens
 * themselves; on Android 12 and earlier this re-creates the activity. Either
 * way it is a configuration change, so the session and the back stack stay.
 */
@Composable
public fun AppLanguageRoute() {
    val context = LocalContext.current
    var selected by remember { mutableStateOf(AppLanguageSetting.selected(context)) }
    AppLanguageScreen(
        selected = selected,
        onSelect = { language ->
            selected = language
            if (AppLanguageSetting.select(context, language)) context.findActivity()?.recreate()
        },
    )
}

/**
 * The phone's language, then the six. Each language is named in itself, so a
 * reader finds theirs whatever the screens are in, and in the screens'
 * language. One not yet translated says so: choosing it still shows English.
 */
@Composable
public fun AppLanguageScreen(
    selected: AppLanguage?,
    onSelect: (AppLanguage?) -> Unit,
    modifier: Modifier = Modifier,
) {
    // The other languages' names are in the language the screen's words are in: the
    // configured one once it is translated, English until then. Otherwise a choice
    // still awaiting translation would show names in it among English words.
    val reader = AppLanguage.of(LocalConfiguration.current.locales[0])?.takeIf { it.translated }?.locale ?: Locale.ENGLISH
    Scaffold(
        topBar = { AegisTopAppBar(title = stringResource(R.string.settings_language_title)) },
    ) { paddingValues ->
        AegisScreenColumn(
            modifier = modifier.padding(paddingValues),
            // The Scaffold has made room for the system bars (AegisScreenColumn).
            windowInsets = WindowInsets(0, 0, 0, 0),
            horizontalAlignment = Alignment.Start,
            verticalArrangement = Arrangement.spacedBy(4.dp),
            contentPadding = 16.dp,
        ) {
            Text(
                text = stringResource(R.string.settings_language_intro),
                style = CallScreenTheme.typography.bodyMedium,
                color = CallScreenTheme.colors.content.secondary,
                modifier = Modifier.padding(bottom = 8.dp),
            )
            Column(modifier = Modifier.selectableGroup()) {
                LanguageRow(
                    name = stringResource(R.string.settings_language_phone),
                    nameLanguage = null,
                    detail = stringResource(R.string.settings_language_phone_detail),
                    note = null,
                    selected = selected == null,
                    onClick = { onSelect(null) },
                )
                AppLanguage.entries.forEach { language ->
                    val nativeName = language.nativeName()
                    val readerName = language.displayName(reader)
                    LanguageRow(
                        name = nativeName,
                        nameLanguage = language,
                        // The name in the screens' language, unless it is the same word.
                        detail = readerName.takeIf { it != nativeName },
                        note = if (language.translated) null else stringResource(R.string.settings_language_not_translated),
                        selected = selected == language,
                        onClick = { onSelect(language) },
                    )
                }
            }
        }
    }
}

/**
 * Screen readers get [text] marked as being in [language], so "தமிழ்" is read
 * with a Tamil voice instead of being spelled out in the screens' language.
 * The text on screen stays plain: Text(AnnotatedString) would bring Compose's
 * whole rich-text layout into the release build (about 20 KB compressed,
 * caught by the APK size gate) for a mark only screen readers use.
 */
internal fun Modifier.readIn(language: AppLanguage, text: String): Modifier = clearAndSetSemantics {
    this.text = AnnotatedString(text, listOf(AnnotatedString.Range(SpanStyle(localeList = LocaleList(language.tag)), 0, text.length)))
}

@Composable
private fun LanguageRow(
    name: String,
    nameLanguage: AppLanguage?,
    detail: String?,
    note: String?,
    selected: Boolean,
    onClick: () -> Unit,
) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .heightIn(min = 56.dp)
            .selectable(selected = selected, onClick = onClick, role = Role.RadioButton)
            .padding(vertical = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        // The row is the control; the button only shows its state.
        RadioButton(
            selected = selected,
            onClick = null,
            colors = RadioButtonDefaults.colors(selectedColor = CallScreenTheme.colors.action.primary.fill),
        )
        Column(modifier = Modifier.weight(1f).padding(start = 16.dp)) {
            Text(
                text = name,
                modifier = if (nameLanguage == null) Modifier else Modifier.readIn(nameLanguage, name),
                style = CallScreenTheme.typography.titleSmall,
                fontWeight = FontWeight.Bold,
                color = CallScreenTheme.colors.content.primary,
            )
            if (detail != null) {
                Text(
                    text = detail,
                    style = CallScreenTheme.typography.bodyMedium,
                    color = CallScreenTheme.colors.content.secondary,
                )
            }
            if (note != null) {
                Text(
                    text = note,
                    style = CallScreenTheme.typography.bodySmall,
                    color = CallScreenTheme.colors.content.secondary,
                )
            }
        }
    }
}

private tailrec fun Context.findActivity(): Activity? = when (this) {
    is Activity -> this
    is ContextWrapper -> baseContext.findActivity()
    else -> null
}

@Preview
@Composable
private fun AppLanguageScreenPreview() {
    CallScreenTheme {
        AppLanguageScreen(selected = null, onSelect = {})
    }
}
