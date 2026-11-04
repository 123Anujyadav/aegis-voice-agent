#!/usr/bin/env python3
"""Translation worksheets for the Android app's words.

The English source is every module's ``android/**/src/main/res/values/strings.xml``.
A translation is ``values-<code>/strings.xml`` beside it, for the release
languages in ``android/app/release-locales.txt``. This tool moves words between
those files and a worksheet (CSV, UTF-8) that a human translator fills in. It
uses only the standard library and the files in this repository: it calls no
network service, and nothing here may be pointed at one (machine translation
needs the owner's approval first; docs/phase19/LOCALISATION.md §6).

    status                    how much of each language is translated, per module
    audit                     the English source: counts, words, placeholders, plurals
    export --lang hi OUT.csv  a worksheet: every translatable string and plural form,
                              its English, where it appears and how much room it has,
                              the note above it, its placeholders, and any
                              translation already in the app
    export --all DIR          the worksheets of all five languages (DIR/hi.csv, ...)
    import --lang hi --translated-by "..." --reviewed-by "..." IN.csv
                              writes the worksheet's translations into each module's
                              values-hi/strings.xml, after checking them

Import refuses the whole worksheet, and writes nothing, if any row:
- is missing, empty, duplicated, or names a string English does not have;
- was exported from different English (the source changed: export again);
- translates a string marked translatable="false";
- changes a placeholder, or adds a stray "%" to a formatted string;
- gives a plural without every form the language uses;
- is identical to the English, unless its keep_english column says "yes";
- has no letter of the language's script, or a letter of another Indian script;
- changes the number of line breaks, starts or ends with spaces, or holds an
  invisible control character.
It also refuses a translator and reviewer who are the same person. The rules
the app depends on are enforced in the build too, by TranslationConsistencyTest;
this tool reports them to the translator first.
"""

from __future__ import annotations

import argparse
import csv
import re
import sys
import unicodedata
import xml.etree.ElementTree as ET
from dataclasses import dataclass, field
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
ANDROID = REPO / "android"
CONTEXT = Path(__file__).resolve().parent / "ui-context.csv"

# CLDR plural categories for cardinal numbers. Each release language uses
# "one" and "other" (https://www.unicode.org/cldr/charts/latest/supplemental/language_plural_rules.html),
# but "one" does not cover the same numbers in each.
PLURAL_CATEGORIES = {code: ["one", "other"] for code in ("en", "hi", "ta", "te", "ml", "kn")}
PLURAL_RULES = {
    "hi": "one = 0 or 1; other = every other number",
    "kn": "one = 0 or 1; other = every other number",
    "ta": "one = exactly 1; other = every other number, 0 included",
    "te": "one = exactly 1; other = every other number, 0 included",
    "ml": "one = exactly 1; other = every other number, 0 included",
}

# Each language's own script, as Unicode code point ranges.
SCRIPTS = {
    "hi": ("Devanagari", [(0x0900, 0x097F), (0xA8E0, 0xA8FF)]),
    "ta": ("Tamil", [(0x0B80, 0x0BFF)]),
    "te": ("Telugu", [(0x0C00, 0x0C7F)]),
    "kn": ("Kannada", [(0x0C80, 0x0CFF)]),
    "ml": ("Malayalam", [(0x0D00, 0x0D7F)]),
}
INDIAN_SCRIPTS = (0x0900, 0x0DFF)  # Devanagari to Sinhala: every Indian script block

