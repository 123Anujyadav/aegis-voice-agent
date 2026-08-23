# Phase 16 T9 — STT Failure Policy

**Status: IMPLEMENTED and VERIFIED LOCALLY.** No frozen module changed, no new
conversation event, no new error vocabulary. `-race NOT RUN LOCALLY`.

**WhisperCLI wiring: STOPPED at the production-mode gate.** See §6.

---

## 1. The defect, restated precisely

Phase 16 T8 found that an STT provider that cannot open ends the **voice
session** and tells the **conversation** nothing:

```
onFrame → beginTurn → PickSTT/OpenSTT error
        → p.note(err) → stop(StateFailed, ReasonCrashed)
```

`stop` moves the SessionFSM and cancels the context. It sends no conversation
event, and nothing else did either: `Call.End` is only called by whoever placed
the call, and a failure is precisely the case where nobody does.

**Measured before the fix:** session `failed`, conversation `listening`, still
accepting utterances, forever.

## 2. What each failure actually does — measured, not assumed

| | Case | Voice session | Conversation (before) | Conversation (after) |
|---|---|---|---|---|
| A | provider will not start (`OpenSTT` refuses) | `failed` | **`listening`, open** | **`ended`** |
| B | process dies while opening | `failed` | **`listening`, open** | **`ended`** |
| C | provider's own deadline expires | — | see F | see F |
| D | typed provider error at open | `failed` | **`listening`, open** | **`ended`** |
| E | context cancelled while STT is active | terminal | closed by `End` | unchanged |
| F | no final transcript | **live** | **live** | unchanged — this is silence, not a fault |

**C and F are the same path.** The pipeline has **no STT-stage deadline** of its
own: `recognize` blocks on the results channel until it closes or the session
ends. A provider that hangs holds the turn until the call ends, so the timeout
is the *provider's* responsibility — `whispercli.Config.ResultTimeout` and
`whispercpp.Config.ResultTimeout` exist for exactly this. Recorded here because
it is invisible with fakes and load-bearing with a real subprocess.

**F is deliberately not a failure.** A recogniser that ends its stream with no
final transcript is indistinguishable from a caller who said nothing, and is
handled as silence: the turn ends, the call continues.

## 3. The policy

> A provider failure **before any response was accepted** →
> **`EventFault`** (the cause) then **`EventHangup`** (the close).

- **NOT `EventResponseAbandoned`** — that means "an accepted response will not be
  spoken" (Phase 15 T4). No plan was ever produced, so there is nothing to
  abandon; sending it would put a phantom response in the record of every failed
  call.
- **NOT `EventSpeechComplete`** — nothing was said.
- **NOT `EventInterrupt`** — nobody interrupted.
- **Both events, not one.** `StateError` is explicitly **not terminal** in the
  frozen engine — *"that is the difference between an error and a failure"*
  (`conversation/state.go:111`) — so the fault alone would leave the conversation
  open, still accepting events, beside a dead session. `StateError → StateEnded`
  is a declared transition, and `StateError` is reachable from every
  non-terminal state, so the pair works from anywhere.

**No new event. No frozen amendment.** This is the existing vocabulary used as
written.

## 4. Where it lives

`Call.watch()` — one goroutine per call, started by `Place`:

```go
<-c.pipeline.Done()
if fault := c.pipeline.Err(); fault != nil && !c.ended.Load() {
    c.terminate(voice.ReasonCrashed, fault)   // EventFault → EventHangup
}
```

**Only on a fault.** A session that ended because `End` was called, or because
the service is shutting down, is already handled; terminating again would record
a spurious hangup on every clean exit. The goroutine waits on a channel that
always closes, so there is nothing to leak.

**Ownership is unchanged:** the pipeline still does not terminate conversations
(it is below the dialogue layer); the driver still owns termination, and now
covers the case where the session ends without being asked.

### Two supporting changes

1. **`Call.Closed()`** — a new channel, closed after the conversation has been
   told and the session stopped. `Done()` means "the pipeline's goroutines
   returned", which happens *before* the watcher closes the conversation.
   Without this distinction, anything waiting on `Done()` alone observes the
   call mid-close — **which is exactly how the first version of these tests
   failed**, and would otherwise have been an intermittently-green suite.
