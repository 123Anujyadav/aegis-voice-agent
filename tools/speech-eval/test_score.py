"""Tests for score: run with `python3 -m unittest discover -s tools/speech-eval -v`."""

from __future__ import annotations

import ast
import json
import math
from pathlib import Path
import socket
import tempfile
from typing import NoReturn
import unittest
from unittest import mock
import wave

import score


def utterance(uid: str, language: str, reference: str, consent: str = "C-1") -> dict[str, object]:
    """One manifest entry."""
    return {"id": uid, "language": language, "reference": reference, "consent": consent}


class NormaliseTest(unittest.TestCase):
    def test_punctuation_case_digits_and_joiners_do_not_count(self) -> None:
        self.assertEqual("मेरा नंबर 98765 है", score.normalise("मेरा नंबर ९८७६५ है।"))
        self.assertEqual("hello world", score.normalise("Hello,   World!"))
        self.assertEqual("കണ്ന", score.normalise("കണ്\u200dന"))

    def test_composed_and_decomposed_text_compare_equal(self) -> None:
        self.assertEqual(score.normalise("\u0929"), score.normalise("\u0928\u093c"))

    def test_the_nukta_is_folded_only_when_asked(self) -> None:
        self.assertNotEqual(score.normalise("ज़रूर"), score.normalise("जरूर"))
        self.assertEqual(
            score.normalise("ज़रूर", fold_nukta=True), score.normalise("जरूर", fold_nukta=True)
        )


class RatesTest(unittest.TestCase):
    def test_edit_distance(self) -> None:
        self.assertEqual(0, score.edit_distance([], []))
        self.assertEqual(3, score.edit_distance(list("abc"), []))
        self.assertEqual(2, score.edit_distance(list("kitten"), list("sitte")))

    def test_word_and_character_error_rates_sum_over_utterances(self) -> None:
        manifest = [
            utterance("1", "hi", "मुझे कल फ़ोन करना"),
            utterance("2", "hi", "धन्यवाद"),
            utterance("3", "ta", "நன்றி"),
        ]
        hypotheses = [
            {
                "id": "1",
                "hypothesis": "मुझे कल फोन करना",
                "final_ms": 100,
                "audio_seconds": 2.0,
                "processing_seconds": 1.0,
            },
            {"id": "2", "hypothesis": "धन्यवाद", "final_ms": 300},
        ]
        results = score.score_stt(manifest, hypotheses)
        hindi = results["hi"]
        self.assertEqual(2, hindi.utterances)
        self.assertEqual(1 / 5, hindi.wer.value)  # one word of five: the nukta differs
        self.assertEqual(1, hindi.cer.errors)  # the nukta alone
        self.assertEqual([100.0, 300.0], hindi.latencies_ms)
        self.assertEqual(0.5, hindi.real_time_factor)
        tamil = results["ta"]
        self.assertEqual(["3"], tamil.missing)  # no hypothesis: every word deleted
        self.assertEqual(1.0, tamil.wer.value)
        self.assertTrue(math.isnan(tamil.real_time_factor))
        folded = score.score_stt(manifest, hypotheses, fold_nukta=True)["hi"]
        self.assertEqual(0.0, folded.wer.value)

    def test_the_report_compares_latency_with_the_adr_budgets(self) -> None:
        manifest = [utterance(str(i), "kn", "ಹೌದು") for i in range(20)]
        fast = [{"id": str(i), "hypothesis": "ಹೌದು", "final_ms": 100} for i in range(20)]
        self.assertIn(
            "within 120 / 250", "\n".join(score.report_stt(score.score_stt(manifest, fast)))
        )
        slow = [{**h, "final_ms": 100 if i < 18 else 400} for i, h in enumerate(fast)]
        self.assertIn(
            "OVER 120 / 250", "\n".join(score.report_stt(score.score_stt(manifest, slow)))
        )
        tts = [{"id": str(i), "language": "ml", "first_audio_ms": 500} for i in range(3)]
        self.assertIn(
            "500 / 500 ms, OVER 90 / 180", "\n".join(score.report_tts(score.score_tts(tts)))
        )

    def test_percentile_is_nearest_rank(self) -> None:
        values = list(range(1, 21))
        self.assertEqual(10, score.percentile(values, 50))
        self.assertEqual(19, score.percentile(values, 95))
        self.assertTrue(math.isnan(score.percentile([], 50)))


class ValidateTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def wav(self, name: str, rate: int = 8000, channels: int = 1) -> str:
        with wave.open(str(self.root / name), "wb") as audio:
            audio.setnchannels(channels)
            audio.setsampwidth(2)
            audio.setframerate(rate)
            audio.writeframes(b"\x00\x00" * channels * 80)
        return name

    def test_a_usable_manifest_has_no_problems(self) -> None:
        entry = {**utterance("1", "hi-en", "kal call karna"), "audio": self.wav("a.wav")}
        self.assertEqual([], score.validate([entry], self.root))

    def test_every_problem_is_reported(self) -> None:
        manifest = [
            {**utterance("1", "bn", "x", consent=""), "audio": self.wav("wide.wav", rate=16000)},
            {**utterance("1", "hi", " । "), "audio": "missing.wav"},
        ]
        problems = score.validate(manifest, self.root)
        for expected in (
            "'bn' is not one of",
            "no consent record",
            "16000 Hz",
            "appears twice",
            "no reference transcript",
            "no recording",
        ):
            self.assertTrue(any(expected in p for p in problems), f"{expected!r}: {problems}")

    def test_the_command_refuses_a_manifest_without_consent(self) -> None:
        manifest = self.root / "m.jsonl"
        manifest.write_text(json.dumps(utterance("1", "te", "అవును", consent="")) + "\n", "utf-8")
        with mock.patch.object(score.sys, "stderr"):
            self.assertEqual(1, score.main(["validate", str(manifest)]))


class OfflineTest(unittest.TestCase):
    def test_it_works_with_no_network_at_all(self) -> None:
        def no_network(*_args: object, **_kwargs: object) -> NoReturn:
            raise AssertionError("score tried to open a network connection")

        with (
            mock.patch.object(socket, "socket", no_network),
            mock.patch.object(socket, "create_connection", no_network),
            mock.patch.object(socket, "getaddrinfo", no_network),
        ):
            results = score.score_stt(
                [utterance("1", "hi", "हाँ")], [{"id": "1", "hypothesis": "हाँ"}]
            )
            score.report_stt(results)

    def test_it_imports_no_networking_module(self) -> None:
        tree = ast.parse(Path(score.__file__).read_text(encoding="utf-8"))
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
        network = {"socket", "ssl", "http", "urllib", "requests", "ftplib", "smtplib", "asyncio"}
        self.assertEqual(set(), imported & network)


if __name__ == "__main__":
    unittest.main()
