"""Tests for check_action_pins (standard library only, no network)."""

from __future__ import annotations

import contextlib
import io
from pathlib import Path
import tempfile
import unittest

import check_action_pins as pins

REPO = Path(__file__).resolve().parents[2]
SHA = "11d5960a326750d5838078e36cf38b85af677262"


class CheckLineTest(unittest.TestCase):
    """One `uses:` value at a time."""

    def test_accepted_forms(self) -> None:
        for value in (
            f"actions/checkout@{SHA} # v4.4.0",
            f"actions/checkout@{SHA}  #v4.4.0",
            f"'actions/checkout@{SHA}' # v4.4.0",
            f"owner/repo/sub/dir@{SHA} # v1.2.3",
            f"owner/repo/.github/workflows/reusable.yml@{SHA} # v2",
            "./.github/actions/local",
            "./.github/workflows/reusable.yml",
            "docker://alpine@sha256:" + "a" * 64,
        ):
            with self.subTest(value=value):
                self.assertIsNone(pins.check_line(value))

    def test_rejected_forms(self) -> None:
        cases = {
            "actions/checkout@v4": "tag or branch",
            "actions/checkout@v4.4.0": "tag or branch",
            "actions/checkout@main": "tag or branch",
            "actions/checkout@11d5960": "tag or branch",
            f"actions/checkout@{SHA}": "no '# vX.Y.Z' version comment",
            f"actions/checkout@{SHA} # latest": "no '# vX.Y.Z' version comment",
            f"actions/checkout@{SHA.upper()} # v4.4.0": "lowercase",
            "actions/checkout": "no @ref",
            "docker://alpine:3.20": "sha256 digest",
            "docker://alpine@sha256:abc": "sha256 digest",
            "": "empty",
        }
        for value, reason in cases.items():
            with self.subTest(value=value):
                got = pins.check_line(value)
                self.assertIsNotNone(got)
                self.assertIn(reason, got)


class CheckFileTest(unittest.TestCase):
    """Whole files: line numbers, comments and unparseable forms."""

    def scan(self, text: str) -> list[str]:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp, "w.yml")
            path.write_text(text, encoding="utf-8")
            return pins.check_file(path)

    def test_reports_each_unpinned_reference_with_its_line(self) -> None:
        got = self.scan(
            "jobs:\n  a:\n    steps:\n"
            f"      - uses: actions/checkout@{SHA} # v4.4.0\n"
            "      - uses: actions/setup-go@v5\n"
            "        with: {go-version: '1.26.10'}\n"
            "      - name: x\n        uses: astral-sh/setup-uv@v5\n"
        )
        self.assertEqual(len(got), 2)
        self.assertIn(":5: ", got[0])
        self.assertIn(":8: ", got[1])

    def test_commented_out_uses_is_ignored(self) -> None:
        self.assertEqual(self.scan("      # - uses: actions/checkout@v4\n"), [])

    def test_a_flow_mapping_uses_fails_closed(self) -> None:
        got = self.scan("    steps:\n      - {uses: actions/checkout@v4}\n")
        self.assertEqual(len(got), 1)
        self.assertIn("unparseable", got[0])

    def test_a_reusable_workflow_job_uses_is_checked(self) -> None:
        got = self.scan("jobs:\n  call:\n    uses: owner/repo/.github/workflows/x.yml@main\n")
        self.assertEqual(len(got), 1)


class MainTest(unittest.TestCase):
    """The command-line entry point."""

    def run_main(self, root: str) -> tuple[int, str]:
        # Captured: in CI an uncaptured "::error::" line would become a real
        # annotation on the PR.
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            code = pins.main(["x", root])
        return code, out.getvalue()

    def workflow(self, root: str, uses: str) -> None:
        wf = Path(root, ".github", "workflows")
        wf.mkdir(parents=True)
        (wf / "w.yml").write_text(f"    steps:\n      - uses: {uses}\n", encoding="utf-8")

    def test_no_workflows_fails_rather_than_passing_vacuously(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            code, out = self.run_main(tmp)
            self.assertEqual(code, 1)
            self.assertIn("refusing to pass vacuously", out)

    def test_an_unpinned_workflow_fails(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            self.workflow(tmp, "actions/checkout@v4")
            code, out = self.run_main(tmp)
            self.assertEqual(code, 1)
            self.assertIn("1 unpinned reference(s)", out)

    def test_a_pinned_workflow_passes(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            self.workflow(tmp, f"actions/checkout@{SHA} # v4.4.0")
            code, out = self.run_main(tmp)
            self.assertEqual(code, 0)
            self.assertIn("0 unpinned reference(s)", out)


class RepositoryTest(unittest.TestCase):
    """This repository's own workflows, so ci-gate enforces the rule on every PR."""

    def test_every_action_in_this_repository_is_pinned(self) -> None:
        files = pins.workflow_files(REPO)
        self.assertTrue(files, "found no workflow files")
        problems = [p for f in files for p in pins.check_file(f)]
        self.assertEqual(problems, [], "\n".join(problems))


if __name__ == "__main__":
    unittest.main()
