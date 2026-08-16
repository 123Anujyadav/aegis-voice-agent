# Speech Evaluation Report

**Phase 11C** · `packages/go/speech` · Evaluated 2026-08-11

---

## 1. Quality gates

Every command was run from `packages/go/speech/`.

| Gate | Command | Result |
|---|---|---|
| Format | `gofmt -l .` | **PASS** — no output |
| Vet | `go vet ./...` | **PASS** |
| Tests | `go test ./...` | **PASS — 72 top-level, 86 including subtests, 0 failures** |
| Repetition and order | `go test -count=10 -shuffle=on ./...` | **PASS** |
| Standalone module | `GOWORK=off go build ./...` | **PASS** |
| Benchmarks | `go test -bench=. -benchmem` | **PASS — 18 benchmarks** |
| Dependency graph | `go list -deps ./...` | **PASS — 3 first-party modules + stdlib only** |
| No vendor SDK | vendor grep | **PASS — 5 hits, all disclaiming comments** |
| **Race detector** | `go test -race ./...` | **NOT RUN — no C compiler in this environment (`gcc` absent; `CGO_ENABLED=1` fails)** |
| **Lint** | `golangci-lint run` | **NOT RUN — golangci-lint is not installed** |

Toolchain: `go1.26.5 windows/amd64`.

**Two gates could not be executed and are reported as NOT RUN, not as passed.**
Neither was worked around, and no claim in this report depends on them.

## 2. The 25 mandatory test cases

| # | Case | Covering test |
|---|---|---|
| 1 | partial → partial → final | `TestAssembler_PartialPartialFinal` |
| 2 | stale partial after final | `TestAssembler_StalePartialAfterFinalIsRejected` |
| 3 | duplicate final | `TestAssembler_DuplicateFinalIsRefused` |
| 4 | out-of-order partial | `TestAssembler_OutOfOrderPartialIsRejected` |
| 5 | provider timeout | `TestRouter_TimeoutCountsAsFailure` |
| 6 | provider failure | `TestRouter_FailsOverToSecondary` |
| 7 | primary → secondary failover | `TestRouter_FailsOverToSecondary` |
| 8 | secondary recovery | `TestRouter_RecoversThroughHalfOpen`, `TestRouter_HalfOpenAllowsExactlyOneTrial`, `TestRouter_FailedTrialReopensImmediately` |
| 9 | TTS cancellation | `TestTTS_CancelStopsSynthesis` |
| 10 | caller interruption during TTS | `TestBargeIn_CancelsTTSAndOpensANewTurn` |
| 11 | bounded TTS queue | `TestTTS_BoundedChunkQueue` |
| 12 | bounded transcript queue | `TestSTT_PartialsThenFinalReachTheAssembler` (queue bound in `stt.go`), `TestRuntime_RefusesBeyondCapacity` |
| 13 | session termination with active STT | `TestSession_CloseWithActiveSTT` |
| 14 | session termination with active TTS | `TestSession_CloseWithActiveTTS` |
| 15 | Hindi transcript | `TestChunker_Boundaries/hindi_devanagari_danda` |
| 16 | Hinglish transcript | `TestChunker_Boundaries/hinglish_code_mixed` |
| 17 | Devanagari transcript | `TestChunker_Boundaries/devanagari_mixed_terminators`, `/double_danda` |
| 18 | Hindi-English mixed | `TestChunker_Boundaries/devanagari_and_latin_mixed` |
| 19 | decimal numbers | `TestChunker_Boundaries/decimals`, `/decimal_in_devanagari` |
| 20 | phone numbers | `TestChunker_Boundaries/phone_number` |
| 21 | OTP-like numbers | `TestChunker_Boundaries/otp_digits` |
| 22 | URLs | `TestChunker_Boundaries/url` |
| 23 | abbreviations | `TestChunker_Boundaries/abbreviations`, `/initials` |
| 24 | concurrent sessions | `TestSession_ConcurrentSessions` (25 parallel) |
| 25 | cross-session isolation | `TestSession_CrossSessionIsolation`, `TestAssembler_RefusesForeignSession` |

**All 25 are covered and passing.**

## 3. Required test categories

