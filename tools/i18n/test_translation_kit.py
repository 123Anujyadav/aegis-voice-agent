"""Tests for translation_kit: run with `python3 -m unittest discover -s tools/i18n -v`."""

import ast
import contextlib
import csv
import io
from pathlib import Path
import socket
import tempfile
from typing import NoReturn
import unittest
from unittest import mock

import translation_kit as kit

ENGLISH = """<?xml version="1.0" encoding="utf-8"?>
<resources>
    <!-- The title of the screen -->
    <string name="x_title">Remove %1$s</string>
    <string name="x_brand" translatable="false">Aegis</string>
    <string name="x_plain">You\\'re "in" &amp; safe</string>
    <string name="x_bullet">• %1$s</string>
    <!-- How many seconds -->
    <plurals name="x_seconds">
        <item quantity="one">%1$d second</item>
        <item quantity="other">%1$d seconds</item>
    </plurals>
</resources>
"""

CONTEXT = "name,kind\nx_title,top-bar\nx_plain,text\nx_bullet,format\nx_seconds,message\n"

HINDI = {
    ("x_title", ""): "%1$s हटाएँ",
    ("x_plain", ""): "आप 'सुरक्षित' \"हैं\" & <ठीक>",
    ("x_bullet", ""): "• %1$s",
    ("x_seconds", "one"): "%1$d सेकंड",
    ("x_seconds", "other"): "%1$d सेकंड",
}

REPO_WORKSHEETS = kit.REPO / "docs" / "translation" / "worksheets"


class TranslationKitTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.android = Path(self.tmp.name) / "android"
        (self.android / "app").mkdir(parents=True)
        (self.android / "app" / "release-locales.txt").write_text(
            "# languages\nen\nhi\nta\n", encoding="utf-8"
        )
        self.values = self.android / "feature" / "x" / "src" / "main" / "res" / "values"
        self.values.mkdir(parents=True)
        (self.values / "strings.xml").write_text(ENGLISH, encoding="utf-8")
        self.context = Path(self.tmp.name) / "ui-context.csv"
        self.context.write_text(CONTEXT, encoding="utf-8")
        # A build output must never be read as a module.
        stray = self.android / "feature" / "x" / "build" / "src" / "main" / "res" / "values"
        stray.mkdir(parents=True)
        (stray / "strings.xml").write_text(ENGLISH, encoding="utf-8")

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def worksheet(self, lang: str = "hi") -> list[dict[str, str]]:
        out = Path(self.tmp.name) / f"{lang}.csv"
        kit.export(lang, out, self.android, self.context)
        with out.open(encoding="utf-8-sig", newline="") as f:
            return list(csv.DictReader(f))

    def fill(
        self, rows: list[dict[str, str]], translations: dict[tuple[str, str], str]
    ) -> list[dict[str, str]]:
        for row in rows:
            key = (row["name"], row["quantity"])
            if key in translations:
                row["translation"] = translations[key]
        return rows

    def problems(
        self,
        translations: dict[tuple[str, str], str],
        lang: str = "hi",
        rows: list[dict[str, str]] | None = None,
    ) -> list[str]:
        rows = self.fill(rows if rows is not None else self.worksheet(lang), translations)
        return kit.check(lang, rows, self.android)

    def assert_refused(self, problems: list[str], *expected: str) -> None:
        for text in expected:
            self.assertTrue(any(text in p for p in problems), f"{text!r} not reported: {problems}")

    def test_the_worksheet_has_every_translatable_string_and_plural_form_with_its_context(
        self,
    ) -> None:
        rows = self.worksheet()
        self.assertEqual(kit.COLUMNS, list(rows[0].keys()))
        self.assertEqual(
            [(r["kind"], r["name"], r["quantity"]) for r in rows],
            [
                ("string", "x_title", ""),
                ("string", "x_plain", ""),
                ("string", "x_bullet", ""),
                ("plural", "x_seconds", "one"),
                ("plural", "x_seconds", "other"),
            ],
        )
        title = rows[0]
        self.assertEqual("feature/x", title["module"])
        self.assertEqual("Remove %1$s", title["english"])
        self.assertEqual(kit.WHERE["top-bar"], title["where"])
        self.assertEqual("11", title["english_chars"])
        self.assertEqual("The title of the screen", title["note"])
        self.assertEqual("%1$s", title["placeholders"])
        self.assertEqual("", title["translation"])
        self.assertEqual('You\'re "in" & safe', rows[1]["english"])
        self.assertEqual("one = 0 or 1; other = every other number", rows[3]["plural_rule"])
        self.assertEqual("", rows[0]["plural_rule"])
        self.assertIn("exactly 1", self.worksheet("ta")[3]["plural_rule"])

    def test_spec_linked_strings_are_marked_for_a_second_review(self) -> None:
        self.assertIn("ACCOUNT-DELETION.md", kit.review("settings_deletion_permanent"))
        self.assertIn("A09b", kit.review("onboarding_role_never"))
        self.assertEqual("", kit.review("calls_feed_title"))

    def test_a_complete_checked_worksheet_is_written_and_reads_back_the_same(self) -> None:
        rows = self.fill(self.worksheet(), HINDI)
        self.assertEqual([], kit.check("hi", rows, self.android))
        written = kit.write("hi", rows, "Translator A -- 2026-11-02", "Reviewer B", self.android)
        target = self.values.parent / "values-hi" / "strings.xml"
        self.assertEqual([target], written)
        text = target.read_text(encoding="utf-8")
        self.assertIn(
            "Translated by: Translator A - 2026-11-02", text
        )  # an XML comment may not hold "--"
        self.assertIn("Reviewed by: Reviewer B", text)
        self.assertIn("\\'", text)
        back = kit.read(target)
        self.assertEqual(
            {"x_title": "%1$s हटाएँ", "x_plain": "आप 'सुरक्षित' \"हैं\" & <ठीक>", "x_bullet": "• %1$s"},
            back.strings,
        )
        self.assertEqual({"x_seconds": {"one": "%1$d सेकंड", "other": "%1$d सेकंड"}}, back.plurals)
        # Exported again, the translation is carried in the worksheet.
        self.assertEqual("%1$s हटाएँ", self.worksheet()[0]["translation"])

    def test_an_incomplete_worksheet_is_refused(self) -> None:
        problems = self.problems({("x_title", ""): "%1$s हटाएँ"})
        self.assertEqual(4, len(problems), "\n".join(problems))
        self.assert_refused(
            problems,
            "(feature/x x_plain): no translation",
            "(feature/x x_seconds other): no translation",
        )
        rows = [r for r in self.fill(self.worksheet(), HINDI) if r["name"] != "x_plain"]
        self.assertEqual(
            ["feature/x x_plain: missing from the worksheet"], kit.check("hi", rows, self.android)
        )

    def test_an_import_with_any_problem_writes_nothing(self) -> None:
        sheet = Path(self.tmp.name) / "partial.csv"
        rows = self.fill(self.worksheet(), {("x_title", ""): "%1$s हटाएँ"})
        with sheet.open("w", encoding="utf-8-sig", newline="") as f:
            writer = csv.DictWriter(f, fieldnames=kit.COLUMNS)
            writer.writeheader()
            writer.writerows(rows)
        with (
            mock.patch.object(kit, "ANDROID", self.android),
            contextlib.redirect_stderr(io.StringIO()) as err,
        ):
            code = kit.main(
                [
                    "import",
                    "--lang",
                    "hi",
                    "--translated-by",
                    "A",
                    "--reviewed-by",
                    "B",
                    "--names-consented",
                    str(sheet),
                ]
            )
        self.assertEqual(1, code)
        self.assertIn("nothing was written", err.getvalue())
        self.assertFalse((self.values.parent / "values-hi").exists())

    def test_the_translator_cannot_be_the_reviewer(self) -> None:
        sheet = Path(self.tmp.name) / "hi.csv"
        with contextlib.redirect_stderr(io.StringIO()) as err:
            code = kit.main(
                [
                    "import",
                    "--lang",
                    "hi",
                    "--translated-by",
                    "Asha  K",
                    "--reviewed-by",
                    "asha k",
                    str(sheet),
                ]
            )
        self.assertEqual(2, code)
        self.assertIn("second person", err.getvalue())

    def test_names_are_recorded_only_with_their_consent(self) -> None:
        # Owner decision Q0: consent before anyone's name is written into the repository.
        sheet = Path(self.tmp.name) / "hi.csv"
        with contextlib.redirect_stderr(io.StringIO()) as err:
            code = kit.main(
                ["import", "--lang", "hi", "--translated-by", "A", "--reviewed-by", "B", str(sheet)]
            )
        self.assertEqual(2, code)
        self.assertIn("--names-consented", err.getvalue())

    def test_the_owners_terms_are_kept(self) -> None:
        # Owner decisions Q1, Q3 and Q4, checked on rows like the app's own.
        def problems(name: str, source: str, text: str) -> list[str]:
            return kit.owner_term_problems(name, text, source)

        self.assertEqual([], problems("t", "Welcome to Lily AI", "Lily AI में आपका स्वागत है"))
        self.assertTrue(problems("t", "Welcome to Lily AI", "लिली एआई में आपका स्वागत है"))
        self.assertEqual([], problems("t", "AI screenings", "AI स्क्रीनिंग"))
        self.assertTrue(problems("t", "AI screenings", "एआई स्क्रीनिंग"))
        self.assertEqual([], problems("onboarding_phone_resend", "Send a new code", "नया OTP भेजें"))
        self.assertTrue(problems("onboarding_phone_resend", "Send a new code", "नया कोड भेजें"))
        # Other codes are not the sign-in code.
        self.assertEqual(
            [],
            problems(
                "onboarding_phone_label_number",
                "Mobile number, with country code",
                "देश कोड सहित मोबाइल नंबर",
            ),
        )

    def test_a_worksheet_that_changes_what_the_app_does_is_refused(self) -> None:
        rows = self.fill(
            self.worksheet(), {**HINDI, ("x_title", ""): "%1$d हटाएँ"}
        )  # %1$s became %1$d
        rows = [r for r in rows if r["quantity"] != "other"]  # no "other"
        rows.append(
            {
                "module": "feature/x",
                "kind": "string",
                "name": "x_brand",
                "quantity": "",
                "translation": "एजिस",
            }
        )
        rows.append(
            {
                "module": "feature/x",
                "kind": "string",
                "name": "x_unknown",
                "quantity": "",
                "translation": "?",
            }
        )
        rows.append(
            {
                "module": "feature/y",
                "kind": "string",
                "name": "x_title",
                "quantity": "",
                "translation": "?",
            }
        )
        rows.append(
            {
                "module": "feature/x",
                "kind": "plural",
                "name": "x_seconds",
                "quantity": "few",
                "translation": "%1$d",
            }
        )
        rows.append(
            {
                "module": "feature/x",
                "kind": "array",
                "name": "x_title",
                "quantity": "",
                "translation": "?",
            }
        )
        problems = kit.check("hi", rows, self.android)
        self.assertEqual(7, len(problems), "\n".join(problems))
        self.assert_refused(
            problems,
            "placeholders",
            "translatable",
            "no string 'x_unknown'",
            "no module",
            "no plural form 'few'",
            "neither string nor plural",
            "x_seconds other: missing",
        )

    def test_a_worksheet_exported_from_older_english_is_refused(self) -> None:
        rows = self.fill(self.worksheet(), HINDI)
        (self.values / "strings.xml").write_text(
            ENGLISH.replace("You\\'re", "You are"), encoding="utf-8"
        )
        self.assert_refused(
            kit.check("hi", rows, self.android), "(feature/x x_plain): the English has changed"
        )

    def test_a_row_given_twice_is_refused(self) -> None:
        rows = self.fill(self.worksheet(), HINDI)
        rows.append(dict(rows[0]))
        self.assert_refused(kit.check("hi", rows, self.android), "appears twice")

    def test_english_left_in_place_is_refused_unless_kept_on_purpose(self) -> None:
        rows = self.fill(self.worksheet(), {**HINDI, ("x_plain", ""): 'You\'re "in" & safe'})
        self.assert_refused(kit.check("hi", rows, self.android), "identical to the English")
        rows[1]["keep_english"] = "yes"
        self.assertEqual([], kit.check("hi", rows, self.android))
        # A pattern without words ("• %1$s") may stay as it is.
        self.assertEqual([], self.problems(HINDI))

    def test_a_translation_in_the_wrong_script_is_refused(self) -> None:
        self.assert_refused(
            self.problems({**HINDI, ("x_plain", ""): "நீங்கள் பாதுகாப்பாக இருக்கிறீர்கள்"}),
            "another Indian script",
            "Devanagari",
        )
        self.assert_refused(
            self.problems({**HINDI, ("x_plain", ""): "Aap surakshit hain"}),
            "has no Devanagari letter",
        )
        tamil = {
            ("x_title", ""): "%1$s நீக்கு",
            ("x_plain", ""): 'நீங்கள் "உள்ளே" & பாதுகாப்பு',
            ("x_bullet", ""): "• %1$s",
            ("x_seconds", "one"): "%1$d வினாடி",
            ("x_seconds", "other"): "%1$d வினாடிகள்",
        }
        self.assertEqual([], self.problems(tamil, lang="ta"))
        self.assert_refused(
            self.problems({**tamil, ("x_title", ""): "%1$s हटाएँ"}, lang="ta"), "not Tamil"
        )

    def test_hidden_characters_spaces_line_breaks_and_stray_percents_are_refused(self) -> None:
        self.assert_refused(
            self.problems({**HINDI, ("x_plain", ""): "आप सुरक्षित \u202eहैं"}), "U+202E"
        )
        self.assert_refused(
            self.problems({**HINDI, ("x_plain", ""): " आप सुरक्षित हैं"}), "starts or ends"
        )
        self.assert_refused(
            self.problems({**HINDI, ("x_plain", ""): "आप\nसुरक्षित हैं"}), "1 line breaks"
        )
        self.assert_refused(self.problems({**HINDI, ("x_title", ""): "%1$s 100% हटाएँ"}), 'a "%"')
        self.assertEqual([], self.problems({**HINDI, ("x_title", ""): "%1$s 100%% हटाएँ"}))
        # Joiners choose a letter's form in Indian scripts, and are kept.
        self.assertEqual([], self.problems({**HINDI, ("x_plain", ""): "आप सुरक्षित\u200d हैं"}))

    def test_imported_text_is_normalised(self) -> None:
        decomposed = "\u0928\u093c"  # na + nukta, which NFC composes to U+0929
        rows = self.fill(self.worksheet(), {**HINDI, ("x_plain", ""): decomposed + " ठीक"})
        self.assertEqual([], kit.check("hi", rows, self.android))
        kit.write("hi", rows, "A", "B", self.android)
        self.assertEqual(
            "\u0929 ठीक",
            kit.read(self.values.parent / "values-hi" / "strings.xml").strings["x_plain"],
        )

    def test_status_counts_each_language_against_english(self) -> None:
        self.assertEqual(
            ["hi: 0/4 (feature/x 0/4)", "ta: 0/4 (feature/x 0/4)"], kit.status(self.android)
        )
        kit.write("hi", self.fill(self.worksheet(), HINDI), "A", "B", self.android)
        self.assertEqual("hi: 4/4 (feature/x 4/4)", kit.status(self.android)[0])

    def test_every_language_s_worksheet_is_exported(self) -> None:
        out = Path(self.tmp.name) / "sheets"
        self.assertEqual({"hi": 5, "ta": 5}, kit.export_all(out, self.android, self.context))
        self.assertEqual(["hi.csv", "ta.csv"], sorted(p.name for p in out.iterdir()))

    def test_the_audit_counts_the_english(self) -> None:
        lines = kit.audit(self.android, self.context)
        self.assertIn("feature/x | 3 | 1 | 1 | 5 | 6 | 58", lines)
        self.assertIn("rows by place: format 1, message 2, text 1, top-bar 1", lines)

    def test_only_release_languages_can_be_imported(self) -> None:
        self.assertEqual(
            2, kit.main(["export", "--lang", "bn", str(Path(self.tmp.name) / "bn.csv")])
        )

    def test_escaping_round_trips(self) -> None:
        for text in ["plain", "it's", 'say "hi"', "back\\slash", "@ref", "?attr", "two\nlines"]:
            self.assertEqual(text, kit.unescape(kit.escape(text)), text)

    def test_it_works_with_no_network_at_all(self) -> None:
        def no_network(*_args: object, **_kwargs: object) -> NoReturn:
            raise AssertionError("translation_kit tried to open a network connection")

        with (
            mock.patch.object(socket, "socket", no_network),
            mock.patch.object(socket, "create_connection", no_network),
            mock.patch.object(socket, "getaddrinfo", no_network),
        ):
            rows = self.fill(self.worksheet(), HINDI)
            self.assertEqual([], kit.check("hi", rows, self.android))
            kit.write("hi", rows, "A", "B", self.android)
            kit.status(self.android)
            kit.audit(self.android, self.context)

    def test_it_imports_no_networking_module(self) -> None:
        tree = ast.parse(Path(kit.__file__).read_text(encoding="utf-8"))
        imported = {
            alias.name.split(".")[0]
            for node in ast.walk(tree)
            if isinstance(node, ast.Import)
            for alias in node.names
        }
        imported |= {
            node.module.split(".")[0]
            for node in ast.walk(tree)
            if isinstance(node, ast.ImportFrom) and node.module
        }
        self.assertEqual(
            set(),
            imported
            & {"socket", "ssl", "http", "urllib", "requests", "ftplib", "smtplib", "asyncio"},
        )


