# Phase 15 T3 — Speech-Complete Ownership & Multi-Turn Voice Flow Audit

**Status:** AUDIT ONLY. No production Go change, no test added, no frozen module
touched. Baseline `beb46df` + the approved T2 one-line fix.

---

## 1. What `EventSpeechComplete` actually means

Read from the frozen definition, not assumed:

> `conversation/engine.go:24` — **“EventSpeechComplete is the agent finishing its output.”**

It is an **AGENT-speech boundary**. It is **not** the STT final transcript, **not**
the caller falling silent, and **not** TTS stream closure per se — it is the
moment the agent has finished producing output for its turn.

`handleSpeechComplete` (`engine.go:559`) does exactly four things:

1. `turns.Release(PartyAgent, last.Expectation)` — the agent gives up the floor
2. computes the next state from the **previous plan's** `Expectation`, with
   `ActionEnd`/`ActionEscalate`/`ActionTransfer` overriding to a terminal state
3. `transition(next, TriggerSpeechComplete, last.Reason)`
4. `turns.Acquire(PartyCaller, false)` — **the caller takes the floor**

and returns `ActionWait` / `"awaiting_caller"`.

**This event *is* the agent→caller floor handoff.** Nothing else performs it.

## 2. Event producer / consumer matrix

Producers are as of today's production source.

| Event | Meaning (frozen doc) | Produced in production by | Conversation effect | Floor after |
|---|---|---|---|---|
| `EventStart` | begins the conversation | **nobody** | opens non-yielding greeting turn | agent |
| `EventGreetingComplete` | ends the opening turn | **nobody** | greeting → listening | caller |
| `EventUtterance` | completed caller contribution | **`voice.Pipeline` (the only one)** | classify → plan | agent (on respond) |
| `EventOverlap` | caller audio while agent holds floor | **nobody** | backchannel / queued / barge-in | unchanged on backchannel |
| `EventSpeechComplete` | **agent finishing its output** | **nobody** | release agent, acquire caller | **caller** |
| `EventSilence` | detected silence window | **nobody** | `ActionWait` / `"silence"` → waiting | none |
| `EventInterrupt` | explicit interruption | **nobody** | `Raise` + force-yield | caller |
| `EventToolComplete` | resume from external action | **nobody** | → thinking, respond | agent |
| `EventTimeout` | inactivity / budget expiry | **nobody** | → `StateTimeout` (terminal) | — |
| `EventFault` | internal or upstream failure | **nobody** | → `StateError` (**not** terminal) | — |
| `EventHangup` | caller ended the call | **nobody** | → `StateEnded` (terminal) | — |

**Measured fact:** there is exactly **one** `Planner.Handle` call in all of
production `packages/go/voice`, at `pipeline.go:942`, and it always sends
`EventUtterance`.

> **The pipeline drives 1 of 11 conversation events.** The missing
> `EventSpeechComplete` is the most damaging omission, but it is one instance of
> a broader ownership gap.

## 3. Two independent “floors”, and why they diverge

| | `voice.SessionFSM` | `conversation.TurnManager` |
|---|---|---|
| Scope | session lifecycle | dialogue floor |
| States | created, listening, speaking_detected, transcribing, thinking, synthesizing, speaking, interrupted, cancelled, failed, completed | holder = none / caller / agent |
| Moved by | `Pipeline` calling `FSM.To(...)` | **conversation events only** |
| Moved by the pipeline today | **yes**, faithfully | **no**, except via `EventUtterance` |

`SessionFSM` is a passive state holder: `To()` is called *by* the pipeline. It
observes no conversation events and cannot emit them.

**The comment at `pipeline.go:1128` — “the interruption … returned the floor to
the caller” — refers to the SessionFSM, not to the conversation.** Verified:
`bargein.go` contains no reference to `Planner` or `conversation`.

## 4. Pipeline lifecycle trace, and exactly where it stops

