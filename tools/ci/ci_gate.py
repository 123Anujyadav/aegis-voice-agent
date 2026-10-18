"""Aggregate CI gate: one check that always runs on a pull request.

pr-go, hardening and the other Go and Python workflows are path-filtered on
``pull_request``, so a docs-only or workflow-only PR never starts them. A
branch-protection rule cannot require a check that never starts: the PR waits
for it forever. This gate always runs, and reports one explicit pass or fail.

1. Work out which workflows this PR triggers. Each workflow's own
   ``on.pull_request`` branch and path filters are read from the checked-out
   files, so nothing is duplicated here and nothing can drift. A trigger the
   parser does not understand fails the gate (fail closed); it is never
   silently assumed not to run.
2. Wait until every expected workflow run for the PR head has completed.
3. Pass only if every job in every run succeeded, was skipped, or is neutral.
   The exception is an *advisory* job (golangci-lint) that also fails on
   ``main``'s latest completed run of the same workflow: that is the known
   lint baseline, and it is reported, not hidden. A lint job that passes on
   ``main`` but fails here, or that has no counterpart on ``main``, fails the
   gate. So the baseline can only shrink.

It uses only the standard library and the ``gh`` CLI (preinstalled on
GitHub-hosted runners), and only reads: no third-party action, no secret
beyond the job's own read-only token.
"""

from __future__ import annotations

import argparse
from dataclasses import dataclass
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time
from typing import Any

GATE_WORKFLOW = ".github/workflows/ci-gate.yml"
ADVISORY_JOB = re.compile(r"^golangci-lint \(")
OK_CONCLUSIONS = frozenset({"success", "skipped", "neutral"})
SUPPORTED_FILTERS = frozenset({"branches", "paths"})


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
) -> Verdict:
    """Judge one completed run from its jobs and main's baseline for advisory jobs."""
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
        lines.append(f"  advisory, failing on main too (baseline): {len(baseline)} job(s)")
    return Verdict(workflow, ok=not bad, lines=tuple(lines))


# --------------------------------------------------------------------------
# GitHub access through the gh CLI
# --------------------------------------------------------------------------


def _gh_lines(*args: str) -> list[dict[str, Any]]:
    out = subprocess.run(
        ["gh", "api", *args], check=True, capture_output=True, text=True
    ).stdout
    return [json.loads(line) for line in out.splitlines() if line.strip()]


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
        return _gh_lines(
            f"repos/{self.repo}/actions/runs/{run_id}/jobs?per_page=100",
            "--paginate", "--jq", ".jobs[] | {name, conclusion}",
        )

    def main_jobs(self, workflow: str) -> dict[str, str] | None:
        """Return job conclusions of main's latest completed push run of workflow."""
        name = workflow.rsplit("/", 1)[-1]
        rows = _gh_lines(
            f"repos/{self.repo}/actions/workflows/{name}/runs"
            "?branch=main&event=push&status=completed&per_page=1",
            "--jq", ".workflow_runs[] | {id}",
        )
        if not rows:
            return None
        return {j["name"]: j["conclusion"] for j in self.jobs(rows[0]["id"])}


# --------------------------------------------------------------------------
# Main loop
# --------------------------------------------------------------------------


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
        run = runs[workflow]
        jobs = [] if run["conclusion"] == "success" else gh.jobs(run["id"])
        needs_main = any(
            ADVISORY_JOB.match(j["name"]) and j.get("conclusion") == "failure" for j in jobs
        )
        main = gh.main_jobs(workflow) if needs_main else None
        verdicts.append(evaluate_run(workflow, run["conclusion"], jobs, main))
        if workflow not in expected:
            report.append(f"note: {workflow} ran although its filters predicted it would not")
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

    passed, report = run_gate(
        GitHub(args.repo),
        args.pr,
        args.workflows_dir,
        wait=not args.no_wait,
        interval=args.interval,
        appear_timeout=args.appear_timeout,
        total_timeout=args.total_timeout,
    )
    report.append("ci-gate: " + ("PASS" if passed else "FAIL"))
    text = "\n".join(report)
    sys.stdout.write(text + "\n")
    summary = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary:
        with Path(summary).open("a", encoding="utf-8") as fh:
            fh.write("### ci-gate\n\n```\n" + text + "\n```\n")
    return 0 if passed else 1


if __name__ == "__main__":
    sys.exit(main())
