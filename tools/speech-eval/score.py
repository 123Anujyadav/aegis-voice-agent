#!/usr/bin/env python3
"""Scores speech models against a consented test set, offline.

Measures speech-to-text accuracy and latency, and text-to-speech latency, in
the terms ADR-0005 and ADR-0007 set (docs/phase19/SPEECH-EVALUATION-PREPARATION.md).
It reads files a test run wrote; it runs no model, downloads nothing, and calls
no network service. Standard library only.

    validate MANIFEST [--audio-root DIR]
        the test set's manifest: unique ids, a supported language, a reference
        transcript and a consent record for every utterance; with --audio-root,
        each recording is 8 kHz, mono, 16-bit WAV (telephony audio)
    stt MANIFEST HYPOTHESES
        word and character error rates, time to the final transcript and the
        real-time factor, per language, against ADR-0005's budget
    tts TIMINGS
        time to the first audio and the real-time factor, per language, against
        ADR-0007's budget

Files are JSON Lines (one object per line):
    manifest     {"id", "language", "reference", "consent", "audio"?}
    hypotheses   {"id", "hypothesis", "final_ms"?, "audio_seconds"?, "processing_seconds"?}
    timings      {"id", "language", "first_audio_ms", "audio_seconds"?, "processing_seconds"?}

"language" is one of the owner's six (en, hi, ta, te, ml, kn) or hi-en for
Hindi-English code-switched speech. "consent" names the consent record that
covers the recording (ADR-0012); an utterance without one is refused.
"""

from __future__ import annotations

import argparse
from dataclasses import dataclass, field
import json
import math
from pathlib import Path
import sys
from typing import TYPE_CHECKING
import unicodedata
import wave

if TYPE_CHECKING:
    from collections.abc import Sequence

LANGUAGES = ("en", "hi", "ta", "te", "ml", "kn", "hi-en")
TELEPHONY_RATE = 8000

# ADR-0005 C1 (ADR-0011): final transcript after end of speech, p50 and p95, in ms.
STT_FINAL_BUDGET_MS = (120.0, 250.0)
# ADR-0007 C1 (ADR-0011): time to the first audio byte, p50 and p95, in ms.
TTS_FIRST_AUDIO_BUDGET_MS = (90.0, 180.0)

NUKTA = "\u093c"  # Devanagari nukta: "ज़" and "ज" are often written interchangeably
JOINERS = {"\u200c", "\u200d"}  # they choose a letter's form, not a different word


def normalise(text: str, *, fold_nukta: bool = False) -> str:
    """Text as it is compared: the same words written the same way.

    NFC; Latin letters lower-cased; zero-width joiners dropped; punctuation
    (the danda "।" included) and symbols become spaces; digits of any script
    become 0-9; runs of space collapse. With fold_nukta, the Devanagari nukta
    is dropped, for transcripts that write it inconsistently.
    """
    text = unicodedata.normalize("NFC", text)
    if fold_nukta:
        text = unicodedata.normalize("NFC", unicodedata.normalize("NFD", text).replace(NUKTA, ""))
    out = []
    for char in text.casefold():
        category = unicodedata.category(char)
        if char in JOINERS:
            continue
        if category == "Nd":
            out.append(str(unicodedata.digit(char)))
        elif category[0] in ("P", "S", "Z", "C"):
            out.append(" ")
        else:
            out.append(char)
    return " ".join("".join(out).split())


def edit_distance(reference: Sequence[str], hypothesis: Sequence[str]) -> int:
    """Levenshtein distance: substitutions, deletions and insertions."""
    previous = list(range(len(hypothesis) + 1))
    for i, ref in enumerate(reference, start=1):
        current = [i]
        for j, hyp in enumerate(hypothesis, start=1):
            current.append(min(previous[j] + 1, current[j - 1] + 1, previous[j - 1] + (ref != hyp)))
        previous = current
    return previous[-1]


@dataclass
class Rate:
    """Errors over reference length, summed over utterances (not averaged per utterance)."""

    errors: int = 0
    length: int = 0

    def add(self, reference: Sequence[str], hypothesis: Sequence[str]) -> None:
        """Adds one utterance's errors and reference length."""
        self.errors += edit_distance(reference, hypothesis)
        self.length += len(reference)

    @property
    def value(self) -> float:
        """The rate; 0 for no reference."""
        return self.errors / self.length if self.length else 0.0