| Category | Representative tests | Count |
|---|---|---|
| Unit | `TestAssembler_*` (8), `TestTurn_*` (9), `TestBoundedReason_*`, `TestTranscriptSegment_*` | ~19 |
| Integration | `TestSTT_*` (7), `TestTTS_*` (7), `TestSession_*` (6) | 20 |
| Concurrency | `TestSession_ConcurrentSessions`, `TestSession_CloseWithActive{STT,TTS}` | 3 |
| Cancellation | `TestSTT_CancelStopsTheConsumerGoroutine`, `TestTTS_CancelStopsSynthesis`, `TestSession_OperationsAfterCloseAreRefused` | 3 |
| Failure injection | `TestTTS_ProviderFailureIsReported`, `TestRouter_*` failure paths | 5 |
| Provider failover | `TestRouter_FailsOverToSecondary`, `TestRouter_RecoversThroughHalfOpen` | 4 |
| Ordering | `TestAssembler_OutOfOrderPartialIsRejected`, `TestChunker_SequenceIsMonotonic`, `TestTTS_ChunksTextAndEmitsFramesInOrder` | 3 |
| Backpressure | `TestTTS_BoundedChunkQueue`, `TestRuntime_RefusesBeyondCapacity` | 2 |
| Barge-in | `TestBargeIn_*` | 3 |
| Transcript assembly | `TestAssembler_*` | 8 |
| TTS chunking | `TestChunker_*` (7 + 14 subtests) | 21 |
| Hinglish / Devanagari / mixed script | `TestChunker_Boundaries` subtests | 6 |
| Session isolation | `TestSession_CrossSessionIsolation` | 1 |
| Memory / retention | `TestTurn_HistoryIsBounded`, `TestRecordingEventPublisher_IsBounded`, `TestRuntime_ClosingASessionFreesItsSlot` | 3 |
| Race-sensitive | `-count=10 -shuffle=on` across the suite | — |
| Determinism | `TestChunker_StreamingMatchesOneShot` | 1 |
| Security | `TestSpeechEvent_CarriesNoContent`, `TestSpeechMetrics_NoContentBearingLabels`, `TestTranscriptSegment_RedactedOmitsText` | 3 |

Every category the brief names is represented.

## 4. Determinism

- Every timer and deadline in non-test code measures against `runtime.Clock`.
  `time.Sleep`, `time.After`, `time.NewTimer` and `time.NewTicker` appear in no
  non-test file.
- `TestChunker_StreamingMatchesOneShot` feeds a mixed Hinglish/Devanagari string
  **one rune at a time** and asserts the chunk sequence is identical to one-shot
  input — the property that stops TTS output depending on packet boundaries.
- Ten consecutive shuffled runs pass. One order-dependent assertion was found
  and fixed this way (D-6).

**One test deliberately uses the real clock:**
`TestBargeIn_LatencyIsWithinFrozenBudget`, because barge-in latency is a
wall-clock claim and measuring it on a `FakeClock` would assert nothing.

## 5. What these tests do **not** establish

The most important section of this report.

- **They establish nothing about speech accuracy, in any language.** No provider
  was ever called. `FakeSTTProvider` emits a script; it performs no recognition.
  **No claim is made — and none may be inferred — that Whisper, Piper, Google,
  Deepgram, Sarvam, ElevenLabs or Cartesia produces accurate results for
  English, Hindi, Hinglish, Devanagari or Tamil.** That is an evaluation-phase
  question requiring a real corpus and real providers.
- **The Hinglish and Devanagari tests are segmentation tests, not language
  tests.** They prove the chunker splits multi-script text at the right
  boundaries. They prove nothing about whether that text was transcribed
  correctly, because nothing transcribed it.
- **They do not establish freedom from data races.** `-race` was never run. Three
  concurrency-bearing tests, two goroutine-settle tests and ten shuffled runs
  pass — none detects a race the way the detector does. **Any environment with a
  C toolchain should run `go test -race ./...` before this carries production
  traffic.**
- **They do not establish end-to-end latency.** ADR-0011's 900 ms p50 is a
  pipeline property. This package is one participant, its own overhead is
  negligible ([PERFORMANCE.md](PERFORMANCE.md)), and the hops that dominate the
  budget are provider network calls this phase does not make.
- **They do not establish behaviour on a real network** — no sockets, no
  connection failures mid-stream, no partial writes, no TLS.
- **They do not establish audio quality.** Nothing in this suite listens. Frames
  are zeroed buffers of the correct size.
- **They do not establish endurance.** No soak test. The longest run is 25
  concurrent sessions for the duration of a test.
- **They do not establish multi-process or multi-host correctness.** One
  process, in-memory everything.
- **`Config.CloseTimeout` is not exercised** — shutdown currently joins
  goroutines directly rather than consulting the budget (F-6).

## 6. Assessment

The module meets the Phase 11C brief as specified. Six defects were found and
fixed, **every one of them by a failing test or a benchmark rather than by
inspection** — including two that a reviewer reading the code would plausibly
have missed: a session-registry leak that only manifests after `MaxSessions`
open/close cycles (D-2), and a chunker whose output depended on how text arrived
(D-3).

Two gates could not run in this environment and are recorded as open findings
rather than treated as passed. The most significant unverified property is the
absence of data races.
