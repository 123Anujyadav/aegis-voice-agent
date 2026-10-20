"""Offline tests for ci_gate: trigger parsing, path matching, lint comparison and job evaluation.

They read the repository's real workflow files, so a change to any workflow's
pull_request trigger that the gate cannot evaluate fails here first.
"""

from __future__ import annotations

from pathlib import Path
import tempfile
import unittest

import ci_gate

WORKFLOWS = Path(__file__).resolve().parents[2] / ".github" / "workflows"

# Lines in the exact shape GitHub Actions serves a golangci-lint job log:
# timestamp, the ##[error] annotation, then the issue (ANSI colour included).
MAIN_LOG = "\n".join([
    "2026-10-18T03:44:29.6426658Z ##[error]outbox.go:1:1: package-comments: should have a package comment (revive)",
    "2026-10-18T03:44:29.6430484Z ##[error]outbox.go:14:2: exported: exported var ErrOutboxClosed should have comment or be unexported (revive)",
    "2026-10-18T03:44:29.6430490Z \x1b[1m##[error]outbox.go:70:4: Error return value of `p.store.MarkFailed` is not checked (errcheck)\x1b[0m",
    "2026-10-18T03:44:29.6430500Z \t_ = p.store.MarkFailed(ctx, entry.ID, err.Error())",
    "2026-10-18T03:44:29.6430600Z level=info msg=\"3 issues\"",
])


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

    def test_the_gate_itself_has_an_unfiltered_pull_request_trigger(self) -> None:
        self.assertEqual(self.parse("ci-gate.yml"), ci_gate.Trigger(".github/workflows/ci-gate.yml", None, None))

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

    def test_workflow_only_change_expects_that_workflow(self) -> None:
        got = self.expected(".github/workflows/hardening.yml")
        self.assertIn(".github/workflows/hardening.yml", got)
        self.assertNotIn(".github/workflows/pr-go.yml", got)

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


class LintComparisonTest(unittest.TestCase):
    """Finding-level comparison of a PR's lint job with main's."""

    def test_findings_are_counted_per_file_and_linter(self) -> None:
        counts = ci_gate.lint_findings(MAIN_LOG)
        self.assertEqual(counts[("outbox.go", "revive")], 2)
        self.assertEqual(counts[("outbox.go", "errcheck")], 1)
        self.assertEqual(sum(counts.values()), 3)

    def test_same_findings_are_no_regression(self) -> None:
        self.assertEqual(ci_gate.lint_regressions(MAIN_LOG, MAIN_LOG), [])

    def test_fewer_findings_are_no_regression(self) -> None:
        pr = MAIN_LOG.replace("##[error]outbox.go:1:1:", "fixed:")
        self.assertEqual(ci_gate.lint_regressions(pr, MAIN_LOG), [])

    def test_line_shifts_and_relabelled_messages_are_no_regression(self) -> None:
        pr = MAIN_LOG.replace("outbox.go:14:2: exported: exported var", "outbox.go:19:2: var-naming: var")
        self.assertEqual(ci_gate.lint_regressions(pr, MAIN_LOG), [])

    def test_an_extra_finding_in_an_already_failing_module_is_a_regression(self) -> None:
        pr = MAIN_LOG + "\n2026-10-18T03:44:30.0Z ##[error]outbox.go:99:1: exported: exported func X should have comment (revive)"
        got = ci_gate.lint_regressions(pr, MAIN_LOG)
        self.assertEqual(len(got), 1)
        self.assertIn("outbox.go (revive): 3 finding(s), 2 on main", got[0])

    def test_a_new_file_or_linter_is_a_regression(self) -> None:
        pr = MAIN_LOG + "\n2026-10-18T03:44:30.0Z ##[error]new.go:1:1: Sentence should start with a capital letter (godot)"
        self.assertTrue(any("new.go (godot)" in r for r in ci_gate.lint_regressions(pr, MAIN_LOG)))

    def test_a_failed_job_with_no_parseable_finding_is_a_regression(self) -> None:
        pr = "2026-10-18T03:44:29Z level=error msg=\"Running error: context loading failed\""
        self.assertEqual(len(ci_gate.lint_regressions(pr, MAIN_LOG)), 1)


