"""Tests for translation_kit: run with `python3 -m unittest discover -s tools/i18n -v`."""

import csv
import tempfile
import unittest
from pathlib import Path

import translation_kit as kit

ENGLISH = """<?xml version="1.0" encoding="utf-8"?>
<resources>
    <!-- The title of the screen -->
    <string name="x_title">Remove %1$s</string>
    <string name="x_brand" translatable="false">Aegis</string>
    <string name="x_plain">You\\'re "in" &amp; safe</string>
    <!-- How many seconds -->
    <plurals name="x_seconds">
        <item quantity="one">%1$d second</item>
        <item quantity="other">%1$d seconds</item>
    </plurals>
</resources>
"""


class TranslationKitTest(unittest.TestCase):

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.android = Path(self.tmp.name) / "android"
        (self.android / "app").mkdir(parents=True)
        (self.android / "app" / "release-locales.txt").write_text("# languages\nen\nhi\nta\n", encoding="utf-8")
        self.values = self.android / "feature" / "x" / "src" / "main" / "res" / "values"
        self.values.mkdir(parents=True)
        (self.values / "strings.xml").write_text(ENGLISH, encoding="utf-8")
        # A build output must never be read as a module.
        stray = self.android / "feature" / "x" / "build" / "src" / "main" / "res" / "values"
        stray.mkdir(parents=True)
        (stray / "strings.xml").write_text(ENGLISH, encoding="utf-8")

    def tearDown(self):
        self.tmp.cleanup()

    def worksheet(self, lang="hi"):
        out = Path(self.tmp.name) / f"{lang}.csv"
        kit.export(lang, out, self.android)
        with out.open(encoding="utf-8-sig", newline="") as f:
            return list(csv.DictReader(f))

    def fill(self, rows, translations):
        for row in rows:
            key = (row["name"], row["quantity"])
            if key in translations:
                row["translation"] = translations[key]
        return rows

    def test_the_worksheet_has_every_translatable_string_and_plural_form_with_its_note(self):
        rows = self.worksheet()
        self.assertEqual(
            [(r["kind"], r["name"], r["quantity"]) for r in rows],
            [("string", "x_title", ""), ("string", "x_plain", ""), ("plural", "x_seconds", "one"), ("plural", "x_seconds", "other")],
        )
        title = rows[0]
        self.assertEqual("feature/x", title["module"])
        self.assertEqual("Remove %1$s", title["english"])
        self.assertEqual("The title of the screen", title["note"])
        self.assertEqual("%1$s", title["placeholders"])
        self.assertEqual("You're \"in\" & safe", rows[1]["english"])
        self.assertEqual("", title["translation"])

    def test_a_checked_worksheet_is_written_and_reads_back_the_same(self):
        rows = self.fill(self.worksheet(), {
            ("x_title", ""): "%1$s हटाएँ",
            ("x_plain", ""): "आप 'सुरक्षित' \"हैं\" & <ठीक>",
            ("x_seconds", "one"): "%1$d सेकंड",
            ("x_seconds", "other"): "%1$d सेकंड",
        })
        self.assertEqual([], kit.check("hi", rows, self.android))
        written = kit.write("hi", rows, "Approved translator A -- 2026-11-02", self.android)
        target = self.values.parent / "values-hi" / "strings.xml"
        self.assertEqual([target], written)
        text = target.read_text(encoding="utf-8")
        self.assertIn("Reviewed by: Approved translator A - 2026-11-02", text)  # an XML comment may not hold "--"
        kit.read(target)  # and the file still parses
        self.assertIn("\\'", text)
        back = kit.read(target)
        self.assertEqual({"x_title": "%1$s हटाएँ", "x_plain": "आप 'सुरक्षित' \"हैं\" & <ठीक>"}, back.strings)
        self.assertEqual({"x_seconds": {"one": "%1$d सेकंड", "other": "%1$d सेकंड"}}, back.plurals)
        # Exported again, the translation is carried in the worksheet.
        self.assertEqual("%1$s हटाएँ", self.worksheet()[0]["translation"])

    def test_an_empty_row_stays_english(self):
        rows = self.fill(self.worksheet(), {("x_title", ""): "%1$s हटाएँ"})
        kit.write("hi", rows, "reviewer", self.android)
        back = kit.read(self.values.parent / "values-hi" / "strings.xml")
        self.assertEqual({"x_title": "%1$s हटाएँ"}, back.strings)
        self.assertEqual({}, back.plurals)

    def test_a_worksheet_that_changes_what_the_app_does_is_refused(self):
        rows = self.fill(self.worksheet(), {
            ("x_title", ""): "%1$d हटाएँ",  # %1$s became %1$d
            ("x_seconds", "one"): "%1$d सेकंड",  # no "other"
        })
        rows.append({"module": "feature/x", "kind": "string", "name": "x_brand", "quantity": "", "translation": "एजिस"})
        rows.append({"module": "feature/x", "kind": "string", "name": "x_unknown", "quantity": "", "translation": "?"})
        rows.append({"module": "feature/y", "kind": "string", "name": "x_title", "quantity": "", "translation": "?"})
        rows.append({"module": "feature/x", "kind": "plural", "name": "x_seconds", "quantity": "few", "translation": "%1$d"})
        rows.append({"module": "feature/x", "kind": "array", "name": "x_title", "quantity": "", "translation": "?"})
        problems = kit.check("hi", rows, self.android)
        self.assertEqual(7, len(problems), "\n".join(problems))
        for expected in ("placeholders", "translatable", "no string 'x_unknown'", "no module", "no plural form 'few'",
                         "neither string nor plural", 'no "other" form'):
            self.assertTrue(any(expected in p for p in problems), f"{expected!r} not reported: {problems}")

    def test_status_counts_each_language_against_english(self):
        self.assertEqual(["hi: 0/3 (feature/x 0/3)", "ta: 0/3 (feature/x 0/3)"], kit.status(self.android))
        rows = self.fill(self.worksheet(), {("x_title", ""): "%1$s हटाएँ"})
        kit.write("hi", rows, "reviewer", self.android)
        self.assertEqual("hi: 1/3 (feature/x 1/3)", kit.status(self.android)[0])

    def test_only_release_languages_can_be_imported(self):
        self.assertEqual(2, kit.main(["export", "--lang", "bn", str(Path(self.tmp.name) / "bn.csv")]))

    def test_escaping_round_trips(self):
        for text in ["plain", "it's", 'say "hi"', "back\\slash", "@ref", "?attr", "two\nlines"]:
            self.assertEqual(text, kit.unescape(kit.escape(text)), text)


if __name__ == "__main__":
    unittest.main()
