# ADR-0019 — Ownership of the agent speech-completion boundary

- **Status:** Proposed — **partially blocked pending a product decision** (Phase 15 T3, audit only)
- **Date:** 2026-08-18
- **Supersedes:** nothing
- **Related:** [ADR-0017](0017-service-wiring.md), [ADR-0018](0018-voice-provider-architecture.md),
  [Phase 15 speech-complete audit](../phase15/SPEECH_COMPLETE_AUDIT.md)

## Context

Phase 15 T2 connected the real `voice.Pipeline` to the real
`conversation.Conversation` for the first time and measured a defect: three
consecutive turns produced three `completed/ok` outcomes but only **one**
generation and **one** synthesis. In a real call that is one spoken response
followed by silence.

The audit established why. `conversation/engine.go:24` defines
`EventSpeechComplete` as **“the agent finishing its output”**, and
`handleSpeechComplete` is the only thing that performs the agent→caller floor
handoff: `Release(PartyAgent)` → `transition(...)` → `Acquire(PartyCaller)`.

Production sends that event from nowhere. There is exactly **one**
`Planner.Handle` call in all of `packages/go/voice` (`pipeline.go:942`) and it
always sends `EventUtterance`. **The pipeline drives 1 of 11 conversation
events.**

## Problem statement

Who emits `EventSpeechComplete`, and at which instant, such that the dialogue
floor returns to the caller after the agent has spoken — without duplicating a
transition that interruption or cancellation already performs?

## Constraints

1. No frozen module may change.
2. No new state machine and no second interruption mechanism.
3. The event must not fire when the agent did not actually finish speaking.
4. `Handle` refuses a terminal conversation with `ErrTerminal`; nothing may
   depend on emitting after termination.

## Considered options

**A. `voice.Pipeline` owns it.** The pipeline is the only component that
observes the last delivered frame. `pipeline.go:1139` is already reached only
after delivery completes and only when the generation guard shows the turn was
not superseded.

**B. `SessionFSM` owns it.** Rejected: it is a passive state holder driven *by*
the pipeline, holds no `Planner`, and giving it one inverts the dependency.

**C. The speech/TTS controller owns it.** Rejected: `audiobridge.Adapter`
implements `Interrupt`/`EndOfSpeech` — an interruption seam, and `EndOfSpeech`
is the *caller’s* boundary, the opposite one. TTS stream closure means synthesis
finished, not that audio reached the caller.

**D. An existing event already covers it.** Rejected on evidence:
`voice.EventBargeIn` is observability and `OnTurnComplete` is an observer
callback; neither moves the conversation floor.

## Decision

**Adopt Option A for the successful-speech path: `voice.Pipeline` emits
`EventSpeechComplete` at `pipeline.go:1139`, inside the existing generation
guard, alongside the existing `FSM.To(StateListening, ReasonOK)`.**

**Explicitly NOT decided here — deferred to the product owner:** what event, if
any, should end a turn in which the plan said *respond* but the agent never
spoke (governance denial, generation failure, synthesis failure). See §Risks and
the audit’s §11.1. This ADR is **not** approval to invent that behaviour.

## Why this option was selected

The boundary is already computed in exactly the right place. Line 1139 is
reached only after the final synthesised frame is delivered, and only when the
turn has not been superseded — so the two paths that must *not* produce a
completion, barge-in and cancellation, are excluded for free by guards that
already exist (`p.ctx.Err()` at :1120, generation mismatch at :1127).

Every alternative owner would need a `Planner` reference it has no business
holding, or would fire at a boundary that is not the one the frozen event names.

## Trade-offs

- **Accepted:** the pipeline’s coupling to `conversation` widens from one event
  to two. It already imports the package and holds the `Planner`, so this is a
  larger use of an existing dependency, not a new one.
- **Accepted:** this fixes only the success path. Denied and failed turns remain
  floor-stuck until §11.1 is decided — that is a deliberate, reported limit, not
  an oversight.
- **Gained:** multi-turn works without a test or a service compensating for the
  pipeline, which is what T2 had to do.

## Alternatives rejected

- **Emitting from the test/service layer**, as T2 did to obtain evidence. That
  makes every consumer reimplement floor management and guarantees divergence.
- **Emitting unconditionally at turn end**, ignoring the generation guard. This
  would double-transition after a barge-in, where `EventInterrupt` has already
  force-yielded the floor.

## Operational impact

None until implemented. Once implemented, a conversation reached through the
pipeline returns to `awaiting_caller` after each spoken turn instead of
remaining in `speaking`.

## Security impact

None. The event carries a bounded reason code and no caller content.

## Cost / performance impact

One additional synchronous `Handle` call per completed turn. Phase 14 measured a
whole intelligence turn at ~7–9 µs; `handleSpeechComplete` performs a floor
release, a state transition and an acquire, so the addition is immaterial
against a turn that has just streamed audio.

## Migration strategy

Single call site. The T2 rig’s explicit floor return is removed in the same
change, which is how the fix is proven: that compensation exists today **only**
because the pipeline does not do this.

## Risks

| Risk | Mitigation |
|---|---|
| Double transition with a barge-in | emit inside the generation guard; `EventInterrupt` owns that path |
| Emission after termination | frozen `Handle` returns `ErrTerminal`; pipeline returns at `p.ctx.Err()` first |
| A future service also sends it | the pipeline becomes the sole owner; state it in the package doc |
| **Denied/failed turns stay floor-stuck** | **UNRESOLVED — product decision (§11.1)**; `EventSpeechComplete` would claim speech that never happened, and `EventFault` forces `StateError` although governance denial is explicitly “a domain outcome, not a fault” |

## Future review trigger

Revisit when **either** the §11.1 decision is taken, **or** a second production
driver of conversation events appears (a service-level session driver owning
`EventStart`, `EventGreetingComplete`, `EventInterrupt`, `EventSilence`,
`EventHangup` — none of which anything sends today).

## References

- `packages/go/conversation/engine.go:24` — event definition
- `packages/go/conversation/engine.go:559` — `handleSpeechComplete`
- `packages/go/conversation/engine.go:459` — `ErrTerminal` guard
- `packages/go/voice/pipeline.go:942` — the only `Planner.Handle` call
- `packages/go/voice/pipeline.go:1120,1127,1139` — cancellation guard, generation guard, completion point
- `packages/go/voice/bargein.go` — contains no `conversation` reference
- [Phase 15 speech-complete audit](../phase15/SPEECH_COMPLETE_AUDIT.md)
