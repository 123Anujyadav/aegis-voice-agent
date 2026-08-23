# Phase 16 T15 — Fixture Provenance & Real-Provider Evidence Closure

**Status: COMPLETE. No production code changed.** The decision itself is
recorded in [ADR-0023](../adr/0023-binary-test-fixture-provenance.md); this
document records the audit that produced it and the evidence classification that
motivated it.

---

## 1. What was audited

Whether the repository's fixture provenance satisfied the policy
[T12](CONFIDENCE_DECISION_AUDIT.md) recommended, and what remained unproven in
the real-provider evidence chain.

Two claims were re-verified independently rather than inherited:

- `CONTRIBUTING.md` contains **no** fixture, testdata, binary or artifact rule.
- `docs/phase13/EVALUATION_FIXTURES.md` governs **text** fixtures only. It does
  establish the norm this work inherits — fixtures are *"measured, never hoped
  for"*, and *"no number here may be reported as accuracy"*.

Both hold. There was no governing rule for committed binary audio.

## 2. Evidence classification

| Link | Classification |
|---|---|
| Fixture audio | **Deterministic fixture**, synthetic origin (Windows SAPI, one-time, historical) |
| WhisperCLI subprocess | **Real provider**, executed |
| Local `tiny` model | **Cached local model** — no download, no network |
| Transcript | **Real provider output**, measured |
| Confidence (`ConfidenceUnknown`) | **Real provider value**, forwarded verbatim |
| `voice.Pipeline` | **Real production code** |
| Intent classification | **Real** deterministic rule classifier |
| Generation | **Synthetic** — `rt.FakeProvider` |
| TTS | **Synthetic** — `speech.FakeTTSProvider` |
| Audio delivery | **Synthetic** — `countingSink` |
| Lifecycle completion | **Real** frozen conversation engine |

**Still unproven, and not claimed:** recognition accuracy, model quality, real
TTS, real generation, telephony, race safety, production readiness.

## 3. The defect this closed

The Phase 16 T11 tests asserted the fixture was *committed*.
`TestT11_AMissingFixtureIsATestFailureNotASkip` calls `t.Fatalf` on absence,
deliberately, so the evidence cannot silently degrade into a skip. **But the
fixture was never added to the index.** On a fresh checkout that test and the
real-speech suite around it **failed rather than skipped**. Committed tests
depended on an untracked file.

Demonstrated by moving the file aside: two tests failed exactly as predicted.

A second, smaller gap: integrity was pinned by **byte size only**, and
same-length audio substitutes silently. A binary cannot be reviewed in a diff.

## 4. What was done

1. [ADR-0023](../adr/0023-binary-test-fixture-provenance.md) — six requirements
   for any `testdata/` binary: sibling README with origin and platform; SHA-256
   **and** byte size; no external toolchain at test or build time; synthetic
   content; minimal size; regeneration requires re-measurement. SAPI/ffmpeg are
   permitted as a **one-time authoring step only**, explicitly not extended as a
   runtime dependency.
2. `services/go/voice/cmd/server/testdata/README.md` — the worked example, now
   carrying the integrity table.
3. `TestT11_AMissingFixtureIsATestFailureNotASkip` — the integrity requirement
   made **executable**, asserting the SHA-256 as well as the size. A single
   flipped byte at identical length is now caught.

## 5. The fixture

`services/go/voice/cmd/server/testdata/speech_transfer_16k_mono_pcm16.raw`

| | |
|---|---|
| SHA-256 | `8eed99cb93b575df01c128b66312d25d1fca3aba1d149fd2eada6059411ff870` |
| Size | 72,640 bytes (2.27 s, 16 kHz mono PCM16) |
| Content | 12.4 % printable bytes, longest ASCII run 11 chars of noise; no `RIFF`/`WAVE`/`LIST`/`INFO`/`ID3` markers, no encoder tags, no dictionary words |

Entirely synthetic — no real person's voice, no personal data. Consumed by
`TestT11_RealSpeechProducesTheExpectedTranscript`,
`TestT11_TheRealTranscriptItselfResolvesToTheRealIntent` (both via `t11Frames`)
and `TestT11_AMissingFixtureIsATestFailureNotASkip`.

## 6. A CRLF hazard found while adding it

`.gitattributes` carried `* text=auto eol=lf` with `core.autocrlf=true`, and
`.raw` had **no binary rule** — `git check-attr` reported `binary: unspecified`,
so git would decide by heuristic. A mis-guess applies `eol=lf`, stripping `0x0D`
bytes from PCM samples and corrupting audio whose SHA-256 is pinned by test.

For this file the heuristic happens to work (leading NUL bytes), and the staged
blob was verified byte-identical. But ADR-0023 authorises an artifact *class*,
and a fixture without early NULs would be mangled.

`.gitattributes` already listed `*.wav`, `*.mp3`, `*.opus` and **`*.pcm`** as
binary; `.raw` was simply never needed before. One line completed that
convention, and classification is now `binary: set` — deterministic rather than
heuristic.

## 7. Boundary

No claim of recognition accuracy, model quality, telephony, race safety or
production readiness. WhisperCLI remains development-class and `ModeProduction`
still refuses it.