def words(text: str) -> list[str]:
    """A normalised text's words."""
    return text.split()


def characters(text: str) -> list[str]:
    """A normalised text's characters (code points), spaces left out.

    Vowel signs and viramas count as characters, so a wrong vowel sign in an
    Indian script is one error. Spaces are left out so that a word split
    differently is not counted twice.
    """
    return [c for c in text if c != " "]


def percentile(values: Sequence[float], q: float) -> float:
    """The nearest-rank percentile [q] (0-100); NaN for no values."""
    if not values:
        return math.nan
    ordered = sorted(values)
    rank = max(1, math.ceil(q / 100 * len(ordered)))
    return ordered[rank - 1]


def read_jsonl(path: Path) -> list[dict[str, object]]:
    """A JSON Lines file's objects; blank lines are skipped."""
    with path.open(encoding="utf-8") as f:
        return [json.loads(line) for line in f if line.strip()]


def _wav_problem(path: Path) -> str:
    try:
        with wave.open(str(path), "rb") as audio:
            shape = (audio.getframerate(), audio.getnchannels(), audio.getsampwidth())
    except (OSError, EOFError, wave.Error) as error:
        return f"cannot read it as WAV ({error})"
    if shape != (TELEPHONY_RATE, 1, 2):
        rate, channels, width = shape
        return f"{rate} Hz, {channels} channel(s), {8 * width}-bit: telephony audio is 8000 Hz mono 16-bit"
    return ""


def validate(manifest: list[dict[str, object]], audio_root: Path | None = None) -> list[str]:
    """What is wrong with a test set's manifest; empty when it can be used."""
    problems = []
    seen: set[str] = set()
    for line, entry in enumerate(manifest, start=1):
        utterance = str(entry.get("id") or "")
        where = f"line {line} ({utterance or 'no id'})"
        if not utterance:
            problems.append(f"{where}: no id")
        elif utterance in seen:
            problems.append(f"{where}: the id appears twice")
        seen.add(utterance)
        if entry.get("language") not in LANGUAGES:
            problems.append(
                f"{where}: language {entry.get('language')!r} is not one of {LANGUAGES}"
            )
        if not normalise(str(entry.get("reference") or "")):
            problems.append(f"{where}: no reference transcript")
        if not str(entry.get("consent") or "").strip():
            problems.append(f"{where}: no consent record (ADR-0012): the recording cannot be used")
        if audio_root is not None:
            audio = audio_root / str(entry.get("audio") or "")
            if not entry.get("audio") or not audio.is_file():
                problems.append(f"{where}: no recording at {audio}")
            else:
                problem = _wav_problem(audio)
                if problem:
                    problems.append(f"{where}: {audio.name}: {problem}")
    return problems


@dataclass
class LanguageResult:
    """One language's measurements."""

    utterances: int = 0
    missing: list[str] = field(default_factory=list)
    wer: Rate = field(default_factory=Rate)
    cer: Rate = field(default_factory=Rate)
    latencies_ms: list[float] = field(default_factory=list)
    audio_seconds: float = 0.0
    processing_seconds: float = 0.0

    @property
    def real_time_factor(self) -> float:
        """Processing time over audio time: below 1 keeps up with a live call."""
        return self.processing_seconds / self.audio_seconds if self.audio_seconds else math.nan


def _number(value: object) -> float | None:
    return float(value) if isinstance(value, int | float) and not isinstance(value, bool) else None


def _timing(result: LanguageResult, row: dict[str, object], latency_key: str) -> None:
    latency = _number(row.get(latency_key))
    if latency is not None:
        result.latencies_ms.append(latency)
    audio, processing = _number(row.get("audio_seconds")), _number(row.get("processing_seconds"))
    if audio is not None and processing is not None:
        result.audio_seconds += audio
        result.processing_seconds += processing


