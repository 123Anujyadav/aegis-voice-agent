"""Aggregate CI gate: one check that always runs on a pull request.

pr-go, hardening and the other Go and Python workflows are path-filtered on
``pull_request``, so a docs-only or workflow-only PR never starts them. A
branch-protection rule cannot require a check that never starts: the PR waits
for it forever. This gate always runs, and reports one explicit pass or fail.

1. Work out which workflows this PR triggers. Each workflow's own
   ``on.pull_request`` branch and path filters are read from the workflow
   files, so nothing is duplicated here and nothing can drift. A trigger the
   parser does not understand fails the gate (fail closed); it is never
   silently assumed not to run.
2. Wait until every expected workflow run for the PR head has completed, and
   any other run that appeared for it.
3. Pass only if every job in every run succeeded, was skipped, or is neutral.
   The exception is an *advisory* job (golangci-lint) that also fails on
   ``main``'s latest completed run of the same workflow: the known lint
   baseline. Even then, the job's findings are counted per (file, linter) in
   both logs. The gate fails on any of:
   - a count higher than on ``main``;
   - a (file, linter) pair ``main`` does not have;
   - a failed lint job with no parseable finding.

   A lint job that passes on ``main`` but fails here, or that has no
   counterpart on ``main``, also fails the gate. So the baseline can only
   shrink.

The workflow runs this file, and reads the workflow filters, from the current
tip of the PR's **base branch**, never from the PR, and before any of the
PR's code runs. A PR therefore cannot change the verdict on itself by editing
this script or another workflow's filters.

It uses only the standard library and the ``gh`` CLI (preinstalled on
GitHub-hosted runners), and only reads: no third-party action, no secret
beyond the job's own read-only token.
"""

from __future__ import annotations

import argparse
from collections import Counter
from dataclasses import dataclass
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import sys
import time
from typing import Any

GATE_WORKFLOW = ".github/workflows/ci-gate.yml"
ADVISORY_JOB = re.compile(r"^golangci-lint \(")
OK_CONCLUSIONS = frozenset({"success", "skipped", "neutral"})
SUPPORTED_FILTERS = frozenset({"branches", "paths"})
# One golangci-lint issue line in a GitHub Actions job log, e.g.
#   2026-10-18T03:21:10.1234567Z ##[error]internal/app/x.go:12:3: message (linter)
LINT_FINDING = re.compile(
    r"^(?:\S+Z )?(?:##\[error\])?(?P<file>[^\s:]+\.go):\d+:\d+: .*\((?P<linter>[a-z0-9]+)\)\s*$"
)
GH_ATTEMPTS = 3
ANSI = re.compile(r"\x1b\[[0-9;]*[A-Za-z]")


class UnsupportedTriggerError(Exception):
    """A workflow trigger uses syntax this gate cannot evaluate safely."""


@dataclass(frozen=True)
class Trigger:
    """The ``pull_request`` trigger of one workflow file."""

    workflow: str
    branches: tuple[str, ...] | None
    paths: tuple[str, ...] | None


# --------------------------------------------------------------------------
# Filter patterns (GitHub's filter-pattern syntax, the subset used here)
# --------------------------------------------------------------------------


def _pattern_regex(pattern: str) -> re.Pattern[str]:
    """Translate one filter pattern into a regular expression.

    ``**`` matches any characters including ``/``; ``*`` matches any
    characters except ``/``; ``?`` matches one character except ``/``.
    Character classes and ``+`` are refused rather than guessed.
    """
    if any(c in pattern for c in "[]+"):
        msg = f"unsupported filter pattern {pattern!r}"
        raise UnsupportedTriggerError(msg)
    out: list[str] = []
    i = 0
    while i < len(pattern):
        if pattern.startswith("**", i):
            out.append(".*")
            i += 2
        elif pattern[i] == "*":
            out.append("[^/]*")
            i += 1
        elif pattern[i] == "?":
            out.append("[^/]")
            i += 1
        else:
            out.append(re.escape(pattern[i]))
            i += 1
    return re.compile("".join(out) + r"\Z")


def matches(patterns: tuple[str, ...], name: str) -> bool:
    """Report whether name passes the filter, applying ``!`` negations in order."""
    matched = False
    for pattern in patterns:
        if pattern.startswith("!"):
            if _pattern_regex(pattern[1:]).match(name):
                matched = False
        elif _pattern_regex(pattern).match(name):
            matched = True
    return matched


# --------------------------------------------------------------------------
# Workflow trigger parsing (deliberately small; fails closed)
# --------------------------------------------------------------------------