class EvaluateRunTest(unittest.TestCase):
    """Job-level verdicts, including the lint ratchet against main."""

    lint = "golangci-lint (packages/go/voice)"

    def test_successful_run_passes_without_jobs(self) -> None:
        self.assertTrue(ci_gate.evaluate_run("w", "success", [], None).ok)

    def test_baseline_lint_failure_without_new_findings_is_advisory(self) -> None:
        jobs = [{"name": self.lint, "conclusion": "failure"}, {"name": "packages/go/voice", "conclusion": "success"}]
        verdict = ci_gate.evaluate_run("pr-go", "failure", jobs, {self.lint: "failure"}, {self.lint: []})
        self.assertTrue(verdict.ok, verdict.lines)
        self.assertIn("baseline", "\n".join(verdict.lines))

    def test_baseline_lint_failure_with_new_findings_blocks(self) -> None:
        jobs = [{"name": self.lint, "conclusion": "failure"}]
        verdict = ci_gate.evaluate_run(
            "pr-go", "failure", jobs, {self.lint: "failure"}, {self.lint: ["x.go (godot): 2 finding(s), 1 on main"]}
        )
        self.assertFalse(verdict.ok)
        self.assertIn("new lint finding", "\n".join(verdict.lines))

    def test_baseline_lint_failure_that_was_not_compared_blocks(self) -> None:
        jobs = [{"name": self.lint, "conclusion": "failure"}]
        self.assertFalse(ci_gate.evaluate_run("pr-go", "failure", jobs, {self.lint: "failure"}, {}).ok)

    def test_lint_failure_that_passes_on_main_blocks(self) -> None:
        jobs = [{"name": self.lint, "conclusion": "failure"}]
        self.assertFalse(ci_gate.evaluate_run("pr-go", "failure", jobs, {self.lint: "success"}, {self.lint: []}).ok)

    def test_lint_failure_with_no_main_counterpart_blocks(self) -> None:
        jobs = [{"name": "golangci-lint (packages/go/new)", "conclusion": "failure"}]
        self.assertFalse(ci_gate.evaluate_run("pr-go", "failure", jobs, {self.lint: "failure"}, {}).ok)

    def test_lint_failure_with_no_main_run_blocks(self) -> None:
        jobs = [{"name": self.lint, "conclusion": "failure"}]
        self.assertFalse(ci_gate.evaluate_run("pr-go", "failure", jobs, None).ok)

    def test_non_lint_failure_blocks_even_with_lint_baseline(self) -> None:
        jobs = [{"name": self.lint, "conclusion": "failure"}, {"name": "Race detector (repeated)", "conclusion": "failure"}]
        self.assertFalse(
            ci_gate.evaluate_run("hardening", "failure", jobs, {self.lint: "failure"}, {self.lint: []}).ok
        )

    def test_cancelled_timed_out_and_unfinished_jobs_block(self) -> None:
        for result in ("cancelled", "timed_out", "action_required", "startup_failure", None):
            with self.subTest(result=result):
                jobs = [{"name": "Coverage", "conclusion": result}]
                self.assertFalse(ci_gate.evaluate_run("hardening", "failure", jobs, None).ok)

    def test_cancelled_lint_job_blocks_even_when_main_fails(self) -> None:
        jobs = [{"name": self.lint, "conclusion": "cancelled"}]
        self.assertFalse(ci_gate.evaluate_run("pr-go", "cancelled", jobs, {self.lint: "failure"}, {}).ok)

    def test_skipped_and_neutral_jobs_pass(self) -> None:
        jobs = [{"name": "a", "conclusion": "skipped"}, {"name": "b", "conclusion": "neutral"}]
        self.assertTrue(ci_gate.evaluate_run("w", "skipped", jobs, None).ok)

    def test_failed_run_without_jobs_blocks(self) -> None:
        self.assertFalse(ci_gate.evaluate_run("w", "startup_failure", [], None).ok)


