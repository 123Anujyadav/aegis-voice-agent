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
    export --lang hi OUT.csv  a worksheet: every translatable string and plural form,
                              its English, the note above it, its placeholders,
                              and any translation already in the app
    import --lang hi --reviewed-by "..." IN.csv
                              writes the worksheet's translations into each module's
                              values-hi/strings.xml, after checking them; a row left
                              empty stays English

Import refuses a worksheet that changes a placeholder, names a string English
does not have, translates one marked translatable="false", or gives a plural
without its "other" form. The same rules are enforced in the build by
TranslationConsistencyTest; this tool reports them to the translator first.
"""

from __future__ import annotations

import argparse
import csv
import re
import sys
import xml.etree.ElementTree as ET
from dataclasses import dataclass, field
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
ANDROID = REPO / "android"

# CLDR plural categories for cardinal numbers. Each release language uses
# "one" and "other" (https://www.unicode.org/cldr/charts/latest/supplemental/language_plural_rules.html).
PLURAL_CATEGORIES = {code: ["one", "other"] for code in ("en", "hi", "ta", "te", "ml", "kn")}

PLACEHOLDER = re.compile(r"%(\d+\$)?[-#+ 0,(]*\d*(\.\d+)?[sdfxXoc]")

COLUMNS = ["module", "kind", "name", "quantity", "english", "note", "placeholders", "translation"]


def placeholders(text: str) -> list[str]:
    """%1$s, %2$d, ...: what the code fills in. "%%" is a literal percent sign."""
    return sorted(m.group(0) for m in PLACEHOLDER.finditer(text.replace("%%", "")))


def release_languages(android: Path = ANDROID) -> list[str]:
    text = (android / "app" / "release-locales.txt").read_text(encoding="utf-8")
    return [line.split("#")[0].strip() for line in text.splitlines() if line.split("#")[0].strip()]


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


def export(lang: str, out: Path, android: Path = ANDROID) -> int:
    rows = []
    for res in modules(android):
        english = read(res / "values" / "strings.xml")
        current = read(res / f"values-{lang}" / "strings.xml")
        module = module_name(res, android)
        for name, text in english.strings.items():
            if name in english.untranslatable:
                continue
            rows.append([module, "string", name, "", text, english.notes.get(name, ""),
                         " ".join(placeholders(text)), current.strings.get(name, "")])
        for name, items in english.plurals.items():
            for quantity in PLURAL_CATEGORIES.get(lang, ["other"]):
                text = items.get(quantity, items.get("other", ""))
                rows.append([module, "plural", name, quantity, text, english.notes.get(name, ""),
                             " ".join(placeholders(text)), current.plurals.get(name, {}).get(quantity, "")])
    with out.open("w", encoding="utf-8-sig", newline="") as f:
        writer = csv.writer(f)
        writer.writerow(COLUMNS)
        writer.writerows(rows)
    return len(rows)


def check(lang: str, worksheet: list[dict[str, str]], android: Path = ANDROID) -> list[str]:
    """What is wrong with a filled worksheet, row by row."""
    problems = []
    english_by_module = {module_name(res, android): read(res / "values" / "strings.xml") for res in modules(android)}
    plural_forms: dict[tuple[str, str], set[str]] = {}
    for line, row in enumerate(worksheet, start=2):
        text = row.get("translation", "")
        if not text.strip():
            continue
        module, name, kind = row.get("module", ""), row.get("name", ""), row.get("kind", "")
        where = f"line {line} ({module} {name})"
        english = english_by_module.get(module)
        if english is None:
            problems.append(f"{where}: no module {module!r}")
            continue
        if kind == "string":
            if name not in english.strings:
                problems.append(f"{where}: English has no string {name!r}")
            elif name in english.untranslatable:
                problems.append(f"{where}: {name!r} is not translated (translatable=\"false\")")
            elif placeholders(text) != placeholders(english.strings[name]):
                problems.append(f"{where}: placeholders {placeholders(text)} are not English's {placeholders(english.strings[name])}")
        elif kind == "plural":
            quantity = row.get("quantity", "")
            if name not in english.plurals:
                problems.append(f"{where}: English has no plurals {name!r}")
            elif quantity not in PLURAL_CATEGORIES.get(lang, ["other"]):
                problems.append(f"{where}: {lang} has no plural form {quantity!r}")
            elif placeholders(text) != placeholders(english.plurals[name].get("other", "")):
                problems.append(f"{where}: placeholders {placeholders(text)} are not English's {placeholders(english.plurals[name].get('other', ''))}")
            plural_forms.setdefault((module, name), set()).add(quantity)
        else:
            problems.append(f"{where}: kind {kind!r} is neither string nor plural")
    for (module, name), forms in plural_forms.items():
        if "other" not in forms:
            problems.append(f"{module} plurals {name}: no \"other\" form")
    return problems


def write(lang: str, worksheet: list[dict[str, str]], reviewed_by: str, android: Path = ANDROID) -> list[Path]:
    """Writes each module's values-<lang>/strings.xml from a checked worksheet."""
    by_module: dict[str, list[dict[str, str]]] = {}
    for row in worksheet:
        if row.get("translation", "").strip():
            by_module.setdefault(row["module"], []).append(row)
    written = []
    for res in modules(android):
        module = module_name(res, android)
        rows = by_module.get(module)
        if not rows:
            continue
        english = read(res / "values" / "strings.xml")
        strings = {r["name"]: r["translation"] for r in rows if r["kind"] == "string"}
        plurals: dict[str, dict[str, str]] = {}
        for r in rows:
            if r["kind"] == "plural":
                plurals.setdefault(r["name"], {})[r["quantity"]] = r["translation"]
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
        # An XML comment may not contain "--".
        reviewer = re.sub(r"-{2,}", "-", " ".join(reviewed_by.split()))
        header = (
            f"<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<!--\n"
            f"  {lang} translation of {module}'s words, imported by tools/i18n/translation_kit.py.\n"
            f"  Reviewed by: {reviewer}\n"
            f"  A string missing here is shown in English (res/values).\n-->\n"
        )
        target = res / f"values-{lang}" / "strings.xml"
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(header + ET.tostring(root, encoding="unicode") + "\n", encoding="utf-8", newline="\n")
        written.append(target)
    return written


