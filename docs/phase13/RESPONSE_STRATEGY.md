# Phase 13 — Response Strategy

**Phase 13 introduces no lifecycle state machine.** The existing
`conversation` and `voice` semantics remain authoritative. Phase 13 supplies a
classifier and a composition root; the frozen `conversation.Planner` continues to
decide what the agent does.

## Frozen action vocabulary (`conversation/policy.go:10`)

`ActionRespond` (zero value) · `ActionClarify` · `ActionConfirm` · `ActionAsk` ·
`ActionTransfer` · `ActionEscalate` · `ActionIgnore` · `ActionReject` ·
`ActionWait` · `ActionEnd`

**`ActionRespond` is `Action`'s zero value** (`iota`). A `Plan` returned
alongside an error is the zero `Plan`, so reading its `Action` reports "the agent
answered" for every refusal. Phase 13 tests therefore assert on the error and on
the transition history, never on a zero-valued `Action`. This was a real defect
in T9's first draft, found and corrected.

## Intent outcome to strategy — MEASURED (T13)

| Situation | Action | Reason | Clarification |
|---|---|---|---|
| accepted, complete | `ActionRespond` | `intent_accepted` | none |
| unfilled required slot | `ActionAsk` | `clarify_missing_slot` | `ClarifyMissingSlot` |
| ambiguous (margin < 0.15) | `ActionClarify` | `clarify_ambiguous` | `ClarifyAmbiguous` |
| low confidence (0.45–0.75) | `ActionConfirm` | `clarify_low_confidence` | `ClarifyLowConfidence` |
| below reject (< 0.45) | `ActionEscalate` | `intent_rejected` | none — terminal |
| zero candidates | `ActionRespond` | `fallback` | none |
| clarification budget spent | `ActionEscalate` | `clarification_exhausted` | none — terminal |
| conversation length bound | `ActionEscalate` | `max_turns_reached` | none — terminal |

## Fallback

With zero candidates and a configured fallback, the frozen engine resolves to
`IntentFallback` and responds. This is the documented behaviour of an engine
**with** a classifier that recognised nothing — distinct from an engine with
**no** classifier, which resolved *every* utterance to fallback and was the
pre-Phase-13 production state.

## Turn semantics — a report, not an instruction

`intent.ClassifyTurn` returns a `TurnSignal` built entirely from frozen types. It
**reports** the semantics of a turn; the existing planner and voice FSM decide
what to do. Full mapping in [TURN_SEMANTICS.md](TURN_SEMANTICS.md):

| Category | Frozen type | Value |
|---|---|---|
| continuation | `IntentState` | `IntentActive` |
| new request | `IntentState` | `IntentProposed` / `IntentSuperseded` |
| clarification | `ClarificationKind` | ambiguous / low-confidence / missing-slot / noise |
| interruption | `InterruptionKind` | `InterruptionUser` |
| acknowledgement | `FloorDecision` | `FloorBackchannel` |
| cancellation | `IntentState` | `IntentAbandoned` |
| silence | `EventKind` | `EventSilence` |
| completion | `IntentState` | `IntentFulfilled` |

**Acknowledgement, interruption and clarification are decided by frozen
components** (`TurnManager.NoteOverlap` classifies backchannels by overlap
*duration*, not by wording; floor arbitration decides barge-in;
`IntentEngine.Resolve` produces the verdict). Phase 13 consumes those decisions
as inputs rather than forming a competing opinion — a lexical backchannel
detector would have been the parallel interruption engine the architecture
forbids.

## Governance denial, tool failure, provider unavailability

These are **frozen concerns**, covered by `voice/failure_test.go`. Phase 13's
obligation is only that it preserves their distinctions, VERIFIED LOCALLY in T9:

- `voice.OutcomeDenied` is not `voice.OutcomeFailed`, and
  `OutcomeDenied.Successful()` is true — a refusal is the system working, not a
  fault.
- `voice.ErrGovernanceDenied` is distinguishable from
  `voice.ErrProviderUnavailable`.
- A tool fault after authorisation stays a fault (`OutcomeFailed`), never a
  denial.
- An infrastructure fault arrives as `EventFault` and must **not** be laundered
  into a classification outcome: `ClassifyTurn` leaves `Clarify` at
  `ClarifyNone` and the lifecycle unchanged.
- Authorisation state does not cross sessions (verified on observable context).

Phase 13 makes no governance decision, executes no tool, and bypasses neither.

## Silence and completion

Silence is not noise. `EventSilence` leaves the pursued intent untouched, sets no
clarification, and does **not** invoke the classifier — MEASURED
(`TestFailure01_SilenceDoesNotReachTheClassifier`: classifier invocations = 0).

Completion is `IntentFulfilled`, reached by `EventHangup` or a caller signalling
`end_call`. It is a **reported** signal: the classifier observes it; the frozen
engine remains the only writer of intent state.
