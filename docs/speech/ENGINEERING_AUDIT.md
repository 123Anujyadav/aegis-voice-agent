# Engineering Audit

**Phase 11C** · `packages/go/speech` · Audited 2026-08-11

Every figure below was produced by a command that was run. Where something could
not be verified, it says so.

---

## 1. Brief compliance

| Brief section | Status | Where |
|---|---|---|
| 15 core subsystems | **Met** | See [SPEECH_ARCHITECTURE.md](SPEECH_ARCHITECTURE.md) §4 for the map |
| STT contract — session, streaming, partials, finals, confidence, timestamps, language, provider metadata, cancellation, timeout, failure, graceful close | **Met** | `provider.go`, `stt.go` |
| Partial transcripts — update, correction, ordering, dedup, finalisation, late rejection, cancellation | **Met** | `assembler.go`; cases 1–4 |
| Transcript model — session, turn, segment, sequence, text, final, confidence, times, language, role, provider metadata; **no raw PCM** | **Met** | `transcript.go` |
| Turn management — 9 states, no implicit transitions, no duplicates, no out-of-order, no stale callbacks, no double finalisation, no cross-session contamination | **Met** | `turn.go`, `assembler.go` |
| TTS contract — streaming, chunk synthesis, frame output, voice, language, prosody, cancellation, timeout, failure, graceful close | **Met** | `provider.go`, `tts.go` |
| TTS streaming — sentence-level, ordering, sequence, cancellation, backpressure, partial failure | **Met** | `tts.go`, `chunker.go` |
| Sentence chunking — `.` `?` `!` `।`, Hinglish, abbreviations, decimals, phone numbers, OTP, URLs, short sentences | **Met** | `chunker.go`; cases 15–23 |
| Language support — English, Hindi, Hinglish, Devanagari, mixed script, Tamil-ready | **Met** | `transcript.go`, `chunker.go`. **No quality claim** — see §5 |
| Provider routing — primary, secondary, health, timeout, failure, fallback, recovery, circuit, capabilities | **Met** | `router.go`; cases 5–8 |
| Local dev adapters (Whisper/Piper boundary) | **Met as a boundary** | `provider.go`. No ML runtime embedded, no credentials |
| Cloud adapter boundaries | **Met as a boundary** | [PROVIDER_ROUTING.md](PROVIDER_ROUTING.md). No SDK, no API calls |
| Barge-in — 7 requirements, deterministic contract, VAD as boundary | **Met** | `session.go`; case 10; [BARGE_IN.md](BARGE_IN.md) |
| Latency — measured, not invented | **Met** | [PERFORMANCE.md](PERFORMANCE.md) |
| Backpressure — bounded queues, explicit documented behaviour | **Met** | `stt.go`, `tts.go`; cases 11, 12 |
| Cancellation — propagates, no goroutine survives | **Met** | Cases 13, 14 assert goroutine settle |
| Error model — 13 typed sentinels, no string matching | **Met** | `errors.go` |
| Security / privacy | **Met** | [SECURITY_REVIEW.md](SECURITY_REVIEW.md) |
| Events — provider-neutral, no PCM, no credentials, no content in labels | **Met** | `events.go`; enforced by reflection test |
| Observability — shared metrics package, no second implementation | **Met** | `metrics.go` uses `packages/go/metrics` aliases |
| Testing — 19 required categories | **Met** | [EVALUATION_REPORT.md](EVALUATION_REPORT.md) |
| Benchmarks — real, not invented | **Met** | 18 benchmarks |
| Documentation | **Met** | 11 documents |

## 2. Dependency rule

```
go list -deps ./... | grep -v callscreen | grep "\."
```

Output: `crypto/internal/entropy/v1.0.0` — a **Go standard-library internal**
package whose path contains a dot only because Go 1.26 versions its internals.
No third-party module.

First-party dependencies, exactly three:

