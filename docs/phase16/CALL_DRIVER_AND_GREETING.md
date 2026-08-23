# Phase 16 T4 — Call Driver & Greeting Lifecycle

**Status: STOPPED — stop condition 1.** The greeting cannot be spoken through
the existing response path, so no call driver was implemented. **No production
code changed.** Evidence tests only.

---

## 1. Why T4 stopped

A call driver's first act is to open the conversation. `EventStart` returns
`Plan{ActionRespond, "greeting", confidence 1.0}` — a **spoken** response by the
pipeline's own `planSpeaks` predicate, and one that INV-CV-2 makes non-yielding
because it carries the caller's lawful-basis announcement.

**Nothing can speak it.**

| Fact | Evidence |
|---|---|
| The speaking path is entered only from a transcript | `runTurn` is unexported and called from exactly one place — the transcript loop |
| No exported method speaks a Plan | `Pipeline` exposes `Start`, `WriteFrame`, `InvokeTool`, `Cancel`, `Disconnect`, `Fail`, `Done`, `Wait`, `Err` and three counters. That is all. |
| `Start` does not greet | It moves the SessionFSM to `StateListening`, starts ingest, and sends **no conversation event** |
| The voice session has no greeting phase | `voice.SessionFSM` states are created, listening, speaking_detected, transcribing, thinking, synthesizing, speaking, interrupted, cancelled, failed, completed — **there is no greeting state** |

So a driver could send `EventStart` and receive the greeting plan, and then have
nowhere to put it.

## 2. What a naive driver would actually do — measured

Sending `EventStart` without being able to deliver the greeting leaves the
conversation in the greeting phase, because nothing can legitimately send
`EventGreetingComplete`. The caller then speaks and is **not heard**:

```
action=ignore  reason="floor_queued"  state=greeting  floor=agent
```

`floor_queued` is the frozen turn manager behaving correctly: the opening turn
is non-yielding, so the caller's audio is queued behind a greeting that will
never finish. **A partial driver is worse than none** — it converts "nothing
happens" into "the caller is queued behind silence forever".

## 3. The blocker is NOT the frozen contract

Driven directly through the frozen API, the whole lifecycle already works:

```
EventStart            -> state=greeting, floor=agent, plan=respond/"greeting"
EventGreetingComplete -> state=listening
utterance             -> request_transfer / intent_accepted
```

**No conversation amendment is required.** The missing piece is a production
component able to speak the greeting between steps one and two.

Also re-verified: a **duplicate `EventStart` is rejected** and harmless
(`already in state greeting`), and once a conversation is open a further
`EventStart` is refused (`listening -> greeting is not declared`). A driver that
started twice could not corrupt anything.

## 4. Second-order problem, recorded before it is hit late

The pipeline builds its generation request from the caller's transcript:
`Messages: [{RoleUser, final.Text}]`. **A greeting has no transcript** — there is
no caller utterance yet.

So even with a speaking route, the generator would be asked to produce a
greeting from an empty user message, with nothing indicating that a greeting is
wanted or which plan is being spoken. With the deterministic fake this is
invisible; with a real model it is the difference between a greeting and
silence.

This is the same class of gap Phase 16 T1 recorded for clarification plans, and
it is a generation-semantics change — outside T4's scope, and not made.

## 5. Ownership, re-verified (T2 still accurate)

| Responsibility | Owner |
|---|---|
| creates `ConversationID` | **nobody** |
| calls `Engine.Begin` | `voiceintel.Bridge.Planner` — which **nothing in production calls** |
| creates `voice.Pipeline` | **nobody** (no production caller of `NewPipeline`) |
| owns pipeline lifetime | **nobody** |
| sends `EventStart` | **nobody** |
| speaks the greeting | **nobody, and nothing can** |
| sends `EventGreetingComplete` | **nobody** |
| feeds the first utterance | **nobody** |
| terminates the conversation | **nobody** |

`REAL INBOUND TRANSPORT IS NOT PRESENT.` No service imports `telephony`,
`media` or `speech`; `telephony-gateway` is a scaffold that registers nothing.

## 6. `EventGreetingComplete` ownership — conclusion C, with a caveat

Of the three permitted conclusions, the honest one is **C: no correct owner
exists today** — because the event must mean *"the greeting audio finished"*,
and no component produces greeting audio.

The caveat matters for the next task: **if** a speaking route existed, the owner
would be unambiguous — the same completion boundary that already emits
`EventSpeechComplete` (`pipeline.go`, after the last frame is delivered, inside
the generation guard). The ownership question is therefore *downstream* of the
speaking-route decision, not independent of it.

## 7. Minimum change required (NOT made — needs approval)

The smallest change that would unblock a driver, in the **non-frozen** `voice`
package:

> An exported seam on `voice.Pipeline` that submits a `conversation.Plan` into
> the existing speaking path — the same governance → generation → chunker → TTS
> → delivery → `EventSpeechComplete` sequence `runTurn` already performs, minus
> the transcript-derived parts.

Consequences that make this more than a one-liner, and why T4 did not attempt it:

1. `runTurn` is built around a `speech.TranscriptSegment`; the greeting has none.
2. The generation request needs *something* to generate from (§4).
3. `voice.SessionFSM` has no greeting state, so either the greeting is spoken as
   an ordinary `speaking` turn, or the session model gains a phase.
4. `EventGreetingComplete` must fire at the delivery boundary **in addition to
   or instead of** `EventSpeechComplete` — the conversation expects
   `EventGreetingComplete` while in `StateGreeting`, and the pipeline currently
   only knows how to send `EventSpeechComplete`.

Item 4 is a genuine semantic fork and should be decided explicitly, not chosen
by whoever writes the code.

## 8. What T4 verified without a driver

- `Pipeline.Start` emits no conversation event and delivers no greeting audio
- the greeting plan is returned to the caller of `EventStart` and dropped
- a naive driver queues the caller behind an unfinished greeting
- the frozen contract already supports the complete lifecycle
- the greeting generation payload is absent
- cancelling a conversation that never spoke fabricates nothing: **0
  completions, 0 abandonments, 0 interruptions**

## 9. Boundary statement

**DETERMINISTIC INTERNAL CALL LIFECYCLE ≠ REAL TELEPHONY.** Neither is claimed.
T4 implemented no driver at all, and real inbound-call support does not exist.