2. **`voice.failureReason`** — a fatal ingest error now maps onto
   `ReasonUnavailable` / `ReasonTimeout` / `ReasonCrashed` rather than always
   `ReasonCrashed`. Same distinction `errors.go` already draws, carried through
   to the terminal reason an operator sees: "the recogniser is not installed"
   must not be reported as "it crashed".

## 5. Verification

10 tests, all green, all deterministic (channels and bounded selects; no sleeps,
no polling). Six mutations executed against code that exists:

| | Mutation | Result |
|---|---|---|
| M3 | STT failure observed and ignored | **CAUGHT** — conversation left `listening` and still accepting |
| M4 | `EventResponseAbandoned` sent instead of the fault | **CAUGHT** — no fault in the trace *(and the frozen engine refused the abandonment itself, from `listening`)* |
| M7 | fault recorded but the call never closed | **CAUGHT** — conversation left in `error`, the non-terminal state |
| M8 | watcher never started | **CAUGHT** — the call never finishes closing |
| M9 | failure reason flattened to `crashed` | **CAUGHT** |
| M10 | fault-driven hangup blamed on the caller | **NOT CAUGHT / INERT** |

**M10 is reported, not credited.** `handleHangup` ignores `Event.Party` — it only
transitions — so the attribution reaches nothing observable. The code still says
`PartyNone` because that is what happened, but **it is documentation, not an
enforced property**, and a future engine that records the party would need a
test written for it.

Mutations M1, M2, M5 and M6 from the task brief (provider not registered, wrong
provider selected, subprocess surviving cancellation, transcript logged on
failure) are **NOT REACHABLE**: they target whispercli wiring that §6 stopped
before writing. Mutating code that does not exist would be theatre.

## 6. WhisperCLI — STOPPED at the production-mode gate

**Preconditions verified (filesystem inspection only, nothing invoked):**

| | |
|---|---|
| executable | `…\Python312\Scripts\whisper.exe` — **present** |
| model | `~/.cache/whisper/tiny.pt` (75.6 MB), `base.pt` (145.3 MB) — **cached** |
| download required | **NO** |
| network required | **NO** |
| API key | **NO** |
| argv safety | vector, never a shell string; only validated config + a generated temp path |
| environment | allowlisted (`DefaultInheritEnv`, 15 named variables) — no wholesale inheritance |
| stderr | bounded ring (`MaxStderrBytes`) |
| orphan | `process.Stop` reaps by signal then kill |
| temp audio | `MkdirTemp` + `RemoveAll` on stream close |

**And it is still blocked.** Registering whispercli requires constructing a
`voice.ProviderRegistry` with a deployment mode, and:

> `ProviderRegistry.describe` refuses a `ClassDevelopment` provider in
> `ModeProduction` — *"ADR-0006 freezes the production ladder and rejected
> self-hosted open-weight models; a development provider does not become a tier
> by being registered."*

All four real providers are development-class. Choosing `ModeDevelopment` in the
service **is** the approval decision T8 raised and nobody has granted; choosing
`ModeProduction` means the registration is refused. There is no third option
that does not bypass the guard.

**The guard is already proven** by `registry_test.go:500` — no new test was
added, because duplicating an existing proof is not evidence.

### What approval unblocks it

One decision, either way:

- **A.** the voice service may run in `ModeDevelopment` with development-class
  providers; **or**
- **B.** ADR-0006 is amended so a local recogniser may be classified
  `ClassProduction`.

Until then §9–§12 of the task brief (real execution, full path, multi-turn,
subprocess cancellation) cannot run.

### A second, independent blocker

Part 9 requires *"a deterministic, non-sensitive audio fixture **already
available in the repository**"*. **There is none** — no `.wav`, no `testdata`
audio anywhere. The existing e2e test *generates* one at runtime via Windows
SAPI **and** ffmpeg, which is two further external binaries. So even with mode
approval, real execution needs either a committed audio fixture or approval to
invoke SAPI + ffmpeg.

## 7. Boundary

**DETERMINISTIC INTERNAL CALL DRIVER ≠ REAL TELEPHONY.** No STT provider is
wired into the service. No claim is made about recognition accuracy, telephony,
SLA or production readiness.
