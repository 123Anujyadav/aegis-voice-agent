# Audio Intelligence Evaluation Report

**Phase 11D** · `packages/go/audiointel`, `packages/go/audiobridge` · Evaluated 2026-08-11

Every number in this report came from a command shown in it. Nothing is
estimated and nothing is projected.

---

## 1. Quality gates

Run from `packages/go/audiointel/` unless noted.

| Gate | Command | Result |
|---|---|---|
| Format | `gofmt -l .` | **PASS** — no output |
| Vet | `go vet ./...` | **PASS** |
| Tests | `go test ./...` | **PASS — 169 top-level, 353 including subtests, 0 failures** |
| Repetition and order | `go test -count=10 -shuffle=on ./...` | **PASS** |
| Standalone module | `GOWORK=off go build ./...` | **PASS** |
| Benchmarks | `go test -bench=. -benchmem` | **PASS — 15 benchmarks** |
| Dependency graph | `go list -deps ./...` | **PASS — 3 first-party modules + stdlib** |
| No vendor SDK | `TestDependencies_NoVendorNamesInProductionCode` | **PASS — 9 mentions, all disclaiming comments** |
| Bridge module | `go test ./...` in `audiobridge` | **PASS — 6 tests** |
| Python lint | `ruff check tools/audio-fixtures/` | **PASS** |
| Python types | `mypy tools/audio-fixtures/analyse.py` | **PASS** |
| Python self-check | `python tools/audio-fixtures/analyse.py` | **PASS** |
| **Race detector** | `go test -race ./...` | **NOT RUN** |
| **Lint** | `golangci-lint run` | **NOT RUN** |

Toolchain: `go1.26.5 windows/amd64`, Python 3.12.10.

### The two gates that could not run

**Race detector.** `go test -race` reports `-race requires cgo`. Forcing
`CGO_ENABLED=1` then reports:

```
cgo: C compiler "gcc" not found: exec: "gcc": executable file not found in %PATH%
```

No C compiler is installed in this environment. Phases 11B and 11C reported the
same limitation. **It is reported as NOT RUN, not as passed**, and no claim in
this report depends on it.

Partial mitigation, not a substitute: `go test -count=10 -shuffle=on` passes,
and the concurrency tests (`TestSession_IsIsolated` with 24 parallel sessions,
`TestSession_ConcurrentLifecycleIsSafe`, `TestRegistry_ConcurrentAccessIsSafe`
with 16 workers) exercise the shared structures. The design also minimises the
race surface: analysis is single-threaded per session with no goroutines, so the
only concurrent structure is the sharded registry.

**golangci-lint** is not installed. Same treatment.

## 2. The 25 mandatory scenarios (§21)

All 25 are present in `TestScenarios` and passing. Every fixture is generated
from arithmetic with a fixed seed — no microphone, no recorded audio, nothing
checked into the repository.

| # | Scenario | Subtest | What is asserted |
|---:|---|---|---|
| 1 | Pure silence | `01_pure_silence` | No onsets, no endpoints |
| 2 | Constant background noise | `02_constant_background_noise` | No onsets, no endpoints |
| 3 | Low-volume speech | `03_low_volume_speech` | One turn, cleanly ended |
| 4 | Normal speech | `04_normal_speech` | One turn |
| 5 | Loud speech | `05_loud_speech` | One turn |
| 6 | Speech → silence | `06_speech_to_silence` | One turn, one endpoint candidate |
| 7 | Silence → speech | `07_silence_to_speech` | One onset; opening silence classified initial |
| 8 | Speech → short pause → speech | `08_speech_short_pause_speech` | **One** onset, zero offsets |
| 9 | Speech → long pause | `09_speech_long_pause` | One turn; pause classified long |
| 10 | Background noise → speech | `10_background_noise_then_speech` | One onset over a noisy line |
| 11 | Speech → background noise | `11_speech_then_background_noise` | One onset, one offset |
| 12 | Sudden transient noise | `12_sudden_transient_noise` | Five door slams, zero onsets |
| 13 | Clipping | `13_clipping` | Detected; reason is clipping |
| 14 | Missing frame | `14_missing_frame` | `missing_sequence` detected, gap opened |
| 15 | Duplicate frame | `15_duplicate_frame` | `duplicate` detected |
| 16 | Out-of-order frame | `16_out_of_order_frame` | `out_of_order` detected |
| 17 | Timestamp discontinuity | `17_timestamp_discontinuity` | `timestamp_jump` detected |
| 18 | AI speaking → caller interrupts | `18_ai_speaking_caller_interrupts` | Exactly one barge-in, delivered |
| 19 | AI speaking → background noise | `19_ai_speaking_background_noise` | **Zero** barge-ins |
| 20 | Double-talk | `20_double_talk` | Overlap confirmed with non-zero confidence |
| 21 | Rapid speech | `21_rapid_speech` | One turn |
| 22 | Long speech | `22_long_speech` | 30 s continuous → one onset |
| 23 | Hindi speech fixture | `23_hindi_timing` | One onset — geminates do not split the turn |
| 24 | Hinglish fixture | `24_hinglish_code_switching` | One onset across four code switches |
| 25 | Devanagari fixture | `25_devanagari_metadata` + `TestScenarios_LanguageMetadataIsCarriedNotInterpreted` | Metadata carried unmodified; changes no decision |

