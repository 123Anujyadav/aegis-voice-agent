# ADR-0021 — The plan-input seam: `Pipeline.Speak` and greeting completion

**Status:** Accepted (implemented, Phase 16 T6)
**Date:** 2026-08-19
**Supersedes:** nothing. **Depends on:** ADR-0019 (speech-complete ownership),
ADR-0020 (response abandoned).

---

## Context

Phase 16 T4 set out to build the smallest production call driver and stopped
before writing one. The reason was structural: a driver's first act is to open
the conversation, `conversation.EventStart` returns
`Plan{ActionRespond, "greeting"}` — a *spoken* action by the pipeline's own
`planSpeaks` predicate — and nothing in the system could speak it. The speaking
path was entered from exactly one place, a final transcript, and `runTurn` was
unexported.

T4 measured what a partial driver costs: with the greeting undelivered, the
conversation stays in its opening turn, and the caller's first utterance comes
back `action=ignore reason="floor_queued"`. INV-CV-2 makes the opening turn
non-yielding because the greeting carries the caller's lawful-basis
announcement, so the caller is queued behind an announcement that will never
finish. A partial driver is worse than none.

Phase 16 T5 audited the contract and found the design expressible entirely
within existing contracts. This ADR records what T6 implemented.

## Problem statement

How does a plan the conversation has already decided — one with no caller
utterance behind it — reach the caller's ear, and how does the conversation
learn that it did?

## Constraints

- `conversation` is frozen; the Phase 15 `EventResponseAbandoned` amendment is
  the only permitted change and is not extended here.
- No second response engine, no second dialogue policy, no session registry.
- No API key, model, network dependency or external binary.
- The existing transcript path must not regress.
- Greeting *content* may not be authored in this repository.

## Considered options

| | Option | Verdict |
|---|---|---|
| A | **exported method on `voice.Pipeline`** | **CHOSEN** |
| B | a method on `voice.SessionFSM` | rejected — a passive state holder with no planner, no generator, no delivery |
| C | a callback from `conversation` into `voice` | rejected — inverts the dependency; `conversation` is frozen and sits below `voice` |
| D | export `runTurn` | rejected — its contract is a `speech.TranscriptSegment`; exporting it exports the wrong thing |
| E | orchestrate in the service | rejected — the service would need its own governance → generation → chunking → synthesis → delivery sequence, which is a second response engine |

## Decision

**1. The seam is `func (p *Pipeline) Speak(ctx context.Context, plan conversation.Plan) error`.**

It runs the same path `runTurn` runs. Both now call one unexported
`deliverPlan`, which differs between them in three fields: the generation
messages, the system instruction, and the external cancellation source.

**2. The completion event is chosen per plan, from `Plan.NextState`.**

`StateGreeting` → `EventGreetingComplete`; anything else →
`EventSpeechComplete`. This is the typed discriminator the frozen contract
already provides — `handleStart` returns `StateGreeting`, every ordinary
speaking plan returns `StateSpeaking`. No string is compared.

This choice is load-bearing and invisible in the state. Both events are accepted
from `StateGreeting`, both reach `StateListening`, and `EventGreetingComplete`
is *also* accepted mid-conversation. What differs is the recorded trigger and
the floor: greeting-complete **releases** the agent's turn to nobody, while
speech-complete **hands** the floor to the caller as the answer to something
they said.

**3. The greeting is generated with no messages and a configured instruction.**

`GenerateSpec.Messages` is left empty — the frozen kernel assembles from the
session's own context when it is (`runtime/kernel.go:573`), which is the
contract's own way of expressing "no input this turn". A fabricated empty user
message is not used. The instruction comes from a new optional
`PipelineConfig.GreetingSystem`, which has **no default**: nothing in this
repository holds greeting text, and a wrong opening line is a compliance defect
rather than a bug.

**4. One new session-FSM edge: `listening → thinking`.**

The declared cycle assumed every response answers something the caller said. An
agent-initiated turn does not, so the edge is declared with its rationale.

**5. One new error sentinel: `ErrPlanNotSpeakable`.**

Every other error `Speak` can return says something failed out there; this one
says the caller asked for the wrong thing. Folding it into a provider error
would send an operator to look at a working synthesiser.

## Why this option was selected

A constraint decided it rather than a preference. The `voice.Planner` port is
`Handle(Event) (Plan, error)` and exposes **no state accessor**, so the pipeline
cannot inspect the conversation to decide which completion event to send. It can
only know from the plan it was handed. That makes the component that receives
the plan and the component that owns the completion the same component.

## Trade-offs

- **A wider state table.** A session may now enter `thinking` with no evidence
  that anybody spoke. That is what an agent-initiated turn is; the alternatives
  were a separate greeting state (duplicating the response cycle) or speaking
  without moving the machine (leaving the barge-in detector unable to see that
  the agent holds the floor).
- **`Speak` is only lifecycle-correct for plans the conversation produced.**
  Producing a plan is what moves the conversation into the state its completion
  is legal from. A fabricated plan is refused by the frozen engine at completion
  time — noted, not fatal, and measured.
- **One at a time, refused rather than queued.** The pipeline holds exactly one
  active dispatcher and one active synthesis stream. A queued second response
  would be spoken into a conversation that had moved past it.

## Alternatives rejected

Emitting `EventGreetingComplete` for every submitted plan "works" — it is
accepted everywhere and always reaches listening — while recording an opening
turn in the middle of a call and releasing the floor to nobody. Rejected
precisely because it is invisible in the state.

## Operational impact

The greeting now consumes a turn (`TurnsStarted` / `TurnsCompleted`), a
governance decision, one generation and one synthesis stream. Deployments that
want a controlled opening line must set `GreetingSystem`.

## Security impact

None added. No transcript or PCM reaches a log or a metric label, no credential
is introduced, nothing is persisted, no queue is unbounded, and no package-level
mutable session state exists. The greeting carries no caller content by
construction — there is no caller input yet.

## Cost / performance / scalability impact

One additional generation and synthesis per call, at the start. No new
dependency, no new goroutine class, no shared state between sessions.

## Migration strategy

Additive. Existing callers are unaffected: the transcript path's behaviour is
unchanged, including its completion event and its cancellation semantics.

## Risks

1. **Greeting content is unconfigured by default**, so an unconfigured
   deployment generates its opening turn from the session instruction. Mitigated
   by documentation, not by a default string.
2. **The lawful-basis barge-in question is unresolved** — see below.

## Future review trigger

Review when a second agent-initiated plan kind appears (a proactive prompt, a
hold message, an escalation announcement), or when a real telephony transport is
integrated and the greeting's timing relative to call answer becomes observable.

## Unresolved, and deliberately not decided here

INV-CV-2 makes the opening turn non-yielding because the greeting carries the
lawful-basis announcement, and the frozen turn manager *queues* a barge-in
during it rather than granting the floor. Whether a caller may lawfully cut off
that announcement is a **compliance question, not a technical one**. T6 changed
no interruption semantics, and this ADR does not answer it.

## References

- `packages/go/voice/pipeline.go` — `Speak`, `deliverPlan`, `completionEvent`,
  `instructionFor`
- `packages/go/voice/fsm.go` — the `listening → thinking` edge
- `packages/go/conversation/engine.go:534,549` — `handleStart`,
  `handleGreetingComplete`
- `packages/go/runtime/kernel.go:573` — empty-`Messages` context assembly
- `docs/phase16/GREETING_SPEECH_CONTRACT.md` — the T5 audit this implements
- `docs/phase16/CALL_DRIVER_AND_GREETING.md` — the T4 stop condition it closes
