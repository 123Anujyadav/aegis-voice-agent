# Phase 13 — T8 Turn / Interruption Semantic Map

Status of each claim below is marked IMPLEMENTED, VERIFIED, MEASURED, NOT RUN or
BLOCKED. Nothing here asserts NLU accuracy or model quality.

## The finding that shaped T8

Every one of the eight requested categories already has a representation in the
frozen vocabulary. **No gap was found, so no new lifecycle enum was created.**

More importantly, three of the eight are already *decided* by frozen components,
not merely representable:

| Category | Frozen decider | How it decides |
|---|---|---|
| acknowledgement | `TurnManager.NoteOverlap` (turn.go:414) | overlap ≤ `BackchannelMaxDuration` (600ms) → `FloorBackchannel` |
| interruption | `TurnManager` / `InterruptionEngine` | floor arbitration → `InterruptionKind` |
| clarification | `IntentEngine.Resolve` (intent.go:305) | `IntentVerdict` from the frozen thresholds |

A lexical backchannel detector in Phase 13 would be a **second interruption
engine** competing with a frozen one that classifies by duration. T8 therefore
*consumes* these decisions as inputs and never recomputes them.

## Semantic map: input → decision → frozen type → FSM interpretation

`ClassifyTurn` is a pure function. It reads frozen outputs and emits frozen
values. It never mutates anything.

```
                    ┌──────────────── frozen deciders ────────────────┐
  caller audio ───► │ TurnManager    → FloorDecision, InterruptionKind │
  ASR transcript ─► │ IntentEngine   → IntentVerdict, Intent           │ ──┐
  event source ───► │ conversation   → EventKind, Expectation          │   │
                    └─────────────────────────────────────────────────┘   │
                                                                          ▼
                                              intent.ClassifyTurn(TurnInput)
                                                          │  (pure, bounded)
                                                          ▼
                                              intent.TurnSignal
                                              — a bundle of FROZEN values only —
                                                          │
                                                          ▼
                            consumed by existing conversation planner / voice FSM
                            (T8 has no authority to transition anything)
```

### Category → exact frozen type and value

| # | Category | Frozen type | Frozen value asserted |
|---|---|---|---|
| 1 | continuation | `conversation.IntentState` | `IntentActive` |
| 2 | new request | `conversation.IntentState` | `IntentProposed` (nothing active) / `IntentSuperseded` (replacing an active intent) |
| 3 | clarification | `conversation.ClarificationKind` | `ClarifyAmbiguous` / `ClarifyLowConfidence` / `ClarifyMissingSlot` / `ClarifyNoise` |
| 4 | interruption | `conversation.InterruptionKind` | `InterruptionUser` |
| 5 | acknowledgement | `conversation.FloorDecision` | `FloorBackchannel` |
| 6 | cancellation | `conversation.IntentState` | `IntentAbandoned` |
| 7 | silence | `conversation.EventKind` | `EventSilence` |
| 8 | completion | `conversation.IntentState` | `IntentFulfilled` |

Types 1, 2, 6 and 8 are all `IntentState` — that is the frozen **intent
lifecycle**, and it already distinguishes exactly these four. Reusing it is the
whole point; introducing a parallel enum would have duplicated it.

### Precedence

`ClassifyTurn` mirrors the ordering the frozen `IntentEngine.Resolve` documents
as "the order of checks is the policy, and it matters" (intent.go:305):

1. `EventSilence` → silence, lifecycle unchanged
2. `EventHangup` → completion
3. `EventInterrupt` → interruption
4. `EventOverlap` → **the frozen `FloorDecision` decides**: `FloorBackchannel`
   is acknowledgement, anything else is interruption
5. `EventUtterance`:
   1. `IntentNoise` verdict → clarification / `ClarifyNoise` *(noise first, same
      as frozen)*
   2. `ExpectYesNo` + affirm/deny → continuation *(constrained expectation
      short-circuits, same as frozen)*
   3. cancellation cue → cancellation
   4. `IntentEndCall` → completion
   5. `IntentReject` verdict → unknown, `IntentUnknown`, lifecycle unchanged
   6. `IntentClarify` verdict → clarification, kind selected below
   7. pending expectation → continuation
   8. otherwise → new request
6. every other event → lifecycle unchanged, event echoed

### Confidence

**No second confidence model.** T8 defines no threshold. It consumes the
`IntentVerdict` the frozen engine already produced. The single numeric
comparison it makes — distinguishing `ClarifyAmbiguous` from
`ClarifyLowConfidence` — uses `Intent.Margin()` (a frozen method) against
`IntentConfig.AmbiguityMargin` (a frozen field) carried in `TurnInput`, i.e. the
same config the engine itself used. See ADR-0016.

### The one thing T8 adds

Cancellation. No frozen component detects a caller withdrawing a request
("never mind", "forget it"); `IntentAbandoned` exists but the frozen engine sets
it for a spent clarification budget. T8 supplies a bounded, closed cue set and
maps it to the existing `IntentAbandoned`. It adds **no** `IntentName` and **no**
enum.

Cancellation is deliberately distinct from denial: under `ExpectYesNo`, "no" is
`IntentDeny` answering the question (continuation), not a cancellation.

## FSM safety

- `TurnSignal` contains no `conversation.State` and no `conversation.Trigger`.
- The `intent` package references neither identifier anywhere (structurally
  guarded).
- `ClassifyTurn` is a pure function of its argument: no receiver, no package
  state, no clock, no map iteration, no goroutine.
- It returns a value. Acting on it remains the existing planner's and voice
  FSM's job.
