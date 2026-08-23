# Phase 16 T6 — Pipeline Speak Seam & Greeting Completion

**Status: IMPLEMENTED and VERIFIED LOCALLY.** No stop condition triggered. No
frozen module changed. `-race NOT RUN LOCALLY`.

The T4 blocker is closed: the greeting plan the frozen engine produces from
`EventStart` now reaches the caller, and the conversation is told about it in a
way that is distinguishable from an ordinary turn.

---

## 1. Production changes — five files, all non-frozen

| File | Change |
|---|---|
| `packages/go/voice/pipeline.go` | `Speak`, `instructionFor`, `completionEvent`, `speakRequest`, `deliverPlan` (extracted from `runTurn`), `PipelineConfig.GreetingSystem`, `Pipeline.speaking` |
| `packages/go/voice/fsm.go` | one declared edge: `listening → thinking` |
| `packages/go/voice/errors.go` | one sentinel: `ErrPlanNotSpeakable` |
| `packages/go/voice/fsm_test.go` | the independent expectation map gains the new edge |
| `packages/go/voice/pipeline_test.go` | greeting/abandon counters, generation-spec recording, sink handshake |

**Frozen: none.** `conversation` carries only the Phase 15 T4 amendment
(`+68` lines), unchanged by T6. The other twelve frozen modules are
byte-identical.

## 2. There is one response engine, and it is shared

```
runTurn(transcript)                    Speak(ctx, plan)
        │                                     │
        │ plan := Planner.Handle(utterance)   │ plan is given
        │ planSpeaks(plan.Action)             │ planSpeaks(plan.Action)
        └──────────────┬──────────────────────┘
                       ▼
               deliverPlan(ctx, speakRequest)
        governance → generation → chunking → TTS → Deliver
                       ▼
        completionEvent(plan) → greeting_complete | speech_complete
```

The two producers differ in exactly three fields:

| | `runTurn` | `Speak` |
|---|---|---|
| `messages` | `[{RoleUser, final.Text}]` | `nil` |
| `system` | `cfg.System` | `cfg.GreetingSystem` for a greeting, else `cfg.System` |
| `abort` | `p.ctx` (the session) | the submitter's `ctx` |

`abort` is why the transcript path did not change behaviour: it is the session
context, which the completion tail already checked.

## 3. The completion contract

| Plan | Event | State after | Floor after | Trigger recorded |
|---|---|---|---|---|
| `NextState == StateGreeting` | `EventGreetingComplete` | `listening` | **none** | `greeting_complete` |
| anything else | `EventSpeechComplete` | `listening` | **caller** | `speech_complete` |

Both events are accepted from `StateGreeting`, both reach `listening`, and
`EventGreetingComplete` is accepted mid-conversation as well. **The state cannot
distinguish them.** The trigger and the floor can, and that is what the tests
assert.

## 4. Generation input

`GenerateSpec.Messages` is `nil` for a submitted plan. The frozen kernel treats
that as "assemble from the session's own context" (`kernel.go:573`), so no
transcript is fabricated and no frozen contract changed. The instruction comes
from `PipelineConfig.GreetingSystem`, which has **no default** — this package
authors none of the caller's words.

## 5. Failure, cancellation, concurrency

| Situation | Result |
|---|---|
| governance denial | `EventResponseAbandoned`, no audio, `ErrGovernanceDenied` |
| TTS selection / open failure | `EventResponseAbandoned`, `ErrProviderUnavailable` |
| chunker failure | `EventResponseAbandoned` |
| generation failure | `EventResponseAbandoned`, `ErrProviderFailed` |
| submitter cancels | **neither** completion **nor** abandonment; the session returns to `listening`, outcome `cancelled` |
| barge-in supersedes | no completion; `EventInterrupt` already owns the floor; a non-nil error |
| second `Speak` in flight | refused with `*TransitionError` (`ErrInvalidTransition`) — **not queued** |
| plan does not speak | refused with `ErrPlanNotSpeakable`; nothing allocated |
| before `Start` / after the call | `ErrSessionClosed` |

`EventSpeechComplete`/`EventGreetingComplete` and `EventResponseAbandoned` are
mutually exclusive on every path: each returns before reaching the other.

## 6. A limit worth stating

**`Speak` is only lifecycle-correct for a plan the conversation actually
produced.** Producing a plan is what moves the conversation into the state its
completion is legal from — `EventStart` → `StateGreeting`, `EventUtterance` →
`StateSpeaking`. Submitting a *fabricated* plan delivers audio, but the frozen
engine refuses the completion (the transition is not declared from `listening`),
the pipeline notes it, and no completion is recorded.

Measured, not inferred: a service test that submitted a hand-written plan showed
exactly this, and was rewritten to obtain a real plan from the real
conversation.

## 7. Verification

| | |
|---|---|
| voice tests | 14 new (`speak_test.go`), whole package green |
| service tests | 6 new (`greeting_speech_test.go`), whole suite green (222 s) |
| mutations | 6 designed, **6 CAUGHT**, none inert |
| determinism | `-count=10 -shuffle=on` green |
| `GOWORK=off` | green for both modules |
| gofmt / vet / build | clean |
| third-party deps | zero |
| `-race` | **NOT RUN LOCALLY** — no C compiler; no race-safety claim; CI authoritative |

### Mutation results

| | Mutation | Result | Caught by |
|---|---|---|---|
| M1 | the seam is a no-op | **CAUGHT** | generation/synthesis/delivery counts, and every failure test |
| M2 | greeting instruction dropped | **CAUGHT** | the generation-spec assertion |
| M2b | an empty user message is fabricated | **CAUGHT** | the same assertion's message count |
| M3 | `EventSpeechComplete` sent for the greeting | **CAUGHT** | the trigger-identity assertion — **state alone would not have caught it** |
| M4 | completion announced before delivery finished | **CAUGHT** | the at-event delivery count (0 of 2 frames), stable over 5 runs |
| M5 | completion sent twice | **CAUGHT** | exactly-once, on both the greeting and the transcript path |

## 8. What T6 did NOT do

No call driver. No transport. No `ConversationID` minted in the service, no
`Pipeline` created there, no `EventStart` sent from production code. No session
registry. No change to interruption semantics.

**REAL INBOUND TRANSPORT IS NOT PRESENT.**
**DETERMINISTIC INTERNAL CALL LIFECYCLE ≠ REAL TELEPHONY.**

## 9. Unresolved — compliance, not code

INV-CV-2 makes the opening turn non-yielding because the greeting carries the
lawful-basis announcement, and the frozen turn manager queues a barge-in during
it rather than granting the floor. Whether a caller may lawfully interrupt that
announcement is a product and compliance decision. T6 changed nothing about it.
