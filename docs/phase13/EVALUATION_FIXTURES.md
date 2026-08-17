# Phase 13 — T13 Evaluation Fixtures

## What this suite is, and what it is not

**Is:** a fixed, ordered inventory of known inputs, each declaring the exact
typed outcome the existing Phase 13 architecture must produce. Every expectation
was **measured** against the real classifier and the real bridge before being
written down.

**Is not:** an NLU accuracy benchmark. `intent.Classifier` is a deterministic,
closed-vocabulary rule matcher. A fixture pass rate means *"these known inputs
still produce these known typed outcomes"* — nothing more. It says nothing about
real-world language understanding, and **no number here may be reported as
accuracy**. No model, provider, network service or credential is involved.

Fixture correctness and language-understanding quality are different claims.
This suite makes only the first.

## Four outcomes, deliberately kept apart

The frozen engine distinguishes **four** outcomes where the task named three.
Collapsing any pair would be a regression, so
`TestT13_FourOutcomesStayDistinct` asserts all four differ:

| Outcome | Classifier result | Plan action | Reason | Clarification |
|---|---|---|---|---|
| unknown | 0 candidates | `ActionRespond` | `fallback` | `ClarifyNone` |
| below-reject | 1 cand @0.333 (< 0.45) | `ActionEscalate` | `intent_rejected` | `ClarifyNone` |
| low confidence | 1 cand @0.667 | `ActionConfirm` | `clarify_low_confidence` | `ClarifyLowConfidence` |
| ambiguous | 2 cands @1.0, margin 0 | `ActionClarify` | `clarify_ambiguous` | `ClarifyAmbiguous` |

**Reachability note.** Confidence is `evidence / saturation`, so with the real
lexicon the attainable values are 0.333 (one single-token cue), 0.667 (two) and
1.0 (a phrase cue, or three singles). 0.667 is what makes the low-confidence
band reachable through genuine inputs at all — without it the band would only be
testable with synthesised `conversation.Intent` values.

## Fixture inventory

### Table fixtures (`evaluationFixtures()` — order is fixed and asserted)

| # | Fixture | Category | Input | Cands | Conf | Action | Reason | Clarify | Slots | Terminal |
|---|---|---|---|---|---|---|---|---|---|---|
| 0 | `normal/callback_with_number` | normal | "please call me back on 9876543210" | 1 | 1.0 | respond | `intent_accepted` | none | 2 | no |
| 1 | `normal/transfer` | normal | "transfer me to rajesh" | 1 | 1.0 | respond | `intent_accepted` | none | 1 | no |
| 2 | `ambiguous/hold_and_callback` | ambiguous | "hold on call back" | 2 | 1.0 | clarify | `clarify_ambiguous` | ambiguous | 0 | no |
| 3 | `clarification/missing_message_body` | clarification | "i want to leave a message" | 1 | 1.0 | ask | `clarify_missing_slot` | missing_slot | 1 | no |
| 4 | `clarification/slot_like_but_invalid` | clarification | "my number is not-a-number call me back" | 1 | 1.0 | ask | `clarify_missing_slot` | missing_slot | 2 | no |
| 5 | `low_confidence/repeat` | low_confidence | "repeat pardon" | 1 | 0.667 | confirm | `clarify_low_confidence` | low_confidence | 0 | no |
| 6 | `low_confidence/hold` | low_confidence | "wait ruko" | 1 | 0.667 | confirm | `clarify_low_confidence` | low_confidence | 0 | no |
| 7 | `below_reject/single_weak_cue` | low_confidence | "callback transfer" | 1 | 0.333 | escalate | `intent_rejected` | none | 2 | **yes** |
| 8 | `malformed/empty` | malformed | "" | 0 | — | respond | `fallback` | none | 0 | no |
| 9 | `malformed/whitespace_only` | malformed | "   \t\n  " | 0 | — | respond | `fallback` | none | 0 | no |
| 10 | `malformed/control_bytes` | malformed | "\x00\x01\x02" | 0 | — | respond | `fallback` | none | 0 | no |
| 11 | `malformed/unknown_vocabulary` | malformed | "zzzz qqqq wubble frotz" | 0 | — | respond | `fallback` | none | 0 | no |

Every declared field is asserted, including zero values — "expect no
clarification" is as much a claim as "expect ambiguity". Intent names are
additionally checked against the closed vocabulary.

### Dedicated fixtures