# Where a string appears, from tools/i18n/ui-context.csv, and how much room it has there.
WHERE = {
    "top-bar": "Screen title in the top bar. Short: one line is best.",
    "tab": "Bottom tab label under an icon. Each tab has a quarter of the screen's width "
           "(about 70 dp on a small phone): one short word.",
    "chip": "Short label in a small rounded chip (a filter or a status). One line: a few words at most.",
    "field": "Label inside a one-line text box. It does not wrap: a long label is cut off. Keep it short.",
    "button": "Button or link. Short: wraps to a second line only if it must.",
    "heading": "Heading above other text. Wraps if it must.",
    "option": "Name or detail of a choice in a list. Wraps if it must.",
    "message": "Message shown after an action or on an error. Wraps freely.",
    "text": "Sentence or paragraph. Wraps freely.",
    "spoken": "Not shown on screen: read aloud by a screen reader (TalkBack) for an icon or control. "
              "Name the thing or action plainly.",
    "format": "A pattern that joins other text. Keep the placeholders and the punctuation; "
              "reorder only if the language needs it.",
}

# Strings whose English a spec quotes or a test holds to what the app does: a
# second reviewer checks the translation keeps exactly that meaning, against the source.
SPEC_LINKED = {
    "onboarding_role_": "docs/ux/android/06-screens-onboarding-auth-permissions.md (A09b), ScreeningRoleCopyTest",
    "app_signed_out_": "docs/ux/android/06-screens-onboarding-auth-permissions.md (A73), SignedOutCopyTest",
    "calls_role_": "docs/ux/android/08-user-flows.md (F14, A75), ScreeningRoleBannerCopyTest",
    "settings_deletion_": "docs/phase19/ACCOUNT-DELETION.md, AccountDeletionCopyTest",
    "protection_notice_contacts": "docs/phase19/ANDROID-DATA-INVENTORY.md: contacts are never read or uploaded",
    "settings_task_contacts_done": "docs/phase19/ANDROID-DATA-INVENTORY.md: contacts are never read or uploaded",
    "assistant_no_microphone": "HonestAssistantUiTest: the app never uses the microphone",
}

PLACEHOLDER = re.compile(r"%(\d+\$)?[-#+ 0,(]*\d*(\.\d+)?[sdfxXoc]")

COLUMNS = ["module", "kind", "name", "quantity", "english", "where", "english_chars", "note", "placeholders",
           "plural_rule", "review", "translation", "keep_english", "translator_note"]

# Invisible characters a translation must not carry. Zero-width joiner and
# non-joiner (U+200C, U+200D) are allowed: Indian scripts use them to choose a form.
FORBIDDEN = {chr(c) for c in range(0x20) if c != 0x0A} | {"\u200b", "\u2060", "\ufeff"} | \
    {chr(c) for c in range(0x202A, 0x202F)} | {chr(c) for c in range(0x2066, 0x206A)}


def placeholders(text: str) -> list[str]:
    """%1$s, %2$d, ...: what the code fills in. "%%" is a literal percent sign."""
    return sorted(m.group(0) for m in PLACEHOLDER.finditer(text.replace("%%", "")))


def stray_percent(text: str) -> bool:
    """A "%" that is neither a placeholder nor "%%": formatting it would fail."""
    return "%" in PLACEHOLDER.sub("", text.replace("%%", ""))


def release_languages(android: Path = ANDROID) -> list[str]:
    text = (android / "app" / "release-locales.txt").read_text(encoding="utf-8")
    return [line.split("#")[0].strip() for line in text.splitlines() if line.split("#")[0].strip()]


def translation_languages(android: Path = ANDROID) -> list[str]:
    return [code for code in release_languages(android) if code != "en"]


def unescape(text: str) -> str:
    """Android string-resource escapes to plain text."""
    out, i = [], 0
    while i < len(text):
        c = text[i]
        if c == "\\" and i + 1 < len(text):
            nxt = text[i + 1]
            out.append({"n": "\n", "t": "\t"}.get(nxt, nxt))
            i += 2
            continue
        out.append(c)
        i += 1
    return "".join(out)


def escape(text: str) -> str:
    """Plain text to an Android string resource's text (XML escaping is ElementTree's)."""
    text = text.replace("\\", "\\\\").replace("'", "\\'").replace('"', '\\"').replace("\n", "\\n").replace("\t", "\\t")
    if text[:1] in ("@", "?"):
        text = "\\" + text
    return text


