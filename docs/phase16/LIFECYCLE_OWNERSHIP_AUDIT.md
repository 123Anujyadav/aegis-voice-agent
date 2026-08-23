# Phase 16 T2 — Conversation Lifecycle Ownership Audit

**Status:** AUDIT ONLY. **No production code changed** (`pipeline.go` is
byte-identical to its Phase 16 T1 state). Test-only evidence added. No frozen
module changed beyond Phase 15 T4's approved amendment.

---

## 1. The headline

> **NO PRODUCTION CALL DRIVER FOUND.**

A fully constructed, running voice service creates **no conversation**, holds
**no session**, and builds **no pipeline**. Its registered runner logs once and
blocks on `ctx.Done()`. Nothing in the repository receives an inbound call.

Proven, not asserted, by `TestT2_RunningServiceCreatesNoConversation`: the
service is started exactly as `run()` starts it, nothing else happens, and the
bridge holds no conversation for any plausible identifier.

## 2. Lifecycle event ownership

| Event | Production producer | Production consumer | Test producer | Status |
|---|---|---|---|---|
| `EventStart` | **none** | `conversation.handleStart` | `openPlanner` (test rigs) | **UNOWNED** |
| `EventGreetingComplete` | **none** | `handleGreetingComplete` | `openPlanner` | **UNOWNED** |
| `EventUtterance` | `voice/pipeline.go:942` | `handleUtterance` | — | **PRODUCTION OWNED** |
| `EventSpeechComplete` | `voice/pipeline.go:1209` | `handleSpeechComplete` | — | **PRODUCTION OWNED** (Phase 15 T4) |
| `EventResponseAbandoned` | `voice/pipeline.go:1244` | `handleResponseAbandoned` | — | **PRODUCTION OWNED** (Phase 15 T4) |
| `EventInterrupt` | **none** | `handleInterrupt` | Phase 14/15 tests | **UNOWNED** — detection exists, emission does not |
| `EventSilence` | **none** | `handleSilence` | Phase 14/15 tests | **UNOWNED** |
| `EventHangup` | **none** | `Handle` → `StateEnded` | conversation's own tests | **EXTERNAL OWNER REQUIRED** (transport) |
| `EventTimeout` | **none** | `Handle` → `StateTimeout` | — | **UNOWNED** |

**3 of 9 events have production producers**, and all three are in
`voice/pipeline.go` — two of them added by Phase 15 T4.

The only non-test references to the six unowned events outside `conversation`
are in `intent/turn.go`, where they are `case` labels of `ClassifyTurn` — a pure
function that **the Bridge never calls** (Phase 15 T3 finding). Those are
consumers of the event *vocabulary*, not producers of events.

## 3. `EventInterrupt` — detection without emission

This one deserves separating from the others, because it is not simply absent.

`voice.interruptController` is **production code**, implements
`audiointel.SpeechController`, and does real work: it bumps the generation
counter, cancels via the existing `SpeechController`, aborts the live turn and
moves the SessionFSM `interrupted → listening`.

What it does **not** do is tell the conversation. `bargein.go` contains no
reference to `Planner` or `conversation` (verified). So in production the
`InterruptionEngine` would record nothing, and the dialogue floor would never be
force-yielded by a barge-in.

**Classification: production detection exists; production emission does not.**

## 4. Conversation creation ownership

| Question | Answer |
|---|---|
| Who creates a `ConversationID`? | **Nobody in production.** |
| Who calls `Engine.Begin`? | `voiceintel.Bridge.Planner` (`bridge.go:141`) — but **nothing in production calls `Bridge.Planner`**; the only match in `main.go` is a comment. |
| Who owns the resulting `Conversation`? | The engine's `active` map, keyed by id. No service holds one. |
| Who owns its lifetime? | **Nobody.** Phase 14 deliberately introduced no session registry. |
| Who receives an inbound call? | **Nobody.** No service imports `telephony`, `media` or `speech`. |
| Who emits `EventStart`? | **Nobody.** |
| Who decides when greeting begins / completes? | **Nobody.** |

`voice.NewPipeline` and `voice.NewSessionFSM` have **no production callers** —
only their own definitions. `services/go/voice` is the only service that
registers a runner at all.

## 5. Test path vs production path

```
TEST (works):
  build service -> Bridge.Planner(id) -> EventStart -> EventGreetingComplete
    -> caller utterance -> intent -> plan -> ... -> audio

PRODUCTION (stops immediately):
  platform.Service.Run -> voiceIntelligence.Run -> <-ctx.Done()
                                                   ▲
                                                   └── stops here.
  No inbound call, no ConversationID, no Begin, no EventStart, no pipeline.
```

The test rigs' `openPlanner` sends the two lifecycle events itself. **That is
test scaffolding, not production ownership**, and this audit does not count it
as such.

