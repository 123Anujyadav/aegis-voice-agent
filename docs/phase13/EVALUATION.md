# Phase 13 — Evaluation

## The claim this document makes, and the one it does not

**These fixtures are deterministic behavioural tests. They are NOT
natural-language-understanding accuracy measurements.**

`intent.Classifier` is a deterministic, closed-vocabulary rule matcher. A
fixture result means *"this known input still produces this known typed
outcome"*. It says nothing about real-world language understanding.

Where a rate is mentioned it is a **fixture pass rate**, never accuracy. No
percentage in Phase 13 may be reported as NLU accuracy, model accuracy, or
benchmark accuracy against real users. No real-world corpus was evaluated. No
model or provider was involved.

Detailed inventory: [EVALUATION_FIXTURES.md](EVALUATION_FIXTURES.md).

## Suite shape (T13)

- **9 top-level tests, 18 sub-tests** — `go test -run TestT13_ -v`, all PASS.
- **6 mutation guards, 6 CAUGHT.**
- Fixture **pass rate: 12/12 table fixtures + all dedicated fixtures**, 0 failures.

Every fixture declares its complete expected outcome — candidate count, top
intent, confidence range and class, action, reason, plan intent, clarification
kind, slot-shape count and terminal state — and **every declared field is
asserted, including zero values**. "Expect no clarification" is as much a claim
as "expect ambiguity". No fixture asserts merely "did not panic".

Expectations were **measured** against the real classifier and real bridge before
being written down; none was assumed.

## Categories covered — all 12 required

| Category | Where | Asserted outcome |
|---|---|---|
| normal | table x2 | `ActionRespond`, `intent_accepted`, conf 1.0 |
| ambiguous | table x1 | `ActionClarify`, `clarify_ambiguous`, 2 cands, margin 0 |
| clarification | table x2 | `ActionAsk`, `clarify_missing_slot` |
| low confidence | table x3 | `ActionConfirm`, `clarify_low_confidence`, conf 0.667; plus a below-reject case |
| malformed input | table x4 | 0 candidates, `ActionRespond`, `fallback` |
| interruption | `TestT13_TurnFixtures` | `InterruptionUser` |
| acknowledgement | `TestT13_TurnFixtures` | `FloorBackchannel`, lifecycle unchanged |
| cancellation | `TestT13_TurnFixtures` | `IntentAbandoned` |
| silence | `TestT13_TurnFixtures` | `EventSilence`, `ClarifyNone` |
| multi-turn | `TestT13_MultiTurnProgression` | 4-step progression, context survives, non-terminal |
| context eviction | `TestT13_ContextEvictionFixture` | bound 256, one victim, newest survives |
| concurrent sessions | `TestT13_ConcurrentSessionFixture` | matches serial baseline, markers isolated |

## Four outcomes kept distinct

`TestT13_FourOutcomesStayDistinct` requires **unknown**, **below-reject**,
**low confidence** and **ambiguous** to yield four *different*
`(action, reason, clarification)` triples. Asserting each in isolation would not
catch two being merged:

```
malformed/unknown_vocabulary   -> action=respond  reason=fallback               clarify=none
below_reject/single_weak_cue   -> action=escalate reason=intent_rejected        clarify=none
low_confidence/repeat          -> action=confirm  reason=clarify_low_confidence clarify=low_confidence
ambiguous/hold_and_callback    -> action=clarify  reason=clarify_ambiguous      clarify=ambiguous
```

## Determinism

- Whole inventory re-run **26 times**, identical signature
  (`TestT13_FixturesAreDeterministic`).
- Fixture **order** asserted stable across 10 constructions.
- Signatures contain only typed logical outcomes — no timestamps, durations,
  goroutine identity or scheduler ordering.
- No sleeps, no wall-clock pass/fail logic, no random data.
- T11's determinism golden re-run after T13: **ok** — the fixtures did not
  perturb it.

## Mutation verification — 6/6 CAUGHT

| | Mutation | Caught by |
|---|---|---|
| M1 | callback rule mislabelled as `repeat` | normal fixture + multi-turn |
| M2 | candidate cap 5 -> 1 (ambiguity collapses) | ambiguous fixture |
| M3 | "never mind" cue removed | cancellation fixture |
| M4 | eviction bound disabled | both eviction sub-fixtures |
| M5 | all sessions share one context | concurrent marker checks |
| M6 | saturation 3.0 -> 1.5 (low-conf promoted) | low-confidence fixture |

Two mutations required correction before they were valid: M1 did not compile,
and M5 was **inert** because the first draft built a bridge per goroutine, making
cross-session sharing structurally unobservable. Fixing that was a real
improvement to the fixture, not a bookkeeping change.

## Related frozen limits exercised

`MaxEntriesPerScope = 256` at the boundary; `maxTokens = 512` via a 600-cue
oversized input; the clarification budget's third-repeat escalation (documented
in [CLARIFICATION_POLICY.md](CLARIFICATION_POLICY.md)) deliberately avoided
inside single-turn fixtures.

## Limitations of this evaluation

1. Closed 11-name vocabulary; anything outside it resolves to fallback **by
   construction** — that is a design property, not a measured recognition rate.
2. Fixtures are hand-written and hand-verified; they are a regression net, not a
   sample of real caller language.
3. No accented, code-switched, noisy-ASR or adversarial-speaker corpus.
4. No human evaluation, no inter-annotator agreement, no held-out set.
5. `-race` NOT RUN; the concurrent fixture is behavioural isolation only.