```
github.com/callscreen/callscreen-platform/packages/go/media
github.com/callscreen/callscreen-platform/packages/go/metrics
github.com/callscreen/callscreen-platform/packages/go/runtime
```

`GOWORK=off go build ./...` passes, proving the module is self-sufficient rather
than leaning on the workspace.

**`packages/go/conversation` is absent by design** — see
[SPEECH_ARCHITECTURE.md](SPEECH_ARCHITECTURE.md) §6.

## 3. Prohibited technology

```
grep -rniE "elevenlabs|deepgram|cartesia|sarvam|whisper|piper|openai|anthropic|google" \
  packages/go/speech/*.go | grep -v "_test.go"
```

Five hits, **all in comments that disclaim the vendor**:

```
doc.go:35     // No Google Speech, Deepgram, OpenAI, Anthropic, ElevenLabs, Cartesia, Sarvam,
doc.go:36     // Whisper or Piper SDK is imported. Not "not yet" — provider integrations live
provider.go:15  // A router that asked "is this Deepgram?" would embed a vendor name in the core
provider.go:164 // Implementations for Google, Deepgram, Sarvam and anything else live
router.go:157   // Deepgram" is a question this type cannot ask and cannot answer, ...
```

No import, no use, no credential, no network call. Nothing implements SIP, RTP,
WebRTC, VAD, an LLM, fraud detection or emergency detection.

## 4. Defects found and fixed

Six, all found by a failing test or a benchmark rather than by inspection.

### D-1 — `Interrupt` wedged the session when nothing was being said

`Interrupt` cancelled TTS and then called `Begin`, but only transitioned the
previous turn to `interrupted` when it was `responding` or `speaking`. Interrupt
a `listening` turn and the old turn stayed live, so `Begin` refused — leaving the
session unable to open another turn.

Found by `BenchmarkSession_Interrupt`, which opened a fresh session per
iteration and hit it immediately.

**Fixed:** `Interrupt` now refuses explicitly unless the agent is `responding` or
`speaking`. Silently treating it as a barge-in would cancel a turn that was
recognising speech perfectly well. Guarded by
`TestBargeIn_RefusedWhenNothingIsBeingSaid`.

### D-2 — Closing a session leaked its runtime registry slot

`SpeechSession.Close()` closed everything the session owned but never removed
the session from `SpeechRuntime.sessions`. Only `SpeechRuntime.Close()` did. A
caller closing sessions directly — the obvious thing to do — accumulated entries
until the runtime refused new sessions with `ErrBackpressure`, on a process that
was in fact idle.

Found by `BenchmarkSession_Interrupt` failing at exactly 1,000 sessions, the
configured `MaxSessions`.

**Fixed:** the session carries an `onClose` deregistration hook set by the
runtime at `Open`, so closing by any route frees the slot. Guarded by
`TestRuntime_ClosingASessionFreesItsSlot`, which opens and closes ten sessions
against a capacity of two.

### D-3 — The chunker split streaming text differently from one-shot text

A period at the end of the buffer was treated as a sentence terminator, so
`"Aapka balance 1234."` split before `"56"` arrived. Feeding text rune-by-rune
produced four chunks where feeding it whole produced three — meaning TTS
chunking depended on network packet boundaries.

Found by `TestChunker_StreamingMatchesOneShot`.

**Fixed:** a trailing period is **undecidable** while streaming; only `Flush`
knows the stream ended, so only `Flush` decides it. `?`, `!` and the dandas are
unambiguous and still cut immediately, so a question never waits an extra
generator round trip.

### D-4 — `"No."` never terminated a sentence

`No` was in the default abbreviation list (for "number"), so `"Yes. No. Ok."`
produced two chunks instead of three.

Found by `TestChunker_Boundaries/short_sentences`.

**Fixed:** removed from the default list. A voice agent says `No.` as a complete
answer far more often than it says `No. 5`, and merging a refusal into the
following sentence is the more audible error.

### D-5 — The turn table declared a self-transition the frozen FSM refuses