def _strip_comment(line: str) -> str:
    """Remove a trailing ``# comment`` that is outside quotes."""
    quote = ""
    for i, c in enumerate(line):
        if quote:
            if c == quote:
                quote = ""
        elif c in "\"'":
            quote = c
        elif c == "#" and (i == 0 or line[i - 1].isspace()):
            return line[:i].rstrip()
    return line.rstrip()


def _unquote(value: str) -> str:
    value = value.strip()
    if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
        return value[1:-1]
    return value


def _inline_list(value: str) -> tuple[str, ...]:
    inner = value.strip()[1:-1]
    return tuple(_unquote(v) for v in inner.split(",") if v.strip())


def _indent(line: str) -> int:
    return len(line) - len(line.lstrip(" "))


def parse_pull_request_trigger(workflow: str, text: str) -> Trigger | None:
    """Return a workflow's ``pull_request`` trigger, or None if it has none.

    Raises UnsupportedTriggerError for any form this parser does not handle,
    so that a workflow is never wrongly treated as not running.
    """
    lines = [_strip_comment(raw) for raw in text.splitlines()]
    lines = [ln for ln in lines if ln.strip()]
    on_index = None
    for i, line in enumerate(lines):
        if re.match(r"^(on|\"on\"|'on'):", line):
            on_index = i
            break
    if on_index is None:
        return None

    inline = lines[on_index].split(":", 1)[1].strip()
    if inline:
        events: tuple[str, ...]
        events = _inline_list(inline) if inline.startswith("[") else (_unquote(inline),)
        if "pull_request_target" in events:
            msg = f"{workflow}: pull_request_target is not supported"
            raise UnsupportedTriggerError(msg)
        if "pull_request" in events:
            return Trigger(workflow, None, None)
        return None

    block: list[str] = []
    for line in lines[on_index + 1 :]:
        if _indent(line) == 0:
            break
        block.append(line)
    if not block:
        return None
    child = _indent(block[0])

    for i, line in enumerate(block):
        if _indent(line) != child:
            continue
        key, _, value = line.strip().partition(":")
        if key == "pull_request_target":
            msg = f"{workflow}: pull_request_target is not supported"
            raise UnsupportedTriggerError(msg)
        if key != "pull_request":
            continue
        value = value.strip()
        if value in ("", "{}", "null", "~"):
            body = []
            for sub in block[i + 1 :]:
                if _indent(sub) <= child:
                    break
                body.append(sub)
            if value in ("{}", "null", "~") and body:
                msg = f"{workflow}: malformed pull_request trigger"
                raise UnsupportedTriggerError(msg)
            return _parse_filters(workflow, body)
        msg = f"{workflow}: unsupported pull_request value {value!r}"
        raise UnsupportedTriggerError(msg)
    return None


def _parse_filters(workflow: str, body: list[str]) -> Trigger:
    filters: dict[str, tuple[str, ...]] = {}
    if not body:
        return Trigger(workflow, None, None)
    level = _indent(body[0])
    i = 0
    while i < len(body):
        line = body[i]
        if _indent(line) != level:
            msg = f"{workflow}: unexpected indentation in pull_request filters"
            raise UnsupportedTriggerError(msg)
        key, _, value = line.strip().partition(":")
        if key not in SUPPORTED_FILTERS:
            msg = f"{workflow}: pull_request filter {key!r} is not supported"
            raise UnsupportedTriggerError(msg)
        value = value.strip()
        i += 1
        if value.startswith("["):
            filters[key] = _inline_list(value)
            continue
        if value:
            filters[key] = (_unquote(value),)
            continue
        items: list[str] = []
        while i < len(body) and _indent(body[i]) > level:
            item = body[i].strip()
            if not item.startswith("- "):
                msg = f"{workflow}: unsupported {key} list item {item!r}"
                raise UnsupportedTriggerError(msg)
            items.append(_unquote(item[2:]))
            i += 1
        filters[key] = tuple(items)
    return Trigger(workflow, filters.get("branches"), filters.get("paths"))


def expected_workflows(
    workflows_dir: Path, base_ref: str, changed: list[str]
) -> list[str]:
    """List the workflow files a PR into base_ref changing changed would start."""
    expected = []
    for path in sorted([*workflows_dir.glob("*.yml"), *workflows_dir.glob("*.yaml")]):
        rel = f".github/workflows/{path.name}"
        if rel == GATE_WORKFLOW:
            continue
        trigger = parse_pull_request_trigger(rel, path.read_text(encoding="utf-8"))
        if trigger is None:
            continue
        if trigger.branches is not None and not matches(trigger.branches, base_ref):
            continue
        if trigger.paths is not None and not any(matches(trigger.paths, f) for f in changed):
            continue
        expected.append(rel)
    return expected