class RepositoryTest(unittest.TestCase):
    """The repository's own English, context table and committed worksheets."""

    def test_every_translatable_string_says_where_it_appears(self) -> None:
        context = kit.ui_context()
        names = {row.name for row in kit.source_rows("hi")}
        self.assertEqual(set(), names - set(context), "add these to tools/i18n/ui-context.csv")
        self.assertEqual(set(), set(context) - names, "remove these from tools/i18n/ui-context.csv")
        self.assertEqual(set(), set(context.values()) - set(kit.WHERE))

    def test_the_sign_in_code_rows_are_the_apps_rows_about_that_code(self) -> None:
        # Q4's list must name real rows, each about the sign-in code, and miss none.
        rows = {row.name: row.english for row in kit.source_rows("hi")}
        self.assertTrue(rows.keys() >= kit.SIGN_IN_CODE_ROWS, kit.SIGN_IN_CODE_ROWS - rows.keys())
        other_codes = ("country code", "area code", "exact code", "coded")
        about_code = {
            name
            for name, english in rows.items()
            if "code" in english.lower() and not any(o in english.lower() for o in other_codes)
        }
        self.assertEqual(about_code, set(kit.SIGN_IN_CODE_ROWS))

    def test_the_committed_worksheets_match_the_english(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            fresh = kit.export_all(Path(tmp))
            for lang in fresh:
                committed = REPO_WORKSHEETS / f"{lang}.csv"
                self.assertTrue(committed.is_file(), f"{committed} is missing")
                self.assertEqual(
                    (Path(tmp) / f"{lang}.csv").read_bytes(),
                    committed.read_bytes(),
                    f"{committed.name} is out of date: run "
                    "python3 tools/i18n/translation_kit.py export --all docs/translation/worksheets",
                )
            self.assertEqual(
                sorted(f"{lang}.csv" for lang in fresh),
                sorted(p.name for p in REPO_WORKSHEETS.glob("*.csv")),
            )


if __name__ == "__main__":
    unittest.main()