`partial → partial` was declared, and `runtime.NewFSM` (Phase 10A) rejects
self-transitions at construction: *"state partial declares a self-transition;
use a hook instead."* Every turn test failed at construction.

The frozen FSM was right and the table was wrong: a repeated partial is an event
*within* the Partial state, not a state change.

**Fixed:** removed the self-transition and added `NotePartial`, which transitions
on the first partial and is a no-op afterwards. `TestTurn_HistoryIsBounded` now
asserts that 96 partials produce exactly **one** history entry.

### D-6 — A TTS cancellation assertion was inherently racy

`TestTTS_CancelStopsSynthesis` asserted `provider.Cancelled() > 0`, where the
fake counted a cancellation only if audio was still undelivered at close. Whether
that held depended on how far the pump had drained — a race. It passed normally
and failed under `-shuffle=on`.

**Fixed:** the fake gained a deterministic `Closed()` counter, and both tests now
assert on that. `Cancelled()` survives, documented as timing-dependent.

## 5. Open findings

| # | Finding | Severity | Status |
|---|---|---|---|
| F-1 | **`go test -race` was never run.** No C compiler in this environment (`gcc` absent; `CGO_ENABLED=1` fails), and Go's race detector requires cgo | **High** | **Unresolved — environment limitation.** Mitigated by `-count=10 -shuffle=on`, three concurrency-bearing tests, and two goroutine-settle tests. **None is a substitute** |
| F-2 | **`golangci-lint` was never run.** Not installed | Medium | **Unresolved — environment limitation.** `gofmt` and `go vet` pass; the repo `.golangci.yml` configures considerably more |
| F-3 | No provider was ever called, so **no accuracy claim is made** about any language or any vendor | Expected | By design. Provider quality is evidence-driven and belongs to an evaluation phase that runs a corpus |
| F-4 | `EventPublisher` and the metric set are defined but the runtime publishes only session and interruption events; partial/final events are not yet emitted per segment | Low | Deliberate: emitting per partial at 50 fps would make this the highest-volume topic family in the platform. A service opts in |
| F-5 | Turn creation costs 42 allocations (FSM construction per turn) | Low | Measured, recorded in [PERFORMANCE.md](PERFORMANCE.md). Not optimised: a turn is per-utterance, not per-frame |
| F-6 | `Config.CloseTimeout` is validated and stored but session shutdown completes synchronously without consulting it | Low | Honest gap. Shutdown currently joins goroutines directly, which is bounded by provider stream closure rather than by this budget |
| F-7 | **One unreproducible `packages/go/media` test failure was observed** during the Phase 11C frozen-phase check | Low — but recorded rather than dismissed | See below |

### On F-7

While verifying that frozen phases still pass, `packages/go/media` reported
`FAIL` once, with no test name and no failure output. It did not reproduce in
**21 subsequent runs**: 5 single runs, one `-count=10` run, and 6
`-shuffle=on` runs, all `ok`.

No file under `packages/go/media/` was modified during Phase 11C — every source
file there is dated 2026-08-10, the Phase 11B session. Phase 11C adds a
dependency *on* media and changes nothing *in* it.

The most likely explanation is a transient build-cache or filesystem contention
artefact on this Windows host rather than a real defect, but **an unexplained
failure is recorded, not dismissed.** Anyone re-running this suite who sees it
again should capture the full output, because a second occurrence would make it
a real finding.

**On F-1**, this is the most significant unverified property in the module. Any
environment with a C toolchain should run `go test -race ./...` before this
carries production traffic.

## 6. Scope

Files created: **only** under `packages/go/speech/` and `docs/speech/`.

**One line was added outside that boundary:** `./packages/go/speech` in the
`use (...)` block of `go.work`. Without it the workspace cannot resolve the
module. No frozen phase was otherwise touched; `packages/go/runtime`,
`packages/go/metrics` and `packages/go/media` are consumed, never edited.
