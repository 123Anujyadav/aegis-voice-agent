"""Offline tests for ci_gate: trigger parsing, path matching and job evaluation.

They read the repository's real workflow files, so a change to any workflow's
pull_request trigger that the gate cannot evaluate fails here first.
"""

from __future__ import annotations

from pathlib import Path
import tempfile
import unittest

import ci_gate

WORKFLOWS = Path(__file__).resolve().parents[2] / ".github" / "workflows"


class PatternTest(unittest.TestCase):
    """GitHub filter-pattern semantics for the subset the gate supports."""

    def test_double_star_crosses_directories(self) -> None:
        self.assertTrue(ci_gate.matches(("services/go/**",), "services/go/voice/cmd/server/main.go"))

    def test_single_star_stays_in_one_segment(self) -> None:
        self.assertTrue(ci_gate.matches(("packages/*/contracts-*/**",), "packages/go/contracts-go/x.go"))
        self.assertFalse(ci_gate.matches(("packages/*/x",), "packages/go/deep/x"))

    def test_exact_file(self) -> None:
        self.assertTrue(ci_gate.matches(("go.work",), "go.work"))
        self.assertFalse(ci_gate.matches(("go.work",), "go.work.sum"))

    def test_negation_applies_in_order(self) -> None:
        patterns = ("docs/**", "!docs/private/**")
        self.assertTrue(ci_gate.matches(patterns, "docs/a.md"))
        self.assertFalse(ci_gate.matches(patterns, "docs/private/a.md"))

    def test_character_classes_are_refused(self) -> None:
        with self.assertRaises(ci_gate.UnsupportedTriggerError):
            ci_gate.matches(("src/[ab].go",), "src/a.go")


class TriggerParsingTest(unittest.TestCase):
    """The parser on the repository's real workflows, and its fail-closed cases."""

    def parse(self, name: str) -> ci_gate.Trigger | None:
        path = WORKFLOWS / name
        return ci_gate.parse_pull_request_trigger(f".github/workflows/{name}", path.read_text(encoding="utf-8"))

    def test_every_repository_workflow_parses(self) -> None:
        for path in sorted(WORKFLOWS.glob("*.y*ml")):
            with self.subTest(workflow=path.name):
                ci_gate.parse_pull_request_trigger(path.name, path.read_text(encoding="utf-8"))

    def test_backend_foundation_has_branches_and_no_paths(self) -> None:
        trigger = self.parse("ci.yml")
        assert trigger is not None
        self.assertEqual(trigger.branches, ("main", "develop"))
        self.assertIsNone(trigger.paths)

    def test_pr_go_reads_its_pull_request_paths_not_its_push_paths(self) -> None:
        trigger = self.parse("pr-go.yml")
        assert trigger is not None
        self.assertIsNone(trigger.branches)
        assert trigger.paths is not None
        self.assertIn("services/go/**", trigger.paths)
        self.assertIn(".golangci.yml", trigger.paths)

    def test_hardening_paths(self) -> None:
        trigger = self.parse("hardening.yml")
        assert trigger is not None
        assert trigger.paths is not None
        self.assertIn("packages/go/**", trigger.paths)
        self.assertNotIn("go.work.sum", trigger.paths)

    def test_inline_and_block_forms(self) -> None:
        self.assertEqual(
            ci_gate.parse_pull_request_trigger("w", "on: [push, pull_request]\n"),
            ci_gate.Trigger("w", None, None),
        )
        self.assertIsNone(ci_gate.parse_pull_request_trigger("w", "on: push\n"))
        text = "on:\n  pull_request:\n    branches: [main]\n    paths:\n      - 'a/**' # note\n"
        self.assertEqual(
            ci_gate.parse_pull_request_trigger("w", text),
            ci_gate.Trigger("w", ("main",), ("a/**",)),
        )

    def test_unsupported_forms_fail_closed(self) -> None:
        cases = {
            "paths-ignore": "on:\n  pull_request:\n    paths-ignore: ['docs/**']\n",
            "types": "on:\n  pull_request:\n    types: [opened]\n",
            "pull_request_target": "on:\n  pull_request_target:\n",
            "inline target": "on: [pull_request_target]\n",
            "flow mapping": "on:\n  pull_request: {branches: [main]}\n",
        }
        for label, text in cases.items():
            with self.subTest(case=label), self.assertRaises(ci_gate.UnsupportedTriggerError):
                ci_gate.parse_pull_request_trigger("w", text)


