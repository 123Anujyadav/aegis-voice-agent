# ADR-0020 — Releasing the floor when a planned response is never spoken

- **Status:** Proposed — **BLOCKED pending a product decision and frozen-module approval** (Phase 15, decision audit only; nothing implemented)
- **Date:** 2026-08-18
- **Supersedes:** nothing
- **Related:** [ADR-0019](0019-speech-complete-ownership.md),
  [Response-failure semantics audit](../phase15/RESPONSE_FAILURE_SEMANTICS.md)

## Context

ADR-0019 gave the pipeline ownership of `EventSpeechComplete`, which fixed the
successful path: after the agent speaks, the floor returns to the caller and
multi-turn works.

It deliberately left one case open. When an intent is accepted and a response is
planned but **never spoken** — governance denial, generation failure, synthesis
failure — the conversation is left in `speaking` with the floor held by the
agent. Measured consequence: every later caller utterance is classified as a
**backchannel and ignored**. The caller is unheard for the remainder of the call
and nothing reports an error.

## Problem statement

How is the agent's floor released when the planned response will not be spoken,
without claiming speech that did not happen or an interruption that did not
happen?

## The controlling finding

Exhaustively, the frozen engine moves the floor in seven places, and only two are
reachable here: `handleSpeechComplete` (`Release(PartyAgent)` + `Acquire(PartyCaller)`)
and `handleInterrupt` (`ForceYield`). `transition()` moves state only and never
touches the floor (`engine.go:393`).

**No other existing event can restore the floor, whatever state it reaches.**

## Considered options

**A. Keep the current behaviour.** The caller is silently unheard. Rejected: it
fails invisibly, which is the worst property a call path can have.

**B/C. `EventFault`.** Traced, not judged by name. It reaches `StateError`, and
`Recover()` reaches `StateListening` only when a context snapshot exists —
**nothing in production ever calls `TakeSnapshot`**, so it always takes the
`no_snapshot` branch and **escalates**, terminating the call. Worse, **neither
`EventFault` nor `Recover()` touches the floor**, so even the unreachable happy
branch leaves the caller unheard. It also mislabels governance denial, which the
engine explicitly treats as *"a domain outcome, not a fault"*. Rejected: does not
solve the problem.

**D. `EventSpeechComplete` for failures.** Restores the floor, but asserts the
agent finished output when nothing was said, and derives the next state from the
previous plan's `Expectation` — leaving the engine awaiting an answer to a
question the caller never heard. Rejected as false.

**E. A new `EventResponseAbandoned`.** Meets every bar: the contract cannot
express the semantic today; the floor can be restored with the existing
`Release`/`Acquire`; **no new state machine is needed because
`StateSpeaking → StateListening` is already a declared transition**
(`state.go:250-253`); one unambiguous meaning; existing speech-complete and
interruption semantics untouched.

**F. Terminate on every failed response.** Honest but drops the call over a
transient synthesiser hiccup. Rejected.

## Decision

**Recommend Option E**, and **do not implement it yet**.

Two approvals are required first, and neither is an engineering call:

1. **Product:** should a denied or failed response release the floor (the caller
   may speak again, having heard nothing), or escalate to a human?
2. **Governance of the frozen contract:** is amending `packages/go/conversation`
   acceptable, or is the stuck call preferable to touching a frozen module?

## Why this option would be selected

It is the only option that both restores the floor and tells the truth. B and C
do not restore the floor at all, so the defect survives them; D restores it by
asserting something false; A and F are the two extremes of doing nothing and
doing too much.

The semantics are narrow enough to state in one sentence — *"the planned response
will not be spoken, so the agent's floor is released"* — which is the test for
whether an event deserves to exist.

## Trade-offs

- **Accepted:** it amends a frozen module. That is precisely why this ADR stops
  rather than implements.
- **Accepted:** it releases the floor but says nothing to the caller, who
  experiences silence and must repeat themselves. Whether an apology or retry
  should precede it is policy that belongs above this event — and note the
  component that would generate that apology is the one that just failed.
- **Gained:** the silent stuck-call condition disappears, and the three failure
  paths become observable as a distinct trigger in the transition history rather
  than as an absence.

## Minimum frozen surface, if approved

| File | Change |
|---|---|
| `conversation/engine.go` | `EventResponseAbandoned` appended **at the end** of the `EventKind` block — the block is `iota`-based, so inserting elsewhere renumbers every later event |
| `conversation/engine.go` | one case in `EventKind.String()`; one case in `Handle`'s switch |
| `conversation/engine.go` | `handleResponseAbandoned()` — `Release(PartyAgent, ExpectNothing)`, `transition(StateListening, TriggerResponseAbandoned, reason)`, `Acquire(PartyCaller, false)` |
| `conversation/state.go` | one `TriggerResponseAbandoned` constant |
| `conversation/*_test.go` | tests |

**No transition-table change.** No exhaustiveness test over `EventKind` exists
that would also need updating.

**Critically, the handler must transition to plain `StateListening`, not
`stateForExpectation(last.Expectation)`.** The agent never asked the question, so
the caller cannot be answering it.

Emission from `voice/pipeline.go` on the three failure branches is a separate,
non-frozen change requiring its own approval.

## Operational impact

None until implemented. Afterwards, a failed response is visible in the
transition history as `TriggerResponseAbandoned` instead of being an absence of
events.

## Security impact

None. The event carries a bounded reason code and no caller content.

## Risks

| Risk | Mitigation |
|---|---|
| Used as a general-purpose floor reset | one meaning, documented at the definition; tests assert it never appears on the success path |
| Confused with interruption | it writes nothing to `Interruptions().History()`; a test asserts that |
| Caller hears silence and repeats themselves | accepted, and named above as a product question |
| Denial and infrastructure failure need different follow-ups | the event releases the floor only; policy sits above it |

## Future review trigger

Revisit when **either** the two approvals above are given, **or** a production
component begins calling `ContextEngine.TakeSnapshot`, which would make
`Recover()`'s restore branch reachable and warrant re-examining Option C.

## References

- `packages/go/conversation/engine.go:393` — `transition` does not move the floor
- `packages/go/conversation/engine.go:516,526,564,581,597,782,792` — every floor movement
- `packages/go/conversation/engine.go:803` — `Recover`, and its `no_snapshot` escalation
- `packages/go/conversation/context.go:326` — `TakeSnapshot`, called by nothing in production
- `packages/go/conversation/state.go:250-253` — `StateSpeaking → StateListening` already declared
- `packages/go/conversation/planner.go:269` — `stateForExpectation`
- [Response-failure semantics audit](../phase15/RESPONSE_FAILURE_SEMANTICS.md)