def status(android: Path = ANDROID) -> list[str]:
    lines = []
    for lang in release_languages(android):
        if lang == "en":
            continue
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


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("status")
    p_export = sub.add_parser("export")
    p_export.add_argument("--lang", required=True)
    p_export.add_argument("out", type=Path)
    p_import = sub.add_parser("import")
    p_import.add_argument("--lang", required=True)
    p_import.add_argument("--reviewed-by", required=True,
                          help="who reviewed this translation (an owner-approved translator or agency), recorded in each file")
    p_import.add_argument("worksheet", type=Path)
    args = parser.parse_args(argv)

    if args.command == "status":
        print("\n".join(status()))
        return 0
    languages = [code for code in release_languages() if code != "en"]
    if args.lang not in languages:
        print(f"{args.lang!r} is not a release language to translate into: {languages}", file=sys.stderr)
        return 2
    if args.command == "export":
        print(f"{export(args.lang, args.out)} rows written to {args.out}")
        return 0
    with args.worksheet.open(encoding="utf-8-sig", newline="") as f:
        worksheet = list(csv.DictReader(f))
    if not args.reviewed_by.strip():
        print("--reviewed-by must name who reviewed the translation", file=sys.stderr)
        return 2
    problems = check(args.lang, worksheet)
    if problems:
        print("\n".join(problems), file=sys.stderr)
        return 1
    for path in write(args.lang, worksheet, args.reviewed_by):
        print(f"wrote {path.relative_to(REPO)}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