class ExpectedWorkflowsTest(unittest.TestCase):
    """Which workflows a PR starts, from the repository's real filters."""

    def expected(self, *changed: str, base: str = "main") -> list[str]:
        return ci_gate.expected_workflows(WORKFLOWS, base, list(changed))

    def test_docs_only_pr_expects_only_backend_foundation(self) -> None:
        self.assertEqual(self.expected("docs/phase15/READINESS_AUDIT.md"), [".github/workflows/ci.yml"])

    def test_go_change_expects_go_workflows(self) -> None:
        got = self.expected("services/go/media-relay/internal/app/media_relay.go")
        for workflow in ("ci.yml", "pr-go.yml", "hardening.yml"):
            self.assertIn(f".github/workflows/{workflow}", got)
        self.assertNotIn(".github/workflows/pr-python.yml", got)

    def test_go_work_sum_triggers_pr_go_but_not_hardening(self) -> None:
        got = self.expected("go.work.sum")
        self.assertIn(".github/workflows/pr-go.yml", got)
        self.assertNotIn(".github/workflows/hardening.yml", got)

    def test_base_branch_filter_applies(self) -> None:
        self.assertNotIn(".github/workflows/ci.yml", self.expected("docs/x.md", base="release/1"))

    def test_the_gate_never_waits_for_itself(self) -> None:
        self.assertNotIn(ci_gate.GATE_WORKFLOW, self.expected(".github/workflows/ci-gate.yml"))

    def test_unparseable_workflow_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            Path(tmp, "x.yml").write_text("on:\n  pull_request:\n    paths-ignore: [a]\n", encoding="utf-8")
            with self.assertRaises(ci_gate.UnsupportedTriggerError):
                ci_gate.expected_workflows(Path(tmp), "main", ["a"])


class EvaluateRunTest(unittest.TestCase):
    """Job-level verdicts, including the lint ratchet against main."""

    lint = "golangci-lint (packages/go/voice)"

    def test_successful_run_passes_without_jobs(self) -> None:
        self.assertTrue(ci_gate.evaluate_run("w", "success", [], None).ok)

    def test_baseline_lint_failure_is_advisory(self) -> None:
        jobs = [{"name": self.lint, "conclusion": "failure"}, {"name": "packages/go/voice", "conclusion": "success"}]
        verdict = ci_gate.evaluate_run("pr-go", "failure", jobs, {self.lint: "failure"})
        self.assertTrue(verdict.ok, verdict.lines)
        self.assertIn("baseline", "\n".join(verdict.lines))

    def test_lint_failure_that_passes_on_main_blocks(self) -> None:
        jobs = [{"name": self.lint, "conclusion": "failure"}]
        self.assertFalse(ci_gate.evaluate_run("pr-go", "failure", jobs, {self.lint: "success"}).ok)

    def test_lint_failure_with_no_main_counterpart_blocks(self) -> None:
        jobs = [{"name": "golangci-lint (packages/go/new)", "conclusion": "failure"}]
        self.assertFalse(ci_gate.evaluate_run("pr-go", "failure", jobs, {self.lint: "failure"}).ok)

    def test_non_lint_failure_blocks_even_with_lint_baseline(self) -> None:
        jobs = [{"name": self.lint, "conclusion": "failure"}, {"name": "Race detector (repeated)", "conclusion": "failure"}]
        self.assertFalse(ci_gate.evaluate_run("hardening", "failure", jobs, {self.lint: "failure"}).ok)

    def test_cancelled_and_timed_out_jobs_block(self) -> None:
        for result in ("cancelled", "timed_out", "action_required", None):
            with self.subTest(result=result):
                jobs = [{"name": "Coverage", "conclusion": result}]
                self.assertFalse(ci_gate.evaluate_run("hardening", "failure", jobs, None).ok)

    def test_skipped_and_neutral_jobs_pass(self) -> None:
        jobs = [{"name": "a", "conclusion": "skipped"}, {"name": "b", "conclusion": "neutral"}]
        self.assertTrue(ci_gate.evaluate_run("w", "neutral", jobs, None).ok)

    def test_failed_run_without_jobs_blocks(self) -> None:
        self.assertFalse(ci_gate.evaluate_run("w", "startup_failure", [], None).ok)


if __name__ == "__main__":
    unittest.main()