class GateWorkflowTest(unittest.TestCase):
    """Structural guards on ci-gate.yml: the trust properties the gate relies on."""

    text = (WORKFLOWS / "ci-gate.yml").read_text(encoding="utf-8")

    def steps(self) -> list[str]:
        body = self.text.split("    steps:\n", 1)[1]
        return ["      - name:" + s for s in body.split("      - name:")[1:]]

    def test_trusted_checkout_is_the_base_branch_tip(self) -> None:
        first = self.steps()[0]
        self.assertIn("path: trusted", first)
        self.assertIn("ref: ${{ github.base_ref }}", first)
        self.assertNotIn("pull_request.base.sha", self.text.split("jobs:", 1)[1])

    def test_the_verdict_runs_before_any_pull_request_code(self) -> None:
        steps = self.steps()
        verdict = next(i for i, s in enumerate(steps) if "ci_gate.py" in s and "--pr" in s)
        runs_pr_code = [i for i, s in enumerate(steps) if "unittest discover -s pr/" in s]
        self.assertTrue(runs_pr_code)
        self.assertTrue(all(i > verdict for i in runs_pr_code), "PR code must run after the verdict")
        for step in steps[:verdict]:
            self.assertNotIn("run:", step, "only checkouts may precede the verdict")

    def test_the_pull_request_is_checked_out_early_only_for_bootstrap(self) -> None:
        steps = self.steps()
        verdict = next(i for i, s in enumerate(steps) if "ci_gate.py" in s and "--pr" in s)
        for step in steps[:verdict]:
            if "path: pr" in step:
                self.assertIn("hashFiles('trusted/tools/ci/ci_gate.py') == ''", step)

    def test_permissions_are_read_only(self) -> None:
        block = self.text.split("permissions:\n", 1)[1].split("\n\n", 1)[0]
        grants = dict(line.strip().split(": ") for line in block.splitlines() if line.strip())
        self.assertEqual(grants, {"actions": "read", "contents": "read", "pull-requests": "read"})
        self.assertNotIn("write", self.text.split("jobs:", 1)[1])

    def test_no_expression_is_interpolated_into_a_script(self) -> None:
        for step in self.steps():
            script = step.split("run: |", 1)[1] if "run: |" in step else ""
            self.assertNotIn("${{", script, step.splitlines()[0])

    def test_checkouts_do_not_persist_credentials(self) -> None:
        for step in self.steps():
            if "actions/checkout" in step:
                self.assertIn("persist-credentials: false", step)


class LatestAttemptTest(unittest.TestCase):
    """Jobs are listed with filter=all and reduced to each job's latest attempt."""

    def test_a_partially_rerun_job_is_taken_from_its_latest_attempt(self) -> None:
        rows = [
            {"id": 10, "name": "lint (a)", "status": "completed", "conclusion": "failure", "run_attempt": 1},
            {"id": 11, "name": "test (a)", "status": "completed", "conclusion": "success", "run_attempt": 1},
            {"id": 20, "name": "lint (a)", "status": "completed", "conclusion": "success", "run_attempt": 2},
        ]
        self.assertEqual(ci_gate._latest_attempt(rows), [
            {"id": 20, "name": "lint (a)", "status": "completed", "conclusion": "success"},
            {"id": 11, "name": "test (a)", "status": "completed", "conclusion": "success"},
        ])

    def test_a_still_running_rerun_is_reported_as_running(self) -> None:
        # The re-run attempt has not finished: the gate must see it as running,
        # not the earlier attempt's completed failure.
        rows = [
            {"id": 30, "name": "lint (b)", "status": "in_progress", "conclusion": None, "run_attempt": 2},
            {"id": 10, "name": "lint (b)", "status": "completed", "conclusion": "failure", "run_attempt": 1},
        ]
        self.assertEqual(ci_gate._latest_attempt(rows), [
            {"id": 30, "name": "lint (b)", "status": "in_progress", "conclusion": None},
        ])

    def test_a_single_attempt_listing_is_unchanged(self) -> None:
        rows = [
            {"id": 1, "name": "x", "status": "completed", "conclusion": "success", "run_attempt": 1},
            {"id": 2, "name": "y", "status": "completed", "conclusion": "failure", "run_attempt": 1},
        ]
        self.assertEqual(ci_gate._latest_attempt(rows),
                         [{k: v for k, v in r.items() if k != "run_attempt"} for r in rows])

    def test_jobs_lists_every_attempt_and_reduces_it(self) -> None:
        from unittest import mock
        calls: list[tuple[str, ...]] = []

        def fake_lines(*args: str) -> list[dict]:
            calls.append(args)
            return [
                {"id": 10, "name": "lint", "status": "completed", "conclusion": "failure", "run_attempt": 1},
                {"id": 20, "name": "lint", "status": "completed", "conclusion": "success", "run_attempt": 2},
            ]

        with mock.patch.object(ci_gate, "_gh_lines", side_effect=fake_lines):
            got = ci_gate.GitHub("o/r").jobs(7)
        self.assertEqual(got, [{"id": 20, "name": "lint", "status": "completed", "conclusion": "success"}])
        self.assertEqual(len(calls), 1)
        self.assertIn("actions/runs/7/jobs?filter=all&per_page=30", calls[0][0])
        self.assertIn("--paginate", calls[0])
        self.assertIn("run_attempt", calls[0][-1])


