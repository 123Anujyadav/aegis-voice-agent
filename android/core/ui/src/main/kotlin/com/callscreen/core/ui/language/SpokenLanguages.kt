package com.callscreen.core.ui.language

import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.text
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.intl.LocaleList

/**
 * Screen readers get [text] with each of [parts] marked as being in its
 * language, a BCP 47 tag, so "भाषा · Language" is read with a Hindi voice, then
 * an English one, where the phone has them. Unmarked text is read in the
 * screens' language. The text on screen stays plain: Text(AnnotatedString)
 * would bring Compose's rich-text layout into the release build for a mark
 * only screen readers use (the APK size gate).
 */
public fun Modifier.readInLanguages(text: String, parts: List<Pair<String, IntRange>>): Modifier = clearAndSetSemantics {
    this.text = AnnotatedString(
        text,
        parts.map { (tag, range) -> AnnotatedString.Range(SpanStyle(localeList = LocaleList(tag)), range.first, range.last + 1) },
    )
}