### Scenario 13 reports `degraded`, not `unusable`

The fixture drives a syllabic signal to 1.4× full scale, so it clips on syllable
peaks and not in the closures between. The worst windowed clip ratio measured
**0.0050**, between `DegradedClipRatio` (0.0020) and `MaxClipRatio` (0.0200).

`degraded` is the correct verdict for occasional clipping. An earlier version of
this test asserted `unusable`; the classifier was right and the assertion was
wrong. Sustained clipping reaching `unusable` is covered directly by
`TestQuality_EveryClassIsReachable`.

## 3. Scenarios 23–25: what these fixtures prove, and what they do not

**The Hindi and Hinglish fixtures are not Hindi or Hinglish.** They are not
speech in any language. They contain no phonemes, no formants and no words.
Producing genuine Hindi speech requires a synthesiser, which is Phase 11C's
concern.

**No recognition accuracy is claimed, measured or implied by this phase.** STT
accuracy belongs to Phase 11C's evaluation.

What the fixtures reproduce is the **timing structure** these algorithms
actually measure. Every detector here counts milliseconds and compares decibels;
none can tell one language from another, and the only way a language can affect
a result is through its rhythm.

| Trait modelled | Why it matters to a detector |
|---|---|
| Syllable-timed rhythm (evenness 0.9 vs English 0.45) | Changes the energy modulation the VAD's third feature measures |
| Geminate closures every 4th syllable | A deep pause **inside** a word — the case most likely to defeat a too-short hangover |
| Utterance-final lengthening (1.6× vs 1.25×) | Changes where the energy trend sits when the endpoint window opens |
| Code-switch pauses (100 ms) | Land between an inter-word gap and an endpoint — the boundary `EndpointPolicy` tunes |

**The fixtures are measurably different signals**, which
`TestScenarios_HindiAndEnglishTimingDiffer` asserts so the coverage is real
rather than decorative:

| Fixture | Min frame RMS | Mean frame RMS |
|---|---:|---:|
| Hindi timing | **0.000343** | 0.034632 |
| English timing | 0.001848 | 0.038566 |

The Hindi fixture reaches five times lower in its closures — the geminates — at
a comparable mean level. That is the property that would break a hangover, and
the engine handles it: one onset, one endpoint.

### Devanagari

Devanagari is a **script**. There is no acoustic Devanagari — nothing in a
waveform indicates what alphabet a transcript will be written in.

The only honest thing to test at this layer is that language metadata from Phase
11C survives the pipeline untouched. `TestScenarios_LanguageMetadataIsCarriedNotInterpreted`
makes two claims across five tags including `hi-in-deva`:

1. The tag reaches every published event unmodified.
2. **The tag changes nothing.** Sessions fed byte-identical audio and differing
   only in their tag produce identical state sequences, identical onset and
   endpoint counts, and identical accumulated confidence to the last bit.

The second claim is what stops the first from being misleading. A tag that
altered behaviour would be a claim this phase has not earned.

## 4. Required test categories (§20)