class OutputTest(unittest.TestCase):
    """The printed report cannot act as workflow commands."""

    def test_report_is_wrapped_in_stop_commands(self) -> None:
        import contextlib
        import io
        from unittest import mock

        out = io.StringIO()
        with mock.patch.object(ci_gate, "run_gate", return_value=(True, ["::error::injected name"])), \
                contextlib.redirect_stdout(out), mock.patch.dict("os.environ", {}, clear=False):
            ci_gate.main(["--repo", "o/r", "--pr", "1"])
        lines = out.getvalue().splitlines()
        self.assertTrue(lines[0].startswith("::stop-commands::"))
        token = lines[0].split("::")[2]
        self.assertEqual(lines[-1], f"::{token}::")
        self.assertIn("::error::injected name", lines[1:-1])


class FakeGitHub:
    """An in-memory stand-in for GitHub, for end-to-end gate simulations."""

    def __init__(self, changed: list[str], runs: dict[str, dict], jobs: dict[int, list[dict]],
                 logs: dict[int, str] | None = None, main: dict[str, list[dict]] | None = None) -> None:
        self._changed, self._runs, self._jobs = changed, runs, jobs
        self._logs, self._main = logs or {}, main or {}

    def pull(self, number: int) -> dict:
        return {"sha": "abc", "base": "main"}

    def changed_files(self, number: int) -> list[str]:
        return self._changed

    def runs(self, sha: str) -> dict:
        return self._runs

    def jobs(self, run_id: int) -> list[dict]:
        return self._jobs[run_id]

    def job_log(self, job_id: int) -> str:
        return self._logs[job_id]

    def main_jobs(self, workflow: str) -> list[dict] | None:
        return self._main.get(workflow)


class SequencedJobsGitHub(FakeGitHub):
    """A FakeGitHub whose job lists change between calls (a job finishing)."""

    def __init__(self, changed: list[str], runs: dict[str, dict], sequences: dict[int, list[list[dict]]]) -> None:
        super().__init__(changed, runs, {})
        self._sequences = sequences

    def jobs(self, run_id: int) -> list[dict]:
        seq = self._sequences[run_id]
        return seq.pop(0) if len(seq) > 1 else seq[0]


def _run(run_id: int, path: str, conclusion: str, status: str = "completed") -> dict:
    return {"id": run_id, "path": path, "status": status, "conclusion": conclusion, "created_at": "t", "run_attempt": 1}


