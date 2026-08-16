"""Offline analysis of Phase 11D audio intelligence evaluation results.

Phase 11D · Python 3.12 · offline only

This script computes confusion matrices, threshold sweeps and latency
percentiles from JSON the Go evaluation harness emitted. It contains no
detection logic and never sees a sample.

§23 of the phase brief allows Python for offline evaluation tooling and forbids
moving production runtime logic into it. The boundary matters for a reason
beyond compliance: a Python reimplementation of the detector would drift from
the Go one, and the evaluation would then describe something that never ran on
a call.
"""

from __future__ import annotations

import argparse
from collections.abc import Iterable, Sequence
from dataclasses import dataclass, field
import json
import math
from pathlib import Path
import sys

# ---------------------------------------------------------------------------
# The data contract
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class ScenarioResult:
    """One scenario run, as the Go harness reported it.

    Scalars only. No audio, no per-frame levels, nothing that could be
    reassembled into a recording — the same rule the event model follows,
    applied here so a corpus of JSON left on a laptop is not an incident.
    """

    scenario: str
    language: str = ""

    frames: int = 0
    expected_speech_frames: int = 0
    detected_speech_frames: int = 0

    onsets: int = 0
    expected_onsets: int = 0
    endpoints: int = 0
    expected_endpoints: int = 0

    endpoint_silence_ms: float = 0.0
    barge_in_latency_us: float = 0.0
    false_triggers: int = 0
    quality: str = ""

    @classmethod
    def from_json(cls, raw: dict[str, object]) -> ScenarioResult:
        """Build a result from one decoded JSON object.

        Unknown keys are ignored rather than rejected: the Go harness may add a
        field before this script learns about it, and failing the whole
        evaluation over a field nobody asked for would be worse than skipping
        it.
        """
        known = set(cls.__annotations__)
        return cls(**{k: v for k, v in raw.items() if k in known})  # type: ignore[arg-type]


# ---------------------------------------------------------------------------
# Confusion matrix
# ---------------------------------------------------------------------------


@dataclass(slots=True)
class Confusion:
    """A two-by-two confusion matrix over frame-level speech decisions."""

    true_positive: int = 0
    false_positive: int = 0
    true_negative: int = 0
    false_negative: int = 0

    @property
    def total(self) -> int:
        """Return how many frame decisions the matrix covers."""
        return (
            self.true_positive
            + self.false_positive
            + self.true_negative
            + self.false_negative
        )

    @property
    def precision(self) -> float:
        """Of the frames called speech, how many were.

        Returns 0.0 when nothing was called speech. Not 1.0: a detector that
        never fires has not achieved perfect precision, it has abstained, and
        reporting 1.0 would make the do-nothing detector look best.
        """
        denominator = self.true_positive + self.false_positive
        return self.true_positive / denominator if denominator else 0.0

    @property
    def recall(self) -> float:
        """Of the frames that were speech, how many were called speech."""
        denominator = self.true_positive + self.false_negative
        return self.true_positive / denominator if denominator else 0.0

    @property
    def f1(self) -> float:
        """The harmonic mean of precision and recall."""
        p, r = self.precision, self.recall
        return 2 * p * r / (p + r) if (p + r) else 0.0

    @property
    def false_positive_rate(self) -> float:
        """Of the frames that were NOT speech, how many were called speech.

        The number to watch when tuning: ADR-0011 §7 records that endpointing
        is tuned by measuring false-endpoint rate rather than by minimising
        latency, and this is its frame-level equivalent.
        """
        denominator = self.false_positive + self.true_negative
        return self.false_positive / denominator if denominator else 0.0

    def add(self, expected: int, detected: int, frames: int) -> None:
        """Fold one scenario's frame counts in.

        Approximate by construction, and worth being explicit about: the Go
        harness reports COUNTS, not per-frame labels, so this cannot tell WHICH
        frames were misclassified. It bounds the counts, which is enough for a
        threshold sweep and not enough for anything finer.
        """
        hit = min(expected, detected)
        self.true_positive += hit
        self.false_negative += max(0, expected - detected)
        self.false_positive += max(0, detected - expected)
        self.true_negative += max(0, frames - expected - max(0, detected - expected))

    def rows(self) -> list[tuple[str, str]]:
        """Render the matrix as label/value pairs for a table."""
        return [
            ("true positive", f"{self.true_positive}"),
            ("false positive", f"{self.false_positive}"),
            ("true negative", f"{self.true_negative}"),
            ("false negative", f"{self.false_negative}"),
            ("precision", f"{self.precision:.4f}"),
            ("recall", f"{self.recall:.4f}"),
            ("F1", f"{self.f1:.4f}"),
            ("false positive rate", f"{self.false_positive_rate:.4f}"),
        ]


# ---------------------------------------------------------------------------
# Latency
# ---------------------------------------------------------------------------