@dataclass
class Strings:
    strings: dict[str, str] = field(default_factory=dict)
    notes: dict[str, str] = field(default_factory=dict)
    untranslatable: set[str] = field(default_factory=set)
    plurals: dict[str, dict[str, str]] = field(default_factory=dict)


def read(path: Path) -> Strings:
    """A strings.xml: its strings, plurals, and the comment above each."""
    result = Strings()
    if not path.is_file():
        return result
    parser = ET.XMLParser(target=ET.TreeBuilder(insert_comments=True))
    root = ET.parse(path, parser=parser).getroot()
    note = ""
    for element in root:
        if element.tag is ET.Comment:
            note = " ".join((element.text or "").split())
            continue
        name = element.get("name", "")
        if element.tag == "string":
            result.strings[name] = unescape("".join(element.itertext()))
            if element.get("translatable") == "false":
                result.untranslatable.add(name)
        elif element.tag == "plurals":
            result.plurals[name] = {
                item.get("quantity", ""): unescape("".join(item.itertext())) for item in element.findall("item")
            }
        else:
            continue
        if note:
            result.notes[name] = note
    return result


def modules(android: Path = ANDROID) -> list[Path]:
    """Each module's res folder that has English strings, as module path -> res."""
    found = []
    for values in sorted(android.glob("**/src/main/res/values/strings.xml")):
        if any(part in ("build", ".gradle", "build-logic") for part in values.parts):
            continue
        found.append(values.parent.parent)
    return found


def module_name(res: Path, android: Path) -> str:
    return res.parent.parent.parent.relative_to(android).as_posix()


def ui_context(path: Path = CONTEXT) -> dict[str, str]:
    """Each string's kind of place on screen (tools/i18n/ui-context.csv)."""
    if not path.is_file():
        return {}
    with path.open(encoding="utf-8", newline="") as f:
        return {row["name"]: row["kind"] for row in csv.DictReader(f)}


def review(name: str) -> str:
    for prefix, source in SPEC_LINKED.items():
        if name.startswith(prefix):
            return source
    return ""


@dataclass(frozen=True)
class Row:
    """One string, or one plural form, to translate."""
    module: str
    kind: str
    name: str
    quantity: str
    english: str
    note: str

    @property
    def key(self) -> tuple[str, str, str, str]:
        return (self.module, self.kind, self.name, self.quantity)


def source_rows(lang: str, android: Path = ANDROID) -> list[Row]:
    """Every row a complete translation into [lang] fills, in the app's order."""
    rows = []
    for res in modules(android):
        english = read(res / "values" / "strings.xml")
        module = module_name(res, android)
        for name, text in english.strings.items():
            if name not in english.untranslatable:
                rows.append(Row(module, "string", name, "", text, english.notes.get(name, "")))
        for name, items in english.plurals.items():
            for quantity in PLURAL_CATEGORIES.get(lang, ["other"]):
                rows.append(Row(module, "plural", name, quantity, items.get(quantity, items.get("other", "")),
                                english.notes.get(name, "")))
    return rows


def export(lang: str, out: Path, android: Path = ANDROID, context: Path = CONTEXT) -> int:
    kinds = ui_context(context)
    current: dict[str, Strings] = {}
    lines = []
    for row in source_rows(lang, android):
        if row.module not in current:
            current[row.module] = read(android / row.module / "src" / "main" / "res" / f"values-{lang}" / "strings.xml")
        have = current[row.module]
        translation = have.strings.get(row.name, "") if row.kind == "string" else \
            have.plurals.get(row.name, {}).get(row.quantity, "")
        lines.append([
            row.module, row.kind, row.name, row.quantity, row.english,
            WHERE.get(kinds.get(row.name, ""), ""), len(row.english), row.note,
            " ".join(placeholders(row.english)),
            PLURAL_RULES.get(lang, "") if row.kind == "plural" else "",
            review(row.name), translation, "", "",
        ])
    out.parent.mkdir(parents=True, exist_ok=True)
    with out.open("w", encoding="utf-8-sig", newline="") as f:
        writer = csv.writer(f, lineterminator="\n")
        writer.writerow(COLUMNS)
        writer.writerows(lines)
    return len(lines)


