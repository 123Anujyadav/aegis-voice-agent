# Media Evaluation Report

**Phase 11B** · `packages/go/media` · Evaluated 2026-08-10

---

## 1. Gate results

Every command was run from `packages/go/media/`.

| Gate | Command | Result |
|---|---|---|
| Build | `go build ./...` | **PASS** |
| Vet | `go vet ./...` | **PASS** |
| Tests | `go test ./...` | **PASS — 105 tests, 0 failures** |
| Repetition | `go test -count=10 ./...` | **PASS** (1.947 s) |
| Standalone module | `GOWORK=off go build ./...` | **PASS** |
| Benchmarks | `go test -bench=. -benchmem` | **PASS — 12 benchmarks** |
| Dependencies | `go.mod` | **PASS — exactly 2, both first-party and dependency-free** |
| Frozen phases untouched | file scan | **PASS — only `packages/go/media/` and `docs/media/`** |
| **Race detector** | `go test -race ./...` | **NOT RUN — no C compiler in this environment** |
| **Lint** | `golangci-lint run` | **NOT RUN — golangci-lint is not installed in this environment** |

Toolchain: `go1.26.5 windows/amd64`.

## 2. Test count

**105 passing, 0 failing.** The suite began this phase at **84 passing, 8
failing**; 13 tests were added.

## 3. Coverage of the eight required categories

The brief requires unit, integration, streaming, concurrency, stress, recovery,
buffer and failure-injection testing. All eight are represented.

| Category | Representative tests | Count |
|---|---|---|
| **Unit** | `TestFrame_*`, `TestFormat_*`, `TestState_*` (10), `TestIDs_*`, `TestConfig_*`, `TestFramePool_*`, `TestReasonCode_*` | ~35 |
| **Integration** | `TestLifecycle_*` (5), `TestPipeline_*` (6), `TestShutdown_*` (3) | 14 |
| **Streaming** | `TestJitter_*` (6), `TestJitterEstimator_*`, `TestMediaClock_*` (3), `TestFrameClock_*` (2), `TestSilenceFrame_*` | 13 |
| **Concurrency** | `TestConcurrency_ManyStreamsManyFrames`, `TestConcurrency_ProducerAndConsumerOnOneStream`, `TestConcurrency_SweepRunsAlongsideFrameChurn` | 3 |
| **Stress** | `TestStress_LongRunningStreamDoesNotGrow` (10,000 frames), `TestStress_SustainedFrameThroughput` | 2 |
| **Recovery** | `TestRecovery_*` (4), `TestRestore_*` (3) | 7 |
| **Buffer** | `TestBuffer_*` (9), `TestBufferConfig_*`, `TestBackpressure_StalledConsumerIsBoundedByCapacity` | 11 |
| **Failure injection** | `TestFailure_BufferFullDropsAndCounts`, `TestFailure_StallDetection`, `TestFailure_StoreOutageDoesNotStopStreams`, `TestFailure_PausedStreamsAreNotStalled` | 4 |

No category is unrepresented, so no category test was added to fill a gap. The
tests added this phase target specific defects rather than categories.

## 4. Tests added this phase

| Test | Guards |
|---|---|
| `TestRead_PumpsThroughWhenRingIsEmpty` | D-1 — read-through delivery |
| `TestJitter_CleanSequenceIsFullyAccepted` | D-3(a) — a clean 50 fps sequence is never dropped |
| `TestJitter_RefusesFramesFarAheadOfTheData` | D-3(a) — the guard still refuses genuine outliers |
| `TestJitter_LostFrameDoesNotStallTheStream` | D-4 — the permanent-stall defect |
| `TestSnapshot_CapturesJitterHeldAudioNotJustTheRing` | D-2 — two-stage capture |
| `TestSnapshot_BoundAppliesToBothStagesCombined` | D-2 — the bound applies to the union |
| `TestSnapshot_AudioIsOffByDefault` | MEDIA-PCM-1 §1 |
| `TestBackpressure_StalledConsumerIsBoundedByCapacity` | D-6 — refusal is reported, memory bounded |
| `TestMediaEvent_TopicShape` | Topic naming contract |
| `TestMediaEvent_CarriesNoAudio` | MEDIA-PCM-1 §4, by reflection |
| `TestRecordingEventPublisher_IsBounded` | Unbounded-recorder memory leak |
| `TestStreamStateEvent_CoversTheStatesThatMatter` | Event coverage of the states operators act on |
| `TestZeroAllocation_SteadyState` | The zero-allocation claim in `doc.go` |

## 5. Determinism

Verified by construction rather than by repetition alone:

- Every timer and ticker in non-test code measures against `runtime.Clock`, with
  one documented exception (the shutdown drain budget — F-3).
- The jitter buffer works in **media time**, so the same input sequence produces
  the same output sequence regardless of how fast the test ran.
- `TestRecovery_IsDeterministic` asserts repeatability of the recovery sweep.
- Ten consecutive full-suite runs pass without flake.

## 6. What these tests do **not** establish

The most important section of this report.

- **They do not establish freedom from data races.** `-race` was never run —
  the environment has no C compiler and Go's detector requires cgo. Three
  concurrency tests pass and the suite is stable across ten runs, but neither
  detects a race the way the detector does. **Any environment with a C toolchain
  should run `go test -race ./...` before this module carries production
  traffic.** This is finding F-1.
- **They do not establish behaviour on a real network.** There is no socket, no
  packet loss, no real jitter. Arrival variation is injected through a
  `FakeClock`, and a `FakeClock` is a model of a network, not a network.
- **They do not establish carrier compatibility.** No RTP, no SIP, no provider.
  Whether a real carrier's frame cadence, sequence numbering or timestamp
  semantics match this engine's assumptions is untested by construction.
- **They do not establish codec behaviour.** PCM only.
- **They do not establish endurance.** The longest run is 10,000 frames (200 s of
  audio). Nothing here runs for hours, and the allocation rate measured in
  [PERFORMANCE.md](PERFORMANCE.md) (~20.7 MB/s of garbage at 1,000 streams) is
  exactly the kind of pressure whose effects appear over hours, not seconds.
- **They do not establish audio quality.** Nothing in this suite listens. That
  gap-filled silence is inserted in the right place is asserted structurally, by
  sequence number and flag — not perceptually.
- **They do not establish multi-process or multi-host correctness.** Recovery is
  tested against an in-memory store; the semantics of a real Redis or Aurora
  adapter under partition are untested because no such adapter exists yet.
- **They do not establish security of a durable audio store**, because
  MEDIA-PCM-1 forbids one existing in this phase.

## 7. Assessment

The module meets the Phase 11B brief as specified. Six defects were found and
fixed, four of them exposed by measurement rather than predicted from the failing
tests, and two of those — the permanent stall on a lost frame (D-4) and the silent
destruction of frames under overload (D-6) — were availability and correctness
faults serious enough that shipping without finding them would have been a
production incident rather than a bug report.

Two gates could not be satisfied in this environment — the race detector (no C
compiler) and `golangci-lint` (not installed). Both are recorded as open findings
rather than treated as passed. Neither was worked around, and no claim in this
report depends on them.