@dataclass(slots=True)
class LatencySummary:
    """Percentiles over a set of measurements."""

    label: str
    unit: str
    samples: list[float] = field(default_factory=list)

    def percentile(self, q: float) -> float:
        """Return the q-th percentile by nearest rank.

        Nearest rank rather than interpolation: an interpolated percentile
        reports a value that was never measured, and on a set of twenty
        synthetic runs that is a fabrication dressed as precision.
        """
        if not self.samples:
            return 0.0
        ordered = sorted(self.samples)
        rank = max(1, math.ceil(q / 100 * len(ordered)))
        return ordered[min(rank, len(ordered)) - 1]

    def rows(self) -> list[tuple[str, str]]:
        """Render the percentiles as label/value pairs for a table."""
        if not self.samples:
            return [("samples", "0")]
        return [
            ("samples", f"{len(self.samples)}"),
            ("min", f"{min(self.samples):.1f} {self.unit}"),
            ("p50", f"{self.percentile(50):.1f} {self.unit}"),
            ("p95", f"{self.percentile(95):.1f} {self.unit}"),
            ("p99", f"{self.percentile(99):.1f} {self.unit}"),
            ("max", f"{max(self.samples):.1f} {self.unit}"),
        ]


# The two budgets this phase is measured against. Frozen elsewhere and restated
# here so a report can compare against the document rather than a number
# somebody retyped.
ENDPOINT_BUDGET_P50_MS = 250.0  # ADR-0011 §5.2 hop 1
ENDPOINT_BUDGET_P95_MS = 350.0  # ADR-0011 §5.2 hop 1
BARGE_IN_BUDGET_US = 20_000.0  # ADR-0004 §12, one frame interval


# ---------------------------------------------------------------------------
# Analysis
# ---------------------------------------------------------------------------


def load(path: Path) -> list[ScenarioResult]:
    """Read scenario results from a JSON file.

    Accepts either a list of objects or one object per line, because both are
    natural things for a Go harness to emit and guessing wrong should not be a
    failure a human has to debug.
    """
    text = path.read_text(encoding="utf-8").strip()
    if not text:
        return []

    if text.startswith("["):
        return [ScenarioResult.from_json(o) for o in json.loads(text)]

    return [
        ScenarioResult.from_json(json.loads(line))
        for line in text.splitlines()
        if line.strip()
    ]


def confusion_over(results: Iterable[ScenarioResult]) -> Confusion:
    """Aggregate frame-level decisions across every result into one matrix."""
    matrix = Confusion()
    for r in results:
        matrix.add(r.expected_speech_frames, r.detected_speech_frames, r.frames)
    return matrix


def by_language(results: Sequence[ScenarioResult]) -> dict[str, list[ScenarioResult]]:
    """Group results by language tag.

    Phase 11D's detectors are language agnostic — they count milliseconds and
    compare decibels — so the interesting question is whether the measured
    behaviour DIFFERS by language. A material difference means the thresholds
    suit one rhythm better than another, which is a tuning finding rather than
    a bug.
    """
    grouped: dict[str, list[ScenarioResult]] = {}
    for r in results:
        grouped.setdefault(r.language or "unspecified", []).append(r)
    return grouped


def sweep(
    results: Sequence[ScenarioResult], thresholds: Sequence[float]
) -> list[tuple[float, Confusion]]:
    """Report the confusion matrix at each of several detection thresholds.

    The Go harness must have been run once per threshold and its results tagged
    accordingly; this only aggregates. Computing a sweep here would mean
    reimplementing the detector, which is exactly what §23 forbids.
    """
    out: list[tuple[float, Confusion]] = []
    for t in thresholds:
        tagged = [r for r in results if r.scenario.endswith(f"@{t}")]
        out.append((t, confusion_over(tagged)))
    return out


# ---------------------------------------------------------------------------
# Rendering
# ---------------------------------------------------------------------------


def table(title: str, rows: Sequence[tuple[str, str]]) -> str:
    """Render label/value pairs as a Markdown table."""
    if not rows:
        return f"### {title}\n\n_No data._\n"

    width = max(len(k) for k, _ in rows)
    lines = [f"### {title}", "", "| Metric | Value |", "|---|---|"]
    lines += [f"| {k.ljust(width)} | {v} |" for k, v in rows]
    return "\n".join(lines) + "\n"


