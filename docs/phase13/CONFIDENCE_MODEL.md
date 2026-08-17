# Phase 13 — Confidence Model

**Phase 13 defines no confidence model of its own.** It inherits the frozen
thresholds and consumes the frozen engine's verdict. ADR-0016 records why a
second confidence model is not permitted.

## Deterministic scoring is not probabilistic confidence

This is the most important distinction in this document.

`intent.Classifier` produces a **deterministic evidence ratio**, not a
probability:

```
conf = min(evidence / saturation, 1.0)      saturation default 3.0
```

It is **not** a calibrated likelihood, **not** a model probability, and **not** a
statistical estimate. Two utterances scoring 0.667 are not "67% likely to be
correct" — each matched two single-token cues. No calibration study was
performed and none is claimed.

MEASURED attainable values with the real lexicon: **0.333**, **0.667**, **1.0**.

## Frozen thresholds (source: `conversation/intent.go:206-209`)

| Threshold | Value | Line |
|---|---|---|
| `AcceptThreshold` | 0.75 | 206 |
| `RejectThreshold` | 0.45 | 207 |
| `AmbiguityMargin` | 0.15 | 208 |
| `MinASRConfidence` | 0.40 | 209 |

## Frozen decision order (`IntentEngine.Resolve`, `intent.go:305`)

The engine's own comment: *"The order of checks is the policy, and it matters."*

1. **Noise first** — `ASRConfidence < MinASRConfidence` yields `IntentNoise`. An
   unintelligible utterance is not a low-confidence intent; treating it as one
   produces a clarification about a topic the caller never raised.
2. **Constrained expectation** — under `ExpectYesNo`, "yes" means yes; this
   short-circuits general classification.
3. **Confidence, ambiguity, then slot completeness:**
   - `top < RejectThreshold` yields `IntentReject`
   - `top < AcceptThreshold` yields `IntentClarify`
   - `Margin() < AmbiguityMargin` yields `IntentClarify`
   - `!Complete()` yields `IntentClarify`
   - otherwise `IntentAccept`

`intent.ClassifyTurn` mirrors this order and **introduces no threshold**. Its one
numeric comparison — distinguishing `ClarifyAmbiguous` from
`ClarifyLowConfidence` — uses the frozen `Intent.Margin()` method against the
frozen `IntentConfig.AmbiguityMargin` carried in its input, i.e. the same config
the engine itself used.

## The five outcomes

| Outcome | Condition | Result |
|---|---|---|
| **accepted** | conf >= 0.75, margin >= 0.15, slots complete | `ActionRespond`, `intent_accepted` |
| **rejected (below-reject)** | conf < 0.45 | `ActionEscalate`, `intent_rejected` — terminal |
| **unknown** | **zero candidates** | fallback intent, `ActionRespond`, `fallback` |
| **low confidence** | 0.45 <= conf < 0.75 | `ActionConfirm`, `ClarifyLowConfidence` |
| **ambiguous** | margin < 0.15 | `ActionClarify`, `ClarifyAmbiguous` |

MEASURED fixture inputs producing each (T13):

- accepted — "please call me back on 9876543210" at 1.0
- below-reject — "callback transfer" at 0.333
- unknown — "zzzz qqqq wubble frotz", zero candidates
- low confidence — "repeat pardon" / "wait ruko" at 0.667
- ambiguous — "hold on call back", two candidates at 1.0, margin 0

`TestT13_FourOutcomesStayDistinct` requires four different
`(action, reason, clarification)` triples, so unknown and low confidence can
never silently collapse into one another.

## What is NOT claimed

- No NLU accuracy.
- No model accuracy, no model probability.
- No statistical calibration.
- No real-world corpus evaluation.
- No claim that confidence correlates with correctness on unseen input.

## Confidence in determinism signatures

T11's golden records confidence **exactly and as a bucket**
(`zero` / `below_reject` / `clarify_band` / `accept` / `certain`). Bucketing
alone would hide a drift of 0.09 — most of the distance between the reject and
accept thresholds — so both forms are recorded.