def score_stt(
    manifest: list[dict[str, object]],
    hypotheses: list[dict[str, object]],
    *,
    fold_nukta: bool = False,
) -> dict[str, LanguageResult]:
    """Error rates and latency per language. A missing hypothesis counts as silence."""
    by_id = {str(h.get("id")): h for h in hypotheses}
    results: dict[str, LanguageResult] = {}
    for entry in manifest:
        result = results.setdefault(str(entry["language"]), LanguageResult())
        result.utterances += 1
        hypothesis = by_id.get(str(entry["id"]))
        if hypothesis is None:
            result.missing.append(str(entry["id"]))
            hypothesis = {}
        reference = normalise(str(entry["reference"]), fold_nukta=fold_nukta)
        heard = normalise(str(hypothesis.get("hypothesis") or ""), fold_nukta=fold_nukta)
        result.wer.add(words(reference), words(heard))
        result.cer.add(characters(reference), characters(heard))
        _timing(result, hypothesis, "final_ms")
    return results


def score_tts(timings: list[dict[str, object]]) -> dict[str, LanguageResult]:
    """Time to the first audio and the real-time factor, per language."""
    results: dict[str, LanguageResult] = {}
    for row in timings:
        result = results.setdefault(str(row.get("language")), LanguageResult())
        result.utterances += 1
        _timing(result, row, "first_audio_ms")
    return results


def _budget(latencies: Sequence[float], budget: tuple[float, float]) -> str:
    if not latencies:
        return "no timings"
    p50, p95 = percentile(latencies, 50), percentile(latencies, 95)
    verdict = "within" if p50 <= budget[0] and p95 <= budget[1] else "OVER"
    return f"{p50:.0f} / {p95:.0f} ms, {verdict} {budget[0]:.0f} / {budget[1]:.0f}"


def report_stt(results: dict[str, LanguageResult]) -> list[str]:
    """A Markdown table of score_stt's results."""
    lines = [
        "| Language | Utterances | Missing | WER | CER | Final transcript p50 / p95 | RTF |",
        "|---|---|---|---|---|---|---|",
    ]
    for language in sorted(results):
        r = results[language]
        lines.append(
            f"| {language} | {r.utterances} | {len(r.missing)} | {r.wer.value:.1%} | "
            f"{r.cer.value:.1%} | {_budget(r.latencies_ms, STT_FINAL_BUDGET_MS)} | "
            f"{r.real_time_factor:.2f} |"
        )
    return lines


def report_tts(results: dict[str, LanguageResult]) -> list[str]:
    """A Markdown table of score_tts's results."""
    lines = ["| Language | Utterances | First audio p50 / p95 | RTF |", "|---|---|---|---|"]
    for language in sorted(results):
        r = results[language]
        lines.append(
            f"| {language} | {r.utterances} | "
            f"{_budget(r.latencies_ms, TTS_FIRST_AUDIO_BUDGET_MS)} | {r.real_time_factor:.2f} |"
        )
    return lines


def main(argv: list[str]) -> int:
    """Runs one command; returns the process's exit status."""
    parser = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    sub = parser.add_subparsers(dest="command", required=True)
    p_validate = sub.add_parser("validate")
    p_validate.add_argument("manifest", type=Path)
    p_validate.add_argument("--audio-root", type=Path)
    p_stt = sub.add_parser("stt")
    p_stt.add_argument("manifest", type=Path)
    p_stt.add_argument("hypotheses", type=Path)
    p_stt.add_argument("--fold-nukta", action="store_true")
    p_tts = sub.add_parser("tts")
    p_tts.add_argument("timings", type=Path)
    args = parser.parse_args(argv)

    if args.command == "tts":
        sys.stdout.write("\n".join(report_tts(score_tts(read_jsonl(args.timings)))) + "\n")
        return 0
    manifest = read_jsonl(args.manifest)
    problems = validate(manifest, args.audio_root if args.command == "validate" else None)
    if problems:
        sys.stderr.write("\n".join(problems) + f"\n{len(problems)} problems\n")
        return 1
    if args.command == "validate":
        sys.stdout.write(f"{len(manifest)} utterances, all usable\n")
        return 0
    results = score_stt(manifest, read_jsonl(args.hypotheses), fold_nukta=args.fold_nukta)
    sys.stdout.write("\n".join(report_stt(results)) + "\n")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