def report(results: Sequence[ScenarioResult]) -> str:
    """Render the whole analysis as Markdown."""
    if not results:
        return "_No results._\n"

    sections = [
        f"# Audio intelligence evaluation\n\n{len(results)} scenario runs.\n",
        table("Frame-level speech detection", confusion_over(results).rows()),
    ]

    endpoint = LatencySummary("endpoint", "ms")
    barge_in = LatencySummary("barge-in", "us")
    for r in results:
        if r.endpoint_silence_ms:
            endpoint.samples.append(r.endpoint_silence_ms)
        if r.barge_in_latency_us:
            barge_in.samples.append(r.barge_in_latency_us)

    sections.append(table("Endpoint silence held", endpoint.rows()))
    if endpoint.samples:
        p50, p95 = endpoint.percentile(50), endpoint.percentile(95)
        sections.append(
            f"Against ADR-0011 §5.2 hop 1 "
            f"({ENDPOINT_BUDGET_P50_MS:.0f} ms p50 / {ENDPOINT_BUDGET_P95_MS:.0f} ms p95): "
            f"p50 {'within' if p50 <= ENDPOINT_BUDGET_P50_MS else 'ABOVE'} budget, "
            f"p95 {'within' if p95 <= ENDPOINT_BUDGET_P95_MS else 'ABOVE'} budget.\n"
        )

    sections.append(table("Barge-in orchestration latency", barge_in.rows()))
    if barge_in.samples:
        p95 = barge_in.percentile(95)
        sections.append(
            f"Against ADR-0004 §12 ({BARGE_IN_BUDGET_US / 1000:.0f} ms): "
            f"p95 {'within' if p95 <= BARGE_IN_BUDGET_US else 'ABOVE'} budget.\n"
        )

    for language, group in sorted(by_language(results).items()):
        sections.append(
            table(f"Frame-level detection — {language}", confusion_over(group).rows())
        )

    sections.append(
        "\n---\n\n"
        "**These results describe synthetic fixtures.** They validate timing "
        "behaviour and metadata propagation, not speech recognition accuracy in "
        "any language. Recognition accuracy belongs to Phase 11C's evaluation.\n"
    )

    return "\n".join(sections)


# ---------------------------------------------------------------------------
# Self-check
# ---------------------------------------------------------------------------


def self_check() -> int:
    """Verify this script's own arithmetic.

    Runs with no input, which is what CI does: there is no committed corpus,
    because there is no committed audio. Without this the script would be
    untested code that only ever runs by hand on a laptop.
    """
    failures: list[str] = []

    def check(name: str, got: object, want: object) -> None:
        if got != want:
            failures.append(f"{name}: got {got!r}, want {want!r}")

    # A perfect detector.
    perfect = Confusion()
    perfect.add(expected=40, detected=40, frames=100)
    check("perfect precision", perfect.precision, 1.0)
    check("perfect recall", perfect.recall, 1.0)
    check("perfect false positive rate", perfect.false_positive_rate, 0.0)

    # One that never fires. Precision must be 0.0, NOT 1.0 — abstaining is not
    # perfect precision, and reporting it as such would rank the do-nothing
    # detector first.
    silent = Confusion()
    silent.add(expected=40, detected=0, frames=100)
    check("abstaining precision", silent.precision, 0.0)
    check("abstaining recall", silent.recall, 0.0)
    check("abstaining F1", silent.f1, 0.0)

    # One that fires constantly.
    trigger_happy = Confusion()
    trigger_happy.add(expected=40, detected=100, frames=100)
    check("over-triggering recall", trigger_happy.recall, 1.0)
    if trigger_happy.precision >= 1.0:
        failures.append("over-triggering precision should be below 1")

    # Percentiles by nearest rank, on a set where interpolation would differ.
    latency = LatencySummary("test", "ms", samples=[10.0, 20.0, 30.0, 40.0])
    check("p50 nearest rank", latency.percentile(50), 20.0)
    check("p95 nearest rank", latency.percentile(95), 40.0)
    check("p100", latency.percentile(100), 40.0)
    check("empty percentile", LatencySummary("e", "ms").percentile(50), 0.0)

    # Unknown JSON fields are ignored rather than fatal.
    parsed = ScenarioResult.from_json(
        {"scenario": "x", "onsets": 2, "a_field_from_the_future": 7}
    )
    check("parsed scenario", parsed.scenario, "x")
    check("parsed onsets", parsed.onsets, 2)

    if failures:
        for f in failures:
            print(f"FAIL {f}", file=sys.stderr)
        return 1

    print("self-check: all arithmetic checks passed")
    return 0


def main() -> int:
    """Parse arguments and run the requested analysis."""
    parser = argparse.ArgumentParser(
        description="Analyse Phase 11D audio intelligence evaluation results."
    )
    parser.add_argument(
        "--input",
        type=Path,
        help="JSON results from the Go evaluation harness. Omit to run the self-check.",
    )
    parser.add_argument(
        "--output",
        type=Path,
        help="Write the Markdown report here instead of to stdout.",
    )
    args = parser.parse_args()

    if args.input is None:
        return self_check()

    results = load(args.input)
    rendered = report(results)

    if args.output:
        args.output.write_text(rendered, encoding="utf-8")
        print(f"wrote {args.output}")
    else:
        print(rendered)

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