```
inbound media.Frame
  → Intel.Analyze                       (VAD.OnsetConfirmed → open STT)   pipeline.go:648,657
  → STTStream.Write / partial segments
  → Endpoint.Confirmed → CloseSend                                        pipeline.go:705
  → final TranscriptSegment
  → Planner.Handle(EventUtterance)      ← THE ONLY conversation event      pipeline.go:942
  → Plan
      ├ action != respond → turn ends quietly (no conversation event)      pipeline.go:952
      └ action == respond
          → Governor.Decide                                               pipeline.go:976
              ├ denied → FSM.To(listening) + OnTurnComplete(denied)        pipeline.go:1004
              └ allowed
                  → Generator.Generate → synthesisSink.Write              pipeline.go:1079
                  → Chunker → TTSStream.Synthesize                        pipeline.go:1232
                  → audio frames → Output.Deliver                         pipeline.go:1336
                  → generation guard
                      ├ superseded (barge-in) → OnTurnComplete(interrupted); return   :1127
                      └ current → FSM.To(StateListening, ReasonOK)
                                  OnTurnComplete(completed, ok)            pipeline.go:1139
                                  ▲
                                  └── ✗ STOPS HERE.
                                      The pipeline returns ITS OWN state to
                                      listening and never tells conversation.
```

**The pipeline already knows the boundary precisely.** Line 1139 is reached only
after the last synthesised frame has been delivered and only when the generation
guard shows the turn was not superseded. That is exactly “the agent finished its
output”.

## 5. The observed failure

Measured in T2: three consecutive turns produced **three `completed/ok`
outcomes but only ONE generation and ONE synthesis**. Turns 2 and 3 were silent
no-ops, because the conversation still had the agent holding the floor and no
longer planned a response.

In a real call this means **one spoken response, then silence**.

## 6. Ownership options

### Option A — `voice.Pipeline` owns it ✅ RECOMMENDED (success path)

- **Semantics:** correct. The pipeline is the only component that observes the
  last delivered frame; “agent finished output” is precisely what line 1139 means.
- **Ordering:** emit immediately before/at the existing `FSM.To(StateListening,
  ReasonOK)`, inside the generation guard.
- **Interruption:** naturally excluded — the barge-in branch returns at :1127
  before reaching it.
- **Cancellation:** naturally excluded — `p.ctx.Err() != nil` returns at :1120.
- **Duplicate risk:** none today (no production caller sends it); the pipeline
  would become the sole owner by construction.
- **Frozen impact:** **NONE.** `packages/go/voice` is not frozen and already
  imports `conversation` and holds the `Planner`.
- **Testability:** high — the T2 rig already observes floor holder, turn count
  and per-turn generation counts.

### Option B — `SessionFSM` owns it ❌

`SessionFSM` is a passive state holder driven *by* the pipeline; it holds no
`Planner` reference and knows nothing about the conversation. Giving it one
would invert the dependency and put dialogue knowledge in a session-lifecycle
type.

### Option C — speech / TTS controller owns it ❌

`audiobridge.Adapter` implements `Interrupt`/`EndOfSpeech` — an **interruption**
seam, not a completion seam. `EndOfSpeech` is the *caller’s* turn ending, the
opposite boundary. `TTSStream` closure means synthesis finished, not that the
audio reached the caller; frames may still be queued in `MaxPendingAudio`.

### Option D — an existing event already covers it ❌

Checked: `voice.EventBargeIn` is a `VoiceEvent` (observability only) and
`OnTurnComplete` is an observer callback. **Neither moves the conversation
floor.** No existing event performs `Release(agent)` + `Acquire(caller)`.

## 7. Correct multi-turn sequence, from code

```
caller audio
  → VAD.OnsetConfirmed        → STT stream opens
  → partial segments
  → Endpoint.Confirmed        → CloseSend → final segment
  → EventUtterance            → conversation: listening → thinking → speaking, floor = AGENT
  → Plan(respond)
  → governance ALLOW
  → generation → chunker → TTS → frames delivered
  → EventSpeechComplete       ← THE MISSING STEP
      conversation: Release(agent) → transition(stateForExpectation) → Acquire(caller)
      floor = CALLER, ActionWait/"awaiting_caller"
  → next caller audio accepted
```

## 8. Interruption semantics — existing mechanism is sufficient