# --------------------------------------------------------------------------
# Lint comparison against main
# --------------------------------------------------------------------------


def lint_findings(log: str) -> Counter[tuple[str, str]]:
    """Count golangci-lint findings in a job log per (file, linter)."""
    counts: Counter[tuple[str, str]] = Counter()
    for line in log.splitlines():
        m = LINT_FINDING.match(ANSI.sub("", line).strip())
        if m:
            counts[(m["file"], m["linter"])] += 1
    return counts


def lint_regressions(pr_log: str, main_log: str) -> list[str]:
    """List what a PR's lint job reports beyond main's run of the same job.

    Counting per (file, linter), not per message, keeps the comparison stable
    across line shifts, and across linter output that labels the same issue
    differently from run to run. The limit: a PR that fixes one finding and
    adds another of the same linter in the same file nets to zero.
    """
    pr_counts, main_counts = lint_findings(pr_log), lint_findings(main_log)
    if not pr_counts:
        return ["the lint job failed but reported no parseable finding"]
    return [
        f"{file} ({linter}): {n} finding(s), {main_counts.get((file, linter), 0)} on main"
        for (file, linter), n in sorted(pr_counts.items())
        if n > main_counts.get((file, linter), 0)
    ]


# --------------------------------------------------------------------------
# Evaluation
# --------------------------------------------------------------------------


@dataclass(frozen=True)
class Verdict:
    """The outcome for one workflow run."""

    workflow: str
    ok: bool
    lines: tuple[str, ...]


def evaluate_run(
    workflow: str,
    conclusion: str | None,
    jobs: list[dict[str, Any]],
    main_jobs: dict[str, str] | None,
    regressions: dict[str, list[str]] | None = None,
) -> Verdict:
    """Judge one completed run from its jobs and main's baseline for advisory jobs.

    main_jobs maps job names to their conclusion on main's latest completed
    run. regressions maps an advisory job's name to the findings it reports
    beyond main (lint_regressions). An advisory job that also fails on main
    is advisory only when its regressions entry exists and is empty.
    """
    if conclusion == "success":
        return Verdict(workflow, ok=True, lines=(f"{workflow}: success",))
    bad: list[str] = []
    baseline: list[str] = []
    for job in jobs:
        name, result = job["name"], job.get("conclusion") or "incomplete"
        if result in OK_CONCLUSIONS:
            continue
        if ADVISORY_JOB.match(name) and result == "failure":
            on_main = (main_jobs or {}).get(name)
            if on_main == "failure":
                extra = (regressions or {}).get(name)
                if extra is None:
                    bad.append(f"{name}: failure (not compared with main)")
                elif extra:
                    bad += [f"{name}: new lint finding(s): {e}" for e in extra]
                else:
                    baseline.append(name)
                continue
            why = "passes on main" if on_main else "has no counterpart on main"
            bad.append(f"{name}: {result} (new lint failure: {why})")
            continue
        bad.append(f"{name}: {result}")
    if not jobs:
        bad.append(f"run concluded {conclusion!r} with no jobs to inspect")
    lines = [f"{workflow}: {'FAIL' if bad else 'pass'} (run conclusion {conclusion})"]
    lines += [f"  blocking: {b}" for b in bad]
    if baseline:
        lines.append(
            f"  advisory, failing on main too with no new findings (baseline): {len(baseline)} job(s)"
        )
    return Verdict(workflow, ok=not bad, lines=tuple(lines))


# --------------------------------------------------------------------------
# GitHub access through the gh CLI
# --------------------------------------------------------------------------


def _gh_text(*args: str) -> str:
    """Run ``gh api`` with retries; a persistent failure raises (fail closed)."""
    cmd = ["gh", "api", *args]
    for attempt in range(1, GH_ATTEMPTS + 1):
        proc = subprocess.run(cmd, capture_output=True, text=True, check=False)
        if proc.returncode == 0:
            return proc.stdout
        if attempt == GH_ATTEMPTS:
            raise subprocess.CalledProcessError(proc.returncode, cmd, proc.stdout, proc.stderr)
        time.sleep(5 * attempt)
    msg = "unreachable"
    raise AssertionError(msg)


def _gh_lines(*args: str) -> list[dict[str, Any]]:
    return [json.loads(line) for line in _gh_text(*args).splitlines() if line.strip()]