def export_all(directory: Path, android: Path = ANDROID, context: Path = CONTEXT) -> dict[str, int]:
    return {lang: export(lang, directory / f"{lang}.csv", android, context) for lang in translation_languages(android)}


def _letters(text: str) -> list[str]:
    return [c for c in text if unicodedata.category(c)[0] in ("L", "M")]


def _script_problem(lang: str, text: str) -> str:
    if lang not in SCRIPTS:
        return ""
    script, ranges = SCRIPTS[lang]
    letters = _letters(text)
    own = {c for c in letters if any(lo <= ord(c) <= hi for lo, hi in ranges)}
    other = {c for c in letters if INDIAN_SCRIPTS[0] <= ord(c) <= INDIAN_SCRIPTS[1] and c not in own}
    if other:
        return f"has letters of another Indian script ({''.join(sorted(other))[:12]}), not {script}"
    if not own:
        return f"has no {script} letter"
    return ""


def _text_problems(lang: str, text: str, source: str, row: dict[str, str]) -> list[str]:
    problems = []
    if placeholders(text) != placeholders(source):
        problems.append(f"placeholders {placeholders(text)} are not English's {placeholders(source)}")
    elif placeholders(source) and stray_percent(text):
        problems.append('a "%" that is not a placeholder: write "%%" for a percent sign')
    if text != text.strip():
        problems.append("starts or ends with a space or line break")
    if text.count("\n") != source.count("\n"):
        problems.append(f"{text.count(chr(10))} line breaks, English has {source.count(chr(10))}")
    invisible = sorted({f"U+{ord(c):04X}" for c in text if c in FORBIDDEN})
    if invisible:
        problems.append(f"invisible control characters {invisible}")
    keep = (row.get("keep_english") or "").strip().lower() == "yes"
    english_has_words = bool(_letters(PLACEHOLDER.sub("", source)))
    if english_has_words and not keep:
        if text == source:
            problems.append('identical to the English: translate it, or write "yes" in keep_english')
        else:
            problem = _script_problem(lang, PLACEHOLDER.sub("", text))
            if problem:
                problems.append(problem)
    return problems


def check(lang: str, worksheet: list[dict[str, str]], android: Path = ANDROID) -> list[str]:
    """What is wrong with a filled worksheet; empty only when every row can be imported."""
    problems = []
    expected = {row.key: row for row in source_rows(lang, android)}
    english_by_module = {module_name(res, android): read(res / "values" / "strings.xml") for res in modules(android)}
    if worksheet:
        missing_columns = [c for c in ("module", "kind", "name", "quantity", "english", "translation")
                           if c not in worksheet[0]]
        if missing_columns:
            return [f"the worksheet has no {', '.join(missing_columns)} column: export it again"]
    seen: set[tuple[str, str, str, str]] = set()
    for line, row in enumerate(worksheet, start=2):
        module, name, kind = row.get("module") or "", row.get("name") or "", row.get("kind") or ""
        quantity = row.get("quantity") or ""
        text = row.get("translation") or ""
        where = f"line {line} ({module} {name}{' ' + quantity if quantity else ''})"
        key = (module, kind, name, quantity)
        english = english_by_module.get(module)
        if english is None:
            problems.append(f"{where}: no module {module!r}")
        elif kind not in ("string", "plural"):
            problems.append(f"{where}: kind {kind!r} is neither string nor plural")
        elif kind == "string" and name in english.untranslatable:
            problems.append(f"{where}: {name!r} is not translated (translatable=\"false\")")
        elif kind == "string" and name not in english.strings:
            problems.append(f"{where}: English has no string {name!r}")
        elif kind == "plural" and name not in english.plurals:
            problems.append(f"{where}: English has no plurals {name!r}")
        elif kind == "plural" and quantity not in PLURAL_CATEGORIES.get(lang, ["other"]):
            problems.append(f"{where}: {lang} has no plural form {quantity!r}")
        elif key in seen:
            problems.append(f"{where}: this row appears twice")
        else:
            seen.add(key)
            source = expected[key].english
            if (row.get("english") or "") != source:
                problems.append(f"{where}: the English has changed since this worksheet was exported "
                                f"(now {source!r}): export again and check the translation")
            if not text.strip():
                problems.append(f"{where}: no translation")
            else:
                problems.extend(f"{where}: {p}" for p in _text_problems(lang, text, source, row))
    for key in expected:
        if key not in seen:
            module, _, name, quantity = key
            problems.append(f"{module} {name}{' ' + quantity if quantity else ''}: missing from the worksheet")
    return problems


