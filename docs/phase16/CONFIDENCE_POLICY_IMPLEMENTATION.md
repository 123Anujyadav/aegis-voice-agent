# Phase 16 T14 — ConfidenceUnknown Policy Implementation

**Status: IMPLEMENTED AND VERIFIED LOCALLY.** This document describes **current**
behaviour. It supersedes
[CONFIDENCE_CASCADE_EVIDENCE.md](CONFIDENCE_CASCADE_EVIDENCE.md), which records
the defect this replaced.

`-race NOT RUN LOCALLY — no race-safety claim; CI authoritative.`

---

## 1. The approved policy

1. Known confidence `< MinASRConfidence` → existing noise behaviour, unchanged.
2. Known confidence `>= MinASRConfidence` → existing classification, unchanged.
3. **No measurement reported → classification proceeds**, because confidence is
   *unavailable*, not because it is high.
4. **No numeric confidence is fabricated anywhere.**
5. No provider behaviour changed.

## 2. The amendment — one line

A second approved amendment to the frozen `conversation` module, alongside the
Phase 15 T4 one. `packages/go/conversation/intent.go:333`:

```go
- if u.ASRConfidence < e.cfg.MinASRConfidence {
+ if u.ASRConfidence >= 0 && u.ASRConfidence < e.cfg.MinASRConfidence {
```

and the contract at `intent.go:127`, which now reads *"0..1, or NEGATIVE when
the recogniser reports none"* and states that **a negative value is not a low
confidence**.

**`speech.ConfidenceUnknown` is deliberately not named in `conversation`.** The
contract is the **sign**, not one constant. That is what keeps the amendment
free of a `speech` dependency — `conversation` still imports nothing from
`speech`, preserving the one-dependency policy its `go.mod` documents.

The guard cannot weaken the gate: `MinASRConfidence` is itself bounded to
`[0,1]` by `validate()`, so every value in the documented domain compares
exactly as before.

## 3. Semantic matrix — measured

Read from `DefaultIntentConfig().MinASRConfidence`, asserted to be `0.40`
(`intent.go:222`) rather than hardcoded in the test.

| ASRConfidence | Result | Changed by T14? |
|---|---|---|
| 0.10 | noise | no |
| 0.20 | noise | no |
| 0.39 | noise | no |
| 0.40 | classified | no (boundary) |
| 0.80 | classified | no |
| 0.94 | classified | no |
| `ConfidenceUnknown` (−1.0) | **classified** | **yes** |
| −0.5 (any negative) | **classified** | **yes** |

Non-noise rows additionally assert the classifier was *consulted*
(`request_transfer` / `intent_accepted`), so "not noise" cannot silently mean
"some other non-noise branch".

## 4. Lifecycle after the fix

Three turns of an unreported-confidence transcript through the real pipeline:

```
turn1 {generated:true chunks:1 frames:2 completions:1 outcome:completed/ok}
turn2 {generated:true chunks:2 frames:4 completions:2 outcome:completed/ok}
turn3 {generated:true chunks:3 frames:6 completions:3 outcome:completed/ok}
conversation=listening  session=listening  floor=caller  non-terminal
```

Every turn now reaches generation → TTS → audio → `EventSpeechComplete` → floor
returned, and completes **exactly once**. Under the previous behaviour turn 3
produced no audio at all and ended the call.

Phase 15 T4/T5 failure paths re-verified unchanged: generation failure, TTS
failure and governance denial each 0 completions / 1 abandonment; interruption
0/0/1; cancellation 0/0/0.

## 5. Mutations — 4 required, 4 CAUGHT, one only after adding a test

| | Mutation | Result |
|---|---|---|
| M1 | restore old behaviour (drop `>= 0`) | CAUGHT |
| M2 | bypass the gate entirely | CAUGHT — exactly the known-low rows fail |
| M3 | pipeline fabricates `1.0` for unknown | **NOT CAUGHT initially → now CAUGHT** |
| M4 | invert the known-confidence comparison | CAUGHT |

**M3 is recorded as the miss it was.** Fabricating `1.0` at `pipeline.go:1074`
passed the entire suite. That is not a gap in those tests: `ASRConfidence` has
exactly one reader, and a fabricated `1.0` and an honest "unreported" take the
**same branch** of it, so no downstream assertion can separate them. The
difference is observable only at the seam where the `Utterance` is constructed.

Since *"never fabricate a confidence"* is an explicit term of the policy,
`TestT14_PipelineForwardsProviderConfidenceVerbatim` was added in
`packages/go/voice` (non-frozen, test-only). It captures the value on the
pipeline's own goroutine via the existing `scriptedPlanner.atEvent` hook and
asserts exact equality for unreported, low, high and genuine-zero confidences.
Re-running M3 then failed it surgically.

## 6. The accepted trade-off, stated plainly

**Whisper-class providers now have no acoustic noise gating.** Only text-level
defences — empty, whitespace-only and punctuation-only inputs resolving to the
fallback — sit between hallucinated recognition and the planner. That was the
approved decision and is recorded here as a live property, not a footnote.

## 7. Task status

| Category | T14 |
|---|---|
| **Implemented and verified** | The one-line gate amendment plus its contract documentation (§2); the T13 guards converted to assert approved semantics; the full confidence matrix (§3); the exactly-once lifecycle (§4); `TestT14_PipelineForwardsProviderConfidenceVerbatim` (§5). |
| **Measured but not implemented** | Nothing outstanding for this policy. The alternative option — extending `Utterance` with an explicit "reported" field (T12 Option D) — remains **unimplemented by choice**, since the sign convention made it unnecessary. |
| **Known limitations** | **Whisper-class providers now have no acoustic noise gating** (§6). `ASRConfidence` has exactly one reader, so a fabricated value is behaviourally indistinguishable downstream — the reason §5's seam test exists at all. |
| **Mutation results** | 4 required — **4 CAUGHT**, M3 only after a new test was added. Honestly recorded as a miss (§5). |
| **Deterministic test status** | `-count=10 -shuffle=on` green for both the service guards and the `voice` forwarding guard. No sleeps, no polling. |
| **Race status** | `-race NOT RUN LOCALLY — no race-safety claim; CI authoritative` (`CGO_ENABLED=0`, gcc absent). |
| **Frozen-module integrity** | `speech`, `media`, `intent`, `runtime`, `governance`, `memory`, `telephony` unchanged. `conversation` carries exactly two approved amendments: Phase 15 T4 and this one. No `go.mod`/`go.sum`/`go.work`/CI change. |
| **Security / dependency** | No transcript or PCM in logs or metrics, no credential, no network, no model download, no persistence, no unbounded queue, no package-level mutable state, **no new dependency** — `conversation` still imports nothing from `speech`. |
| **Resume point** | T14 is closed. Remaining Phase 16 work is documentation and commit hygiene; see [FIXTURE_PROVENANCE.md](FIXTURE_PROVENANCE.md). |

## 8. Boundary

WhisperCLI remains development-class and `ModeProduction` still refuses it.
Generation, TTS and audio delivery remain deterministic fakes. No claim of
recognition accuracy, model quality, telephony, race safety or production
readiness.