def _latest_attempt(rows: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Keep every row of each job name's highest run attempt, names in first-seen order.

    After a single job of a large run is re-run, GitHub's jobs listing for
    that run slows down with page depth; at 100 jobs per page, its "latest
    attempt" listing returned HTTP 502 on every request, so the gate failed
    closed on a run whose jobs had all completed (pr-go run 39363295786, 137
    jobs). Every attempt is therefore listed (``filter=all``) in small pages
    and reduced here to the same set of jobs, with the same fields, as the
    latest-attempt listing. Small pages are a mitigation measured on that run,
    not a guarantee; a persistent API failure still fails closed.
    """
    by_name: dict[str, list[dict[str, Any]]] = {}
    for row in rows:
        by_name.setdefault(row["name"], []).append(row)
    kept = []
    for group in by_name.values():
        top = max(row.get("run_attempt", 1) for row in group)
        kept += [row for row in group if row.get("run_attempt", 1) == top]
    return [{k: v for k, v in row.items() if k != "run_attempt"} for row in kept]


class GitHub:
    """The read-only GitHub queries the gate needs."""

    def __init__(self, repo: str) -> None:
        """Bind the client to owner/repo."""
        self.repo = repo

    def pull(self, number: int) -> dict[str, Any]:
        """Return the pull request's head SHA and base ref."""
        return _gh_lines(
            f"repos/{self.repo}/pulls/{number}",
            "--jq", "{sha: .head.sha, base: .base.ref}",
        )[0]

    def changed_files(self, number: int) -> list[str]:
        """List the files the pull request changes (renames: both names)."""
        rows = _gh_lines(
            f"repos/{self.repo}/pulls/{number}/files?per_page=100",
            "--paginate", "--jq", ".[] | {f: .filename, p: (.previous_filename // \"\")}",
        )
        names = [r["f"] for r in rows] + [r["p"] for r in rows if r["p"]]
        return sorted(set(names))

    def runs(self, sha: str) -> dict[str, dict[str, Any]]:
        """Return the latest pull_request run per workflow file for sha."""
        rows = _gh_lines(
            f"repos/{self.repo}/actions/runs?head_sha={sha}&event=pull_request&per_page=100",
            "--paginate",
            "--jq", ".workflow_runs[] | {id, path, status, conclusion, created_at, run_attempt}",
        )
        latest: dict[str, dict[str, Any]] = {}
        for row in rows:
            path = row["path"].split("@", 1)[0]
            key = (row["created_at"], row["run_attempt"])
            if path not in latest or key > (latest[path]["created_at"], latest[path]["run_attempt"]):
                latest[path] = row
        return latest

    def jobs(self, run_id: int) -> list[dict[str, Any]]:
        """Return the jobs of the latest attempt of one run."""
        return _latest_attempt(_gh_lines(
            f"repos/{self.repo}/actions/runs/{run_id}/jobs?filter=all&per_page=30",
            "--paginate", "--jq", ".jobs[] | {id, name, status, conclusion, run_attempt}",
        ))

    def job_log(self, job_id: int) -> str:
        """Return one job's log text."""
        # Job logs carry ANSI colour codes; gh refuses to print them unless asked,
        # and lint_findings strips them before parsing.
        return _gh_text("--allow-escape-sequences", f"repos/{self.repo}/actions/jobs/{job_id}/logs")

    def main_jobs(self, workflow: str) -> list[dict[str, Any]] | None:
        """Return the jobs of main's latest completed push run of workflow."""
        name = workflow.rsplit("/", 1)[-1]
        rows = _gh_lines(
            f"repos/{self.repo}/actions/workflows/{name}/runs"
            "?branch=main&event=push&status=completed&per_page=1",
            "--jq", ".workflow_runs[] | {id}",
        )
        if not rows:
            return None
        return self.jobs(rows[0]["id"])


# --------------------------------------------------------------------------
# Main loop
# --------------------------------------------------------------------------


def _judge(gh: GitHub, workflow: str, run: dict[str, Any]) -> Verdict:
    """Fetch what one completed run needs and evaluate it."""
    if run["conclusion"] == "success":
        return evaluate_run(workflow, "success", [], None)
    jobs = gh.jobs(run["id"])
    advisory_failures = [
        j for j in jobs if ADVISORY_JOB.match(j["name"]) and j.get("conclusion") == "failure"
    ]
    if not advisory_failures:
        return evaluate_run(workflow, run["conclusion"], jobs, None)
    main = {j["name"]: j for j in gh.main_jobs(workflow) or []}
    regressions = {
        j["name"]: lint_regressions(gh.job_log(j["id"]), gh.job_log(main[j["name"]]["id"]))
        for j in advisory_failures
        if main.get(j["name"], {}).get("conclusion") == "failure"
    }
    conclusions = {name: j["conclusion"] for name, j in main.items()}
    return evaluate_run(workflow, run["conclusion"], jobs, conclusions, regressions)


def _runs_with_incomplete_jobs(gh: GitHub, runs: dict[str, dict[str, Any]]) -> list[str]:
    """List completed, non-successful runs that still have an unfinished job.

    GitHub can report a run as completed while one of its jobs is still in
    progress. Judging such a run now would fail on a job that has not
    finished, so it is treated as still running, and the wait (and its
    timeout) applies to it as to any other run.
    """
    stale = []
    for workflow, run in sorted(runs.items()):
        if run["conclusion"] == "success":
            continue
        if any(j.get("status", "completed") != "completed" for j in gh.jobs(run["id"])):
            stale.append(f"{workflow} (reported completed, but a job is still running)")
    return stale


def run_gate(  # noqa: PLR0913 - every argument is an independent knob
    gh: GitHub,
    pr: int,
    workflows_dir: Path,
    *,
    wait: bool,
    interval: float,
    appear_timeout: float,
    total_timeout: float,
) -> tuple[bool, list[str]]:
    """Run the gate once or until completion, and return (passed, report lines)."""
    info = gh.pull(pr)
    changed = gh.changed_files(pr)
    expected = expected_workflows(workflows_dir, info["base"], changed)
    report = [
        f"head {info['sha']} into {info['base']}; {len(changed)} changed file(s)",
        f"workflow filters read from: {workflows_dir}",
        "expected workflows: " + (", ".join(expected) or "none"),
    ]
    start = time.monotonic()
    while True:
        runs = {p: r for p, r in gh.runs(info["sha"]).items() if p != GATE_WORKFLOW}
        missing = [w for w in expected if w not in runs]
        unfinished = sorted(w for w, r in runs.items() if r["status"] != "completed")
        elapsed = time.monotonic() - start
        if missing and (not wait or elapsed > appear_timeout):
            report += [f"expected workflow never started: {w}" for w in missing]
            return False, report
        if not missing and not unfinished:
            unfinished = _runs_with_incomplete_jobs(gh, runs)
            if not unfinished:
                break
        if not wait:
            report += [f"still running: {w}" for w in unfinished]
            return False, report
        if elapsed > total_timeout:
            report += [f"timed out waiting for: {w}" for w in [*missing, *unfinished]]
            return False, report
        time.sleep(interval)

    verdicts = []
    for workflow in sorted(runs):
        if workflow not in expected:
            report.append(f"note: {workflow} ran although its base filters predicted it would not")
        verdicts.append(_judge(gh, workflow, runs[workflow]))
    for v in verdicts:
        report += list(v.lines)
    return all(v.ok for v in verdicts), report


def main(argv: list[str] | None = None) -> int:
    """Command-line entry point."""
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--repo", default=os.environ.get("GITHUB_REPOSITORY", ""))
    parser.add_argument("--pr", type=int, required=True)
    parser.add_argument("--workflows-dir", type=Path, default=Path(".github/workflows"))
    parser.add_argument("--no-wait", action="store_true", help="evaluate once; do not poll")
    parser.add_argument("--interval", type=float, default=30.0)
    parser.add_argument("--appear-timeout", type=float, default=600.0)
    parser.add_argument("--total-timeout", type=float, default=6600.0)
    args = parser.parse_args(argv)

    try:
        passed, report = run_gate(
            GitHub(args.repo),
            args.pr,
            args.workflows_dir,
            wait=not args.no_wait,
            interval=args.interval,
            appear_timeout=args.appear_timeout,
            total_timeout=args.total_timeout,
        )
    except (UnsupportedTriggerError, subprocess.CalledProcessError, OSError, ValueError) as exc:
        passed, report = False, [f"gate error (failing closed): {type(exc).__name__}: {exc}"]
    report.append("ci-gate: " + ("PASS" if passed else "FAIL"))
    text = "\n".join(report)
    # The report repeats names a pull request controls (its job names, its
    # workflow file names). Pausing workflow-command processing while it is
    # printed stops such a name from acting as a ::command:: on the runner.
    token = secrets.token_hex(16)
    sys.stdout.write(f"::stop-commands::{token}\n{text}\n::{token}::\n")
    summary = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary:
        with Path(summary).open("a", encoding="utf-8") as fh:
            fh.write("### ci-gate\n\n```\n" + text + "\n```\n")
    return 0 if passed else 1


if __name__ == "__main__":
    sys.exit(main())