def write(lang: str, worksheet: list[dict[str, str]], translated_by: str, reviewed_by: str,
          android: Path = ANDROID) -> list[Path]:
    """Writes each module's values-<lang>/strings.xml from a checked worksheet."""
    by_module: dict[str, list[dict[str, str]]] = {}
    for row in worksheet:
        if (row.get("translation") or "").strip():
            by_module.setdefault(row["module"], []).append(row)

    def clean(who: str) -> str:
        return re.sub(r"-{2,}", "-", " ".join(who.split()))  # an XML comment may not contain "--"

    written = []
    for res in modules(android):
        module = module_name(res, android)
        rows = by_module.get(module)
        if not rows:
            continue
        english = read(res / "values" / "strings.xml")
        strings = {r["name"]: unicodedata.normalize("NFC", r["translation"]) for r in rows if r["kind"] == "string"}
        plurals: dict[str, dict[str, str]] = {}
        for r in rows:
            if r["kind"] == "plural":
                plurals.setdefault(r["name"], {})[r["quantity"]] = unicodedata.normalize("NFC", r["translation"])
        root = ET.Element("resources")
        for name in english.strings:
            if name in strings:
                element = ET.SubElement(root, "string", name=name)
                element.text = escape(strings[name])
        for name in english.plurals:
            if name in plurals:
                element = ET.SubElement(root, "plurals", name=name)
                for quantity in PLURAL_CATEGORIES.get(lang, ["other"]):
                    if quantity in plurals[name]:
                        item = ET.SubElement(element, "item", quantity=quantity)
                        item.text = escape(plurals[name][quantity])
        ET.indent(root, space="    ")
        header = (
            f"<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<!--\n"
            f"  {lang} translation of {module}'s words, imported by tools/i18n/translation_kit.py.\n"
            f"  Translated by: {clean(translated_by)}\n"
            f"  Reviewed by: {clean(reviewed_by)}\n"
            f"  A string missing here is shown in English (res/values).\n-->\n"
        )
        target = res / f"values-{lang}" / "strings.xml"
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(header + ET.tostring(root, encoding="unicode") + "\n", encoding="utf-8", newline="\n")
        written.append(target)
    return written


def status(android: Path = ANDROID) -> list[str]:
    lines = []
    for lang in translation_languages(android):
        done = total = 0
        per_module = []
        for res in modules(android):
            english = read(res / "values" / "strings.xml")
            translated = read(res / f"values-{lang}" / "strings.xml")
            wanted = [n for n in english.strings if n not in english.untranslatable] + list(english.plurals)
            have = [n for n in wanted if n in translated.strings or n in translated.plurals]
            done += len(have)
            total += len(wanted)
            per_module.append(f"{module_name(res, android)} {len(have)}/{len(wanted)}")
        lines.append(f"{lang}: {done}/{total} ({', '.join(per_module)})")
    return lines