If the caller interrupts during generation, synthesis or output, `audiointel`
calls `interruptController.Interrupt`, which bumps the generation counter,
cancels via the existing `SpeechController`, aborts the turn, and moves the
SessionFSM interrupted → listening.

**Requirement:** `EventSpeechComplete` must **not** be emitted on that path. The
correct conversation event for a barge-in is `EventInterrupt`, which already
exists and already force-yields the floor. Emitting both would be a double floor
transition. The existing generation guard at :1127 makes this exclusion free.

**Separate gap, out of T3 scope:** the pipeline does not send `EventInterrupt`
to the conversation either, so `InterruptionEngine` currently records nothing in
production.

## 9. Cancellation semantics

`Handle` returns `ErrTerminal` immediately when the conversation is terminal
(`engine.go:459`), so a completion event after cancellation is refused by the
frozen engine rather than mis-applied. The pipeline’s own `p.ctx.Err()` check at
:1120 already returns before the completion point.

**Conclusion: `EventSpeechComplete` must NOT be emitted after cancellation or
session termination**, and the existing guards already achieve that.

## 10. Frozen-module impact — NONE

| Module | Change required |
|---|---|
| conversation, speech, runtime, governance, memory, toolruntime, audiointel, audiobridge, media, metrics, evaluation, evalsubjects, telephony | **none** |

The future change belongs in exactly one non-frozen file: **`packages/go/voice/pipeline.go`**.

## 11. UNRESOLVED — requires a product/architecture decision

**These are not omissions in this audit; they are genuine forks that source
evidence does not settle.**

### 11.1 What ends a turn where the plan said *respond* but the agent never spoke?

Reachable today: governance denial (:1004), generation failure, TTS failure. In
each, the conversation is in `StateSpeaking` with the floor held by the agent,
and something must return it.

| Candidate | Argument for | Argument against |
|---|---|---|
| `EventSpeechComplete` | correctly releases the floor; `handleSpeechComplete` reads the last plan and lands in the right state | claims the agent finished output **when nothing was spoken**; the conversation may then expect an answer to a question the caller never heard |
| `EventFault` | honest that the turn failed | forces `StateError`; **governance denial is explicitly “a domain outcome, not a fault”**, so this mislabels the commonest case |
| leave it | no invented semantics | floor stays stuck — the very defect under audit |

**Decision required.** I am not choosing silently.

### 11.2 Who owns the other ten conversation events?

Nothing in production sends `EventStart` or `EventGreetingComplete`, so a
conversation reached through the pipeline is never opened (the T2 rig opened it
manually through the Bridge). `EventInterrupt`, `EventSilence` and `EventHangup`
are likewise unsent. Whether the pipeline should own all of these, or a
service-level session driver should, is an architecture decision beyond
“speech-complete”.

## 12. FakeSTT limitation — classification **C**

**C — CAN BE SOLVED WITH A LOCAL TEST DOUBLE WITHOUT TOUCHING FROZEN CODE.**

`speech.FakeSTTProvider` copies its whole script into every stream
(`harness.go:109`) and flushes the remainder on `CloseSend` (`:196`), so
per-turn scripts are impossible. But `speech.STTProvider`/`STTStream` are small,
exported interfaces (`OpenSTT`; `Write`/`Results`/`CloseSend`/`Close`), and
`packages/go/voice`’s own `e2e_test.go` already implements a local `recordingSTT`
against them.

A **test-only** provider in the Phase 15 test package can therefore hand out a
different script per opened stream with no frozen change and no new production
abstraction. **Not a blocker for Phase 15.**

## 13. Minimum tests for the eventual implementation

Deferred until §11.1 is decided; listed so the decision can be costed.

1. speech-complete is emitted exactly once per successfully spoken turn
2. floor transitions agent → caller after delivery
3. conversation turn count advances per turn
4. the next caller turn is accepted and plans a response
5. no speech-complete on the barge-in path (generation superseded)
6. no speech-complete after cancellation
7. no duplicate completion when the same turn ends twice
8. no completion after a terminal conversation (`ErrTerminal` respected)
9. multi-turn with a **local per-stream STT double** (§12) — distinct intent per turn