Caller-visible consequence, measured
(`TestT2_UnopenedConversationRefusesTheCaller`): without `EventStart`, a
perfectly good utterance yields `action=ignore reason="overlap_denied"` with
`state=idle floor=none`. The caller is simply not heard.

## 6. `EventStart` semantics, measured

| Step | Plan | State | Floor |
|---|---|---|---|
| initial | — | `idle` | none |
| `EventStart` | **`respond` / `"greeting"`, confidence 1.0** | `greeting` | **agent** |
| `EventStart` again | error: `invalid state transition: already in state greeting` | `greeting` (unchanged) | agent |
| `EventGreetingComplete` | `wait` / `"awaiting_caller"` | `listening` | **none** |
| first utterance | `respond` / `"intent_accepted"` | — | — |

**A duplicate `EventStart` is REJECTED and harmless** — the conversation is
unchanged. The frozen contract is safe against a driver that starts twice.

**Measured asymmetry worth recording:** `handleGreetingComplete` *releases* the
agent but does not *acquire* for the caller, so the floor is `none`, unlike
`handleSpeechComplete` which does both. The caller is still heard, because
`handleUtterance` takes the floor itself.

## 7. Greeting semantics — the finding that enlarges the problem

`handleStart` returns **`Plan{Action: ActionRespond, Reason: "greeting"}`**. The
greeting is a *spoken response*, and INV-CV-2 makes the opening turn
**non-yielding** because it carries the announcement that is the caller's
lawful basis.

But the pipeline's speaking path is entered **only from a final transcript**
(`runTurn`). A plan returned from `EventStart` has no route into generation,
synthesis or audio.

> **Sending `EventStart` alone would open a conversation whose greeting is never
> heard** — and by INV-CV-2 that greeting is a compliance artefact, not a
> pleasantry.

Whoever owns `EventStart` must therefore also route the returned plan into the
speaking path. **Classification: D — not implemented.** There is no greeting
audio, no greeting text and no component that speaks one.

## 8. Where the owner should live

| Candidate | Assessment |
|---|---|
| **A. `services/go/voice/cmd/server`** | **RECOMMENDED.** It already owns the `platform.Runner` lifecycle, already holds the `Bridge`, and is non-frozen. Dependency direction is correct: the service may depend on `voice`, `voiceintel` and `conversation`; none may depend on it. Shutdown already works and is tested. It is the only place that can own a session without inverting a dependency. |
| B. `voice.Pipeline` | Unsuitable as the *owner*. It is per-session and is constructed *by* whoever owns the session; it cannot create itself. It should remain the thing a driver drives. |
| C. `voice.SessionFSM` | Unsuitable. A passive state holder driven by the pipeline; holds no `Planner` and knows nothing of conversations. |
| D. `voiceintel.Bridge` | Unsuitable. Deliberately stateless — "this service stores no conversation state" — and giving it session lifetime would reintroduce the registry Phase 14 avoided. |
| E. `conversation.Engine` | Unsuitable **and frozen**. It cannot know about transports or audio. |
| F. External transport/session adapter | **The eventual right answer, and it does not exist.** No service imports `telephony`; `telephony-gateway` is a 78-line scaffold that registers nothing. |

**Recommendation: A**, with the explicit caveat that A is where the driver
*lives*, not where calls *come from*. Until a transport feeds it, any driver in
A can only be exercised by a test — which is the same gap one level up.

## 9. Stop conditions

Two are triggered, and both are reported rather than resolved:

- **Condition 1 — a production lifecycle owner cannot be identified.** None
  exists; candidates were evaluated and one recommended, but choosing it is an
  architecture decision.
- **Condition 7 — the only way to prove production ownership today is test-only
  injection.** Exactly what the audit set out to test, and it is confirmed.

**No frozen module needs to change.** `EventStart` semantics are unambiguous and
duplicate-safe; `EventGreetingComplete` is unambiguous. The blocker is ownership
and the missing greeting-speech route, not the contract.

## 10. What a future call driver must do

Recorded so the eventual task is scoped from evidence, not invention:

1. receive an inbound call from a transport that does not yet exist
2. mint a `ConversationID` and call `Bridge.Planner`
3. send `EventStart` — and **speak the returned greeting plan**, which needs a
   route into the pipeline's speaking path that does not exist today
4. send `EventGreetingComplete` when that audio finishes
5. construct and drive the `voice.Pipeline` (nothing constructs one today)
6. forward `EventInterrupt` from the existing detection, which currently stops
   at the SessionFSM
7. own `EventSilence`, `EventHangup` and `EventTimeout`, separating transport
   lifecycle from conversation lifecycle
8. terminate the conversation and release the session

Items 3, 5 and 6 are each substantial in their own right. **This is a phase, not
a task.**