def words(text: str) -> int:
    """English words, as a translator counts them: placeholders are not words."""
    return len(re.findall(r"[A-Za-z0-9][A-Za-z0-9'’-]*", PLACEHOLDER.sub("", text)))


def audit(android: Path = ANDROID, context: Path = CONTEXT) -> list[str]:
    """The English source, measured: what a translator of one language takes on."""
    kinds = ui_context(context)
    lines = ["module | strings | plurals | not translated | worksheet rows | words | characters"]
    total = [0] * 6
    for res in modules(android):
        english = read(res / "values" / "strings.xml")
        translatable = [t for n, t in english.strings.items() if n not in english.untranslatable]
        plural_texts = [t for items in english.plurals.values() for t in items.values()]
        texts = translatable + plural_texts
        counts = [len(translatable), len(english.plurals), len(english.untranslatable),
                  len(translatable) + sum(len(items) for items in english.plurals.values()),
                  sum(map(words, texts)), sum(map(len, texts))]
        total = [a + b for a, b in zip(total, counts)]
        lines.append(f"{module_name(res, android)} | " + " | ".join(map(str, counts)))
    lines.append("total | " + " | ".join(map(str, total)))
    rows = source_rows(translation_languages(android)[0], android)
    by_kind: dict[str, int] = {}
    for r in rows:
        kind = kinds.get(r.name, "(none)")
        by_kind[kind] = by_kind.get(kind, 0) + 1
    lines.append(f"rows with placeholders: {sum(1 for r in rows if placeholders(r.english))}; "
                 f"rows without a note: {sum(1 for r in rows if not r.note)}; "
                 f"rows a spec or test quotes (second review): {sum(1 for r in rows if review(r.name))}")
    lines.append("rows by place: " + ", ".join(f"{k} {v}" for k, v in sorted(by_kind.items())))
    return lines


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("status")
    sub.add_parser("audit")
    p_export = sub.add_parser("export")
    which = p_export.add_mutually_exclusive_group(required=True)
    which.add_argument("--lang")
    which.add_argument("--all", action="store_true", help="every translation language, into the folder OUT")
    p_export.add_argument("out", type=Path)
    p_import = sub.add_parser("import")
    p_import.add_argument("--lang", required=True)
    p_import.add_argument("--translated-by", required=True,
                          help="who translated it (an owner-approved translator or agency), recorded in each file")
    p_import.add_argument("--reviewed-by", required=True,
                          help="who reviewed it: a second person, not the translator; recorded in each file")
    p_import.add_argument("worksheet", type=Path)
    args = parser.parse_args(argv)

    if args.command == "status":
        print("\n".join(status()))
        return 0
    if args.command == "audit":
        print("\n".join(audit()))
        return 0
    if args.command == "export" and args.all:
        for lang, count in export_all(args.out).items():
            print(f"{lang}: {count} rows written to {args.out / (lang + '.csv')}")
        return 0
    languages = translation_languages()
    if args.lang not in languages:
        print(f"{args.lang!r} is not a release language to translate into: {languages}", file=sys.stderr)
        return 2
    if args.command == "export":
        print(f"{export(args.lang, args.out)} rows written to {args.out}")
        return 0
    translator, reviewer = " ".join(args.translated_by.split()), " ".join(args.reviewed_by.split())
    if not translator or not reviewer:
        print("--translated-by and --reviewed-by must each name a person or agency", file=sys.stderr)
        return 2
    if translator.casefold() == reviewer.casefold():
        print("the reviewer must be a second person, not the translator", file=sys.stderr)
        return 2
    with args.worksheet.open(encoding="utf-8-sig", newline="") as f:
        worksheet = list(csv.DictReader(f))
    problems = check(args.lang, worksheet)
    if problems:
        print("\n".join(problems), file=sys.stderr)
        print(f"{len(problems)} problems: nothing was written", file=sys.stderr)
        return 1
    for path in write(args.lang, worksheet, translator, reviewer):
        print(f"wrote {path.relative_to(REPO)}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