| Category | Representative tests |
|---|---|
| Unit | `TestFrameAnalyzer_KnownSignals`, `TestWindow_StatisticsAreArithmeticallyCorrect`, `TestVAD_ConfidenceFormula` |
| State machine | `TestVAD_TransitionTableIsComplete`, `TestVAD_EveryDeclaredEdgeIsReachedByRealAudio`, `TestOverlap_TransitionTableIsWellFormed` |
| Concurrency | `TestSession_IsIsolated` (24 parallel), `TestSession_ConcurrentLifecycleIsSafe`, `TestRegistry_ConcurrentAccessIsSafe` (16 workers) |
| Determinism | `TestScenarios_DeterministicReplay`, `TestVAD_IsDeterministic`, `TestNoise_IsDeterministic`, `TestWindow_StatisticsAreReproducibleRegardlessOfHistory` |
| Noise | `TestNoise_*` (11 tests) |
| Silence | `TestSilence_*` (6 tests) |
| Speech onset | `TestVAD_OnsetIsBackdatedToWhereSpeechBegan`, `TestVAD_EmitsExactlyOneOnsetPerRun` |
| Speech offset | `TestVAD_OneSilentFrameDoesNotEndSpeech`, `TestVAD_HangoverSpansAShortPauseAndEndsOnALongOne` |
| Endpoint | `TestEndpoint_*` (9 tests) |
| Barge-in | `TestBargeIn_*` (11 tests) + `TestBridge_*` (6) |
| Overlap | `TestOverlap_*` (11 tests) |
| Frame loss | `TestContinuity_*` (8 tests) |
| Timestamp | `TestContinuity_DetectsEveryTransportFault`, `TestContinuity_OutOfOrderDoesNotPoisonTheTimestampReference` |
| Configuration validation | `TestConfig_EveryFieldHasARejectingCase` (58 fields), `TestConfig_CrossSectionInvariants` |
| Session isolation | `TestSession_IsIsolated` |
| Failure injection | `TestBargeIn_RecordsAControllerRefusal`, `TestBridge_PropagatesAPhase11CRefusal`, `TestSession_RefusesFramesAfterClose` |
| Stress | `TestWindow_NeverGrows` (10⁶ pushes), `TestScenarios/22_long_speech` (30 s) |
| Race-sensitive | `-count=10 -shuffle=on` across the suite |

## 5. Anti-flapping and detection bounds

| Property | Bound | Measured |
|---|---|---|
| Onsets under 60 ms speech/silence alternation | ≤ 11 in 136 frames | **1** |
| Steady tone treated as speech before reclassification | 440 ms | **340 ms** |
| Quality class changes under per-frame alternation | 0 | **0** |
| Onsets across one continuous noisy utterance (150 frames) | 1 | **1** |
| Noise floor movement across 250 gated speech frames | 0 | **0** |

## 6. Latency

Reported in full in [PERFORMANCE.md](PERFORMANCE.md).

| Hop | Frozen budget | Measured |
|---|---|---|
| Endpoint silence window | 250 ms p50 / 350 ms p95 (ADR-0011 §5.2 hop 1) | worst **260 ms** across 10 turns |
| Barge-in orchestration | 20 ms (ADR-0004 §12) | **0 s** across 10 runs, below clock resolution |

**Neither is a production distribution.** Ten synthetic turns is not a p50, and
this report does not present it as one. No new SLA is created by this phase.

## 7. Allocation

| Path | B/op | allocs/op |
|---|---:|---:|
| `FrameAnalyzer.Analyze` | 0 | **0** |
| Full detector chain | 0 | **0** |
| `Session.Analyze` (production publisher) | **0** | **0** |
| `Session.Analyze` (recording publisher) | 5 | 0 |

The 5 B belongs to the test recorder, which retains every event. Memory per
session is about 11 KB, constant.

## 8. Dependency verification

```
$ go list -deps ./... | grep callscreen
packages/go/metrics
packages/go/runtime
packages/go/media
packages/go/audiointel
```

Three first-party modules and the standard library. No third-party dependency in
either module.

## 9. What this evaluation does not establish

Stated plainly, because a report that only lists passes is not a report.

- **Nothing about real speech.** Every fixture is synthetic. Behaviour on real
  telephony audio, real Hindi, real code-switching or a real noisy line is
  unmeasured by this phase.
- **No recognition accuracy, in any language.** That is Phase 11C's.
- **No production latency distribution.** The figures are synthetic
  measurements against frozen budgets, not observed percentiles.
- **No thread-safety proof.** The race detector could not run.
- **No echo/double-talk separation.** Architecturally impossible here — see
  [OVERLAP_DETECTION.md](OVERLAP_DETECTION.md).
- **No clause-boundary detection.** At a 250 ms endpoint window an
  inter-sentence pause and a turn end are the same measurement.

## 10. Reproducing this

```bash
cd packages/go/audiointel
gofmt -l .
go vet ./...
go test ./...
go test -count=10 -shuffle=on ./...
GOWORK=off go build ./...
go test -bench=. -benchmem -run=XXX -benchtime=200ms ./...
go list -deps ./...

cd ../audiobridge && go test ./...

cd ../../.. && python tools/audio-fixtures/analyse.py
```

Every fixture is seeded from `audiointel.FixtureSeed`, so every run produces
byte-identical audio on every machine.
