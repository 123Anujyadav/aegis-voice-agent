# Failure Handling

**Status:** IMPLEMENTED · all 17 named cases **VERIFIED** (Task 14) · 20 tests.

---

## 1. The principle

"An error was returned" is close to worthless. Every failure below is
survivable, and what matters is the state **afterwards**: whether a process is
still running, a goroutine still parked, a queue still holding a call's worth of
audio, whether the next caller can be served.

A pipeline can return a beautifully typed error and still have leaked a
subprocess.

## 2. The post-failure contract

Every case ends in the same assertions, regardless of what went wrong:

| Guarantee | How it is checked |
|---|---|
| FSM state valid, reached only by declared transitions | full history walked against the table |
| Queues within bounds | `len(frames) ≤ cap(frames)` |
| No stale audio from the failed turn | per-synthesis-stream counts sampled at the media sink |
| Pipeline shut down when it should be | `Done()` closes |
| No goroutine leak | counted across the matrix, settle-and-compare |
| No orphan process | heartbeat-file observation with a **real** child |
| A subsequent session still works | a fresh session runs a full turn |

That last one matters most: a leak that only damages the **next** caller is the
one that reaches production.

## 3. The matrix — all VERIFIED

| # | Case | Outcome | Call survives? |
|---|---|---|---|
| 1 | STT provider missing | `ErrProviderUnavailable`, session ends | no — cannot open a turn at all |
| 2 | STT process crash | partial forwarded, turn ends with nothing to say | **yes** |
| 3 | STT timeout (never answers) | session lifetime reclaims it | **yes**, until disconnect |
| 4 | STT invalid output (blank final) | treated as silence | **yes** |
| 5 | TTS provider missing | `ErrProviderUnavailable`, turn fails | **yes** |
| 6 | TTS process crash | no audio; turn ends | **yes** |
| 7 | TTS timeout (accepts, emits nothing) | turn timeout reclaims — **20.25 s → 1.05 s** | **yes** |
| 8 | TTS invalid output (refuses chunks) | no audio; turn ends | **yes** |
| 9 | LLM unavailable | `ErrProviderFailed` | **yes** |
| 10 | Provider switch | breaker opens, secondary serves | **yes** |
| 11 | Provider recovery | cooldown → half-open → closed | **yes** |
| 12 | Cancellation | FSM → `cancelled` | ends by request |
| 13 | Barge-in during TTS | generation guard; 0 stale frames | **yes** |
| 14 | Disconnect during STT | FSM → `completed` | ends normally |
| 15 | Disconnect during TTS | FSM → `completed` | ends normally |
| 16 | Governance denial | invoker never reached | **yes** |
| 17 | Tool failure after approval | `ErrProviderFailed`, not a denial | **yes** |

**One provider hiccup costs one turn, not the call.** The exceptions are
deliberate: a missing recogniser means no turn can open at all, and a
disconnect/cancel is an ending by definition.

## 4. Error taxonomy

| Error | Meaning | Runbook |
|---|---|---|
| `ErrProviderUnavailable` | The provider is absent or unreachable | install/start it |
| `ErrProviderFailed` | It was there and broke | investigate the provider |
| `ErrGovernanceDenied` | Policy said no | policy question |
| `ErrObligationsUnmet` | Policy said "not yet" | satisfy the obligation |
| `ErrBackpressure` | A queue is full | capacity |
| `ErrSessionClosed` | The call has ended | none |
| `ErrInvalidTransition` | The state machine refused | a bug, unless a lost race |

The distinction between *unavailable* and *failed* is load-bearing: it sends an
operator to different places. Task 17 confirmed the pipeline picks the more
precise one — a missing synthesiser surfaces as `ErrProviderUnavailable` because
`PickTTS` runs before generation.

## 5. Determinism

No test waits for a random crash. Faults are **scripted provider behaviours**;
the breaker's 30-second cooldown is **advanced on an injected `rt.FakeClock`**
rather than slept through (switch + recovery run in 0.00 s); process cleanup is
observed via a heartbeat file.

## 6. Concurrent, mixed failures

`TestFailure_ConcurrentFailuresStayIsolated` runs healthy and broken sessions
together — STT crash, TTS crash, model down, voice absent, and one healthy. Each
reaches its own outcome; the healthy session speaks while its neighbours fail.

Task 15 extends this to a **shared** router, recogniser, voice and metric set —
the deployed shape — with 12 concurrent sessions. See
[EVALUATION_REPORT.md](EVALUATION_REPORT.md).

## 7. Defects the matrix found

Both were real production defects, fixed with mutation-verified regressions:

1. **A hung synthesiser held the turn until the call ended.** `pumpAudio` watched
   only the session context, so the turn timeout could not reclaim it.
   **MEASURED: 20.25 s → 1.05 s.**
2. **A blank final transcript became a question to the model.** The agent would
   speak, unprompted, at someone who said nothing.

## 8. A test-double bug worth recording

The TTS-invalid case ran 20 s and I first suspected the frozen dispatcher — and
wrote that `Close` is not called on a detached sink. **That was wrong, and it is
corrected here**: on a `Write` error `live[i]` stays true, so `Close` **is**
called; both detach paths close the sink.

The real cause was my own stub incrementing `inflight` before an early return,
leaking the slot so the audio channel could never close (20.25 s → 0.25 s). The
pipeline change I had made on the wrong theory was **reverted**, because no
failing test justified keeping it.
