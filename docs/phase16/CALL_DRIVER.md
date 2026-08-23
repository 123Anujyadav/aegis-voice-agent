# Phase 16 T7 — The Internal Call Driver

**Status: IMPLEMENTED and VERIFIED LOCALLY.** No stop condition triggered. No
frozen module changed. `-race NOT RUN LOCALLY`.

> **DETERMINISTIC INTERNAL CALL DRIVER ≠ REAL TELEPHONY.**
> No SIP, no PSTN, no WebRTC, no RTP, no WebSocket, no HTTP media ingestion, no
> microphone, no real caller audio. **REAL INBOUND TRANSPORT IS NOT PRESENT.**

---

## 1. What was missing, and now is not

Phase 16 T2 audited who owns each step of a call's life. The answer was the same
nine times: **nobody**. T6 gave the greeting a route into the speaking path;
nothing called it. Every conversation in this repository was opened by a test.

| Responsibility | T2 owner | T7 owner |
|---|---|---|
| creates `ConversationID` | nobody | `CallDriver.Place` |
| calls `Engine.Begin` | nothing production | `CallDriver.Place`, **through the Bridge** |
| creates `voice.Pipeline` | nobody | `CallDriver.Place` |
| owns pipeline lifetime | nobody | the driver's run context |
| sends `EventStart` | nobody | `Call.Greet` |
| speaks the greeting | nobody, and nothing could | `Call.Greet` → `Pipeline.Speak` |
| sends `EventGreetingComplete` | nobody | **`Pipeline.Speak`, not the driver** |
| feeds the first utterance | nobody | `Call.Audio` |
| terminates the conversation | nobody | `Call.End` → `EventHangup` |

## 2. Shape

```go
type CallDriver struct{ ... }          // a platform.Runner, one per process
func (d *CallDriver) Place(ctx) (*Call, error)

type Call struct{ ... }                // ONE call; the value IS the session
func (c *Call) Greet(ctx) error        // EventStart -> Speak(plan) -> blocks
func (c *Call) Audio(media.Frame) error
func (c *Call) End(reason string) error // idempotent; EventHangup + Disconnect
```

**No registry, no package-level map, no global.** A call is reachable only
through the value `Place` returned. Shutdown reaches every call because each
pipeline is bound to the driver's run context — not because the driver keeps a
list it would have to prune correctly on every exit path.

**Per-call ports are per-call.** `CallDriverConfig.NewCallPorts` produces the
audio analyser, the media sink and the observer for each call. The registry, the
generator and the governor are legitimately process-wide; those three are not,
and sharing them would put two callers' audio in one sink.

## 3. What the driver deliberately does NOT do

- **It does not emit `EventGreetingComplete` or `EventSpeechComplete`.** Those
  mean "the audio finished", which only the component that delivered it knows.
  `Pipeline.Speak` owns them (ADR-0021). A driver that sent them too would send
  them twice on every call.
- **It writes no greeting text.** The words come from
  `PipelineConfig.GreetingSystem`. Authoring the lawful-basis announcement is a
  compliance decision, not a service's.
- **It classifies nothing, plans nothing, and decides nothing.** Every dialogue
  decision comes from the frozen engine through the Bridge.

## 4. Why the production build registers no driver

A call needs a recogniser, a voice, an analyser, a generator, a governor and a
media sink. Every shipped provider shells out to an external binary or a model,
and this phase remains no-model/no-network (ADR-0017), so `run()` supplies none
and registers only the intelligence runner. `withCallDriver` is the seam a
deployment **with** providers uses, and it is production code precisely so the
wiring a real deployment performs is the wiring that is exercised.

This is asserted, not merely stated:
`TestT7_ProductionRunRegistersNoDriverWithoutProviders`.

## 5. Verified

| | |
|---|---|
| new tests | 19 in `driver_test.go`, all through the production `buildService` path |
| service suite | green (228 s), including every Phase 14/15/16 test |
| determinism | T7 `-count=10 -shuffle=on` green; voice, voiceintel, intent green |
| `GOWORK=off` | green |
| mutations | 8 executed: **6 CAUGHT**, **2 NOT CAUGHT (reported)** |
| third-party deps | zero |
| frozen modules | unchanged; `conversation` still carries only the Phase 15 amendment |
| `-race` | **NOT RUN LOCALLY** — no C compiler |

### Mutation results

| | Mutation | Result |
|---|---|---|
| M1 | `EventStart` removed from the driver | **CAUGHT** — the conversation is still idle after the greeting |
| M2 | `Pipeline.Speak(greeting)` skipped | **CAUGHT** — the conversation is still in its opening turn |
| M3a | the post-greeting lifecycle check removed | **NOT CAUGHT / INERT** — that check *is* the detector for M1 and M2; removing it alone changes nothing on a healthy path |
| M3b | audio accepted after termination | **NOT CAUGHT / INERT** — `Pipeline.WriteFrame` already refuses a closed session, so the driver's guard is redundant |
| M3c | the greeting is not awaited before caller input | **CAUGHT**, after the test was strengthened — see below |
| M4 | `ConversationID` reused between calls | **CAUGHT** — identity and isolation |
| M5 | driver teardown removed | **CAUGHT** — three tests; the waits are bounded so it fails rather than hangs |
| M6 | Bridge bypassed with a second Engine | **CAUGHT** — the bridge no longer knows the conversation, and the rogue engine has no classifier, so every intent falls back |
| M7 | per-call ports shared across calls | **CAUGHT** — one port set for two calls |

**M3c escaped first.** The rig's own `greet` helper waited for a turn token
after calling `Greet`, which hid the fact that `Greet` had stopped waiting for
the greeting to be delivered. The fix was a new test —
`TestT7_GreetReturnsOnlyAfterTheGreetingHasBeenDelivered` — which parks the
greeting mid-delivery with the sink handshake and asserts `Greet` is still
running at that moment. The mutation is caught deterministically now.

**M3a and M3b are reported as not caught rather than credited.** Neither is a
defect the suite should be able to see independently: one is a detector, the
other is a redundant guard in front of a check the pipeline already performs.

## 6. One flake found and fixed in T7's own code

`-count=10 -shuffle=on` surfaced a real ordering defect in the driver: `Run`
published readiness *before* logging it, so an observer could see the driver
ready with nothing in the log to say so. The log now precedes the signal.

## 7. Unresolved — compliance, not code

INV-CV-2 makes the opening lawful-basis announcement **non-yielding**, and the
frozen turn manager queues a barge-in during it rather than granting the floor.
T7 **retains** that behaviour unchanged and arms its barge-in test only *after*
the greeting completes. Whether a caller may lawfully interrupt the announcement
remains a product and compliance decision.
