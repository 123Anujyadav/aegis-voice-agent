# Test fixtures

Binary fixtures in this directory are governed by
[ADR-0023](../../../../../../docs/adr/0023-binary-test-fixture-provenance.md).
Every requirement it states is recorded below; a new binary fixture must carry
the same.

## `speech_transfer_16k_mono_pcm16.raw`

**What it is:** 2.27 s of headerless PCM — 16 kHz, mono, signed 16-bit
little-endian — of a synthetic voice saying **"Transfer me to Rajesh"**.

**Integrity (ADR-0023 §2):**

| | |
|---|---|
| SHA-256 | `8eed99cb93b575df01c128b66312d25d1fca3aba1d149fd2eada6059411ff870` |
| Size | 72,640 bytes exactly |
| Duration | 2.27 s at 16 kHz mono PCM16 (32,000 bytes/s) |

The size is additionally asserted by
`TestT11_AMissingFixtureIsATestFailureNotASkip`, so a substitution fails loudly
rather than silently changing what the transcript assertion is measured against.

**Why it exists:** Phase 16 T11 needed to prove
`real speech → real WhisperCLI → real transcript → real intent`. Every earlier
real-provider test ran on a tone, which can only ever prove the *absence* of
hallucination. Speech is the one thing a tone cannot stand in for.

**How it was produced (once, by hand — not by the test suite):** Windows SAPI
(`System.Speech.Synthesis.SpeechSynthesizer`) writing directly at 16 kHz mono
16-bit, so no resampling and no `ffmpeg` were involved. The RIFF `data` chunk was
extracted verbatim; no header, and therefore no `LIST`/`INFO` metadata, is
present in this file.

**Why it is committed rather than generated:** so the test depends on nothing but
these bytes. SAPI exists only on Windows, and a fixture regenerated per run would
make the transcript — and therefore the assertion — a property of whichever
speech engine happened to be installed.

**Content safety:** entirely synthetic. No real person's voice, no personal data,
no account or phone number, no credential. "Rajesh" is the placeholder name used
throughout this repository's dialogue fixtures.

**Do not regenerate casually.** The committed transcript assertion
(`transfer me to rajesh.`) was measured against this exact audio.