class GateSimulationTest(unittest.TestCase):
    """The whole gate, against simulated PRs (no network)."""

    ci, pr_go = ".github/workflows/ci.yml", ".github/workflows/pr-go.yml"
    hardening = ".github/workflows/hardening.yml"
    lint = "golangci-lint (packages/go/outbox)"

    def gate(self, gh: FakeGitHub) -> tuple[bool, list[str]]:
        return ci_gate.run_gate(gh, 1, WORKFLOWS, wait=False, interval=0, appear_timeout=0, total_timeout=0)

    def go_pr(self, pr_log: str, ran_hardening: str = "success") -> FakeGitHub:
        return FakeGitHub(
            ["packages/go/outbox/outbox.go"],
            {self.ci: _run(1, self.ci, "success"), self.pr_go: _run(2, self.pr_go, "failure"),
             self.hardening: _run(3, self.hardening, ran_hardening)},
            {2: [{"id": 20, "name": self.lint, "conclusion": "failure"},
                 {"id": 21, "name": "packages/go/outbox", "conclusion": "success"}],
             3: [{"id": 30, "name": "Race detector (repeated)", "conclusion": "failure"}]},
            logs={20: pr_log, 90: MAIN_LOG},
            main={self.pr_go: [{"id": 90, "name": self.lint, "conclusion": "failure"}]},
        )

    def test_docs_only_pr_passes_on_backend_foundation_alone(self) -> None:
        gh = FakeGitHub(["docs/a.md"], {self.ci: _run(1, self.ci, "success")}, {})
        passed, report = self.gate(gh)
        self.assertTrue(passed, report)

    def test_docs_only_pr_fails_when_backend_foundation_fails(self) -> None:
        gh = FakeGitHub(["docs/a.md"], {self.ci: _run(1, self.ci, "failure")},
                        {1: [{"id": 10, "name": "Build & Test Backend Foundation", "conclusion": "failure"}]})
        self.assertFalse(self.gate(gh)[0])

    def test_go_pr_with_only_baseline_lint_passes(self) -> None:
        passed, report = self.gate(self.go_pr(MAIN_LOG))
        self.assertTrue(passed, report)

    def test_go_pr_adding_a_finding_to_a_failing_module_fails(self) -> None:
        pr_log = MAIN_LOG + "\n2026-10-18T03:44:30.0Z ##[error]outbox.go:99:1: exported: x (revive)"
        passed, report = self.gate(self.go_pr(pr_log))
        self.assertFalse(passed)
        self.assertIn("new lint finding", "\n".join(report))

    def test_go_pr_whose_hardening_fails_fails(self) -> None:
        self.assertFalse(self.gate(self.go_pr(MAIN_LOG, ran_hardening="failure"))[0])

    def test_expected_workflow_that_never_started_fails(self) -> None:
        gh = FakeGitHub(["packages/go/outbox/outbox.go"], {self.ci: _run(1, self.ci, "success")}, {})
        passed, report = self.gate(gh)
        self.assertFalse(passed)
        self.assertIn("never started", "\n".join(report))

    def test_unfinished_run_does_not_pass(self) -> None:
        gh = FakeGitHub(["docs/a.md"], {self.ci: _run(1, self.ci, None, status="in_progress")}, {})
        self.assertFalse(self.gate(gh)[0])

    def test_a_run_reported_completed_with_a_running_job_does_not_pass(self) -> None:
        # Seen on GitHub: pr-go reported completed/failure while a govulncheck
        # job was still in progress.
        gh = FakeGitHub(["docs/a.md"], {self.ci: _run(1, self.ci, "failure")},
                        {1: [{"id": 10, "name": "Build & Test Backend Foundation", "status": "in_progress", "conclusion": None}]})
        passed, report = self.gate(gh)
        self.assertFalse(passed)
        self.assertIn("a job is still running", "\n".join(report))

    def test_the_gate_waits_for_such_a_job_and_then_judges_it(self) -> None:
        gh = SequencedJobsGitHub(["docs/a.md"], {self.ci: _run(1, self.ci, "failure")}, {1: [
            [{"id": 10, "name": "Build & Test Backend Foundation", "status": "in_progress", "conclusion": None}],
            [{"id": 10, "name": "Build & Test Backend Foundation", "status": "completed", "conclusion": "failure"}],
        ]})
        passed, report = ci_gate.run_gate(gh, 1, WORKFLOWS, wait=True, interval=0, appear_timeout=5, total_timeout=5)
        self.assertFalse(passed, "the finished job failed, so the gate must fail")
        self.assertIn("Build & Test Backend Foundation: failure", "\n".join(report))

    def test_a_job_that_never_finishes_times_out(self) -> None:
        gh = FakeGitHub(["docs/a.md"], {self.ci: _run(1, self.ci, "failure")},
                        {1: [{"id": 10, "name": "x", "status": "in_progress", "conclusion": None}]})
        passed, report = ci_gate.run_gate(gh, 1, WORKFLOWS, wait=True, interval=0, appear_timeout=0, total_timeout=0)
        self.assertFalse(passed)
        self.assertIn("timed out waiting for", "\n".join(report))

    def test_an_unpredicted_run_is_judged_too(self) -> None:
        extra = ".github/workflows/new.yml"
        gh = FakeGitHub(["docs/a.md"], {self.ci: _run(1, self.ci, "success"), extra: _run(5, extra, "failure")},
                        {5: [{"id": 50, "name": "x", "conclusion": "failure"}]})
        self.assertFalse(self.gate(gh)[0])


if __name__ == "__main__":
    unittest.main()