| Category | Test | Asserted outcome |
|---|---|---|
| interruption | `TestT13_TurnFixtures/interruption/barge_in` | `InterruptionUser`, lifecycle `IntentActive` |
| acknowledgement | `TestT13_TurnFixtures/acknowledgement/backchannel` | `FloorBackchannel`, `InterruptionNone`, lifecycle unchanged |
| cancellation | `TestT13_TurnFixtures/cancellation/never_mind` | lifecycle `IntentAbandoned` |
| silence | `TestT13_TurnFixtures/silence/window` | `EventSilence`, lifecycle unchanged, `ClarifyNone` |
| multi-turn | `TestT13_MultiTurnProgression` | 4-step progression, context survives, `last_intent` per turn, non-terminal |
| context eviction | `TestT13_ContextEvictionFixture` | bound 256 held, exactly 1 victim, newest survives |
| concurrent sessions | `TestT13_ConcurrentSessionFixture` | 12 sessions on one shared bridge, results match serial baseline, markers isolated |
| oversized input | `TestT13_OversizedInputRespectsTokenBound` | 600 cues score identically to one; `maxTokens` 512 |

## Frozen limits respected

- Conversation length bound — the default persona sets `MaxTurns: 40`
  (`conversation/persona.go:138`), counting turns for **both parties**
  (`TurnManager.Count()` is `len(history)`, `turn.go:469`), i.e. **20 caller
  round-trips**. The multi-turn fixture uses 4 steps and asserts non-terminal.
  *(Earlier checkpoints recorded this as "MaxTurns = 20", which was the observed
  caller-turn count rather than the configured value — see
  [CLARIFICATION_POLICY.md](CLARIFICATION_POLICY.md).)*
- Clarification budget — the unfilled-slot fixture asserts a single turn's
  outcome, never a repeat loop (repeating escalates on the 3rd attempt).
- `MaxEntriesPerScope = 256` — exercised exactly at the boundary.
- `maxTokens = 512` — the oversized fixture sits at 600 cues, past the bound.

## Eviction: what is and is not guaranteed

Per T7/T11, `evictOldestLocked` compares with `Before()`, which is false for
equal timestamps, so the victim among tied entries is unspecified.

- **Tied timestamps** (`tied_timestamps_bound_only`): asserts the bound holds,
  exactly one entry is evicted, and the newest survives. **Does not assert which
  key was evicted** — that would be asserting an accident of map iteration.
- **Distinct timestamps** (`distinct_timestamps_victim_is_oldest`): clock
  advanced per insert, so victim identity *is* determined and is asserted
  (`k0000` evicted, `k0001` retained).

No frozen code was modified.

## Determinism methodology

- `TestT13_FixturesAreDeterministic` runs the whole inventory **26 times** and
  requires an identical signature.
- Fixture **order** is separately asserted stable across 10 constructions.
- Signatures contain only typed logical outcomes — no timestamps, durations,
  goroutine identities or scheduler ordering.
- No sleeps, no wall-clock pass/fail logic, no random data.
- T11's golden was re-run after T13 to confirm the fixtures did not perturb it.

## Mutation verification

| | Mutation | Result | Caught by |
|---|---|---|---|
| M1 | callback rule mislabelled as `repeat` | CAUGHT | `EvaluationFixtures/normal/...`, `MultiTurnProgression` |
| M2 | candidate cap 5→1 (ambiguity collapses) | CAUGHT | `ambiguous/hold_and_callback` |
| M3 | "never mind" cue removed | CAUGHT | `TurnFixtures/cancellation/never_mind` |
| M4 | eviction bound disabled via config | CAUGHT | both eviction sub-fixtures |
| M5 | all sessions collapsed onto one context | CAUGHT | `ConcurrentSessionFixture` marker checks |
| M6 | saturation 3.0→1.5 (low-conf promoted) | CAUGHT | `low_confidence/repeat`, oversized bound |

All mutations were reverted; the four touched files were verified
byte-identical to their snapshots afterwards. No mutation remains in the tree.

## Limitations

1. **Not an accuracy measure.** See the opening section.
2. **`-race` not run** — no C compiler locally. The concurrent fixture is a
   behavioural isolation test, not race-detector evidence.
3. **Closed vocabulary.** Fixtures exercise the 11-name lexicon; inputs outside
   it resolve to fallback by construction, which is a design property, not a
   measured recognition rate.
4. **Two initial mutation misses** (M1 non-compiling, M5 inert) were corrected;
   the M5 miss revealed and fixed a real gap in the concurrent fixture.
