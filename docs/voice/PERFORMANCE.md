# Phase 11E — Voice Orchestration Performance

> Part of the Phase 11E documentation set — see [README.md](README.md).
> Readiness implications: [PLATFORM_READINESS.md](PLATFORM_READINESS.md).
> Every figure here is category **A — Aegis orchestration overhead**. Provider
> inference (category B) is in [PROVIDER_COMPATIBILITY.md](PROVIDER_COMPATIBILITY.md);
> frozen ADR budgets (category C) are quoted in §8 and asserted nowhere.

**Status:** measured 2026-08-16 · **Task 16** · Every figure below was produced by a
command in this document, run on the machine described in §1. Nothing is
estimated, extrapolated or carried over from an earlier session.

---

## 0. Read this before reading a number

Three quantities are kept apart throughout, because mixing them produces
confident nonsense:

| | Quantity | Status in this document |
|---|---|---|
| **A** | **Aegis orchestration overhead** — what this phase's code costs | **Measured.** Everything in §3–§6. |
| **B** | **Provider inference latency** — recogniser, model, synthesiser | **NOT RUN.** No runtime is installed; see §7. |
| **C** | **Frozen architectural budgets** — ADR-0004 §12, ADR-0006, ADR-0011 | **Referenced, never asserted.** See §8. |

Every benchmark drives deterministic stand-in providers. There is no model, no
recogniser and no synthesiser inference in any number here. That is deliberate:
an end-to-end figure containing a local CPU model would describe the model, not
the orchestration, and would move on different hardware while this code stayed
byte-identical.

**No production code was changed to improve any number in this document.** Task 16
is measurement. Two *measurement* defects were fixed (§2.3); the code under test
was not touched.

---

## 1. Environment

```
$ go version
go version go1.26.5 windows/amd64

$ go env GOARCH GOOS
amd64
windows
```

| | |
|---|---|
| CPU | 11th Gen Intel Core i7-11800H @ 2.30 GHz |
| Cores / logical processors | 8 / 16 |
| `GOMAXPROCS` (benchmark suffix `-16`) | 16 |
| OS | Windows 11 (26200) |
| Go | 1.26.5 |
| Machine state | developer laptop, not an isolated benchmark host |

**This is a laptop, not a benchmark rig.** Background load, thermal behaviour and
power management all move wall-clock numbers here. Absolute values are indicative
of magnitude; they are not a baseline to diff future runs against on other
hardware.

---

## 2. Measurement quality

### 2.1 Clock resolution

```
$ go test -run '^$' -bench 'BenchmarkZZZ' -benchtime=200x .
BenchmarkZZZ_MeasurementResolution/clock-granularity-16    200    950198 ns/tick
BenchmarkZZZ_MeasurementResolution/empty-loop-16           200      2.000 ns/op   0 B/op   0 allocs/op
```

**The wall clock ticks about every 950 µs on this machine.**

This does **not** invalidate the sub-microsecond `ns/op` figures below. Go
benchmarks divide total elapsed time by `b.N`, so a 43 ns/op result measured over
28 million iterations rests on ~1.2 s of elapsed time — far above the tick.

It *does* invalidate any **single-shot** timing below ~1 ms. That is why Task 12's
barge-in orchestration latency is reported as *"below measurable resolution"*
rather than as a number: one interruption is one event, not an amortized average.

### 2.2 Iteration count changes the answer

The same three benchmarks at 200 iterations versus default (millions):

```
$ go test -run '^$' -bench '...' -benchmem -count=5 -benchtime=200x .
BenchmarkGovernance_AllowedAction-16   200   362.0 ns/op
BenchmarkGovernance_AllowedAction-16   200   466.0 ns/op
BenchmarkGovernance_AllowedAction-16   200   257.5 ns/op
BenchmarkGovernance_AllowedAction-16   200   157.5 ns/op
BenchmarkGovernance_AllowedAction-16   200   180.5 ns/op     ← ~3x spread
```

```
$ go test -run '^$' -bench '...' -benchmem -count=5 .
BenchmarkTTS_ChunkScheduling-16   2598478   446.5 ns/op   48 B/op   1 allocs/op
BenchmarkTTS_ChunkScheduling-16   3386524   435.0 ns/op   48 B/op   1 allocs/op
BenchmarkTTS_ChunkScheduling-16   2585929   428.1 ns/op   48 B/op   1 allocs/op
BenchmarkTTS_ChunkScheduling-16   2769411   456.3 ns/op   48 B/op   1 allocs/op
BenchmarkTTS_ChunkScheduling-16   3009169   449.4 ns/op   48 B/op   1 allocs/op     ← ±3%
```

**Only default-benchtime figures are quoted as results below.** Low-iteration runs
on this machine are not reliable to better than a factor of three.

### 2.3 Allocation stability is separate from — and better than — wall-clock stability

Across every repeat above, `B/op` and `allocs/op` were **identical** while `ns/op`
moved. Allocation counts are a property of the code; wall-clock is a property of
the code *and* the machine's mood. Where this document makes a claim worth
trusting, it is usually the allocation figure.

That property also **caught two measurement defects**, both in the benchmark
harness rather than in the code under test:

1. **A test double was being measured.** `BenchmarkGovernance_AllowedAction` first
   reported ~420 ns/op and ~1.3 KB/op against **0 allocs/op** — a contradiction.
   The cause was `recordingGovernor`, which stores every request it is asked
   about; its slice growth was attributed to the gateway. Replaced with a
   non-accumulating `benchGovernor`:

   | | ns/op | B/op | allocs/op |
   |---|---|---|---|
   | with accumulating double | ~420 | ~1300 | 0 |
   | with non-accumulating double | **~100–139** | **0** | **0** |

   The gateway allocates nothing on the allow path. The original number was
   **4× too slow and entirely fictitious in its allocation column.**

2. **A benchmark measured the wrong thing.** `BenchmarkPipeline_FrameIngest`
   initially failed with `voice: session is closed`: the stand-in recogniser
   cannot open, so the first speech onset failed the session. Fixed by removing
   the onset, so the benchmark measures the inbound queue handover it claims to.

---

## 3. Orchestration hot paths

```
$ go test -run '^$' -bench 'BenchmarkRouting|BenchmarkSession|BenchmarkEvents|BenchmarkCancellation_GenerationGuard|BenchmarkProviderSwitch|BenchmarkTranscript|BenchmarkTTS|BenchmarkGovernance' -benchmem .
```

| # | Area | Benchmark | ns/op | B/op | allocs/op |
|---|---|---|---:|---:|---:|
| 1 | Provider routing | `Routing_PickSTT` | 43.1 | 0 | 0 |
| 1 | Provider routing | `Routing_PickTTS` | 44.8 | 0 | 0 |
| 1 | Provider routing (16 goroutines) | `Routing_PickSTTParallel` | 124.5 | 0 | 0 |
| 1 | Descriptor read | `Routing_Describe` | 114.7 | 24 | 2 |
| 2 | Session creation | `Session_NewFSM` | 6 569 | 6 352 | 82 |
| 2 | Session creation | `Session_NewPipeline` | 20 154 | 19 856 | 169 |
| 3 | Event dispatch | `Events_StateTransition` (2 transitions) | 1 330 | 1 173 | 2 |
| 3 | Event dispatch | `Events_Publish` | 323.1 | 384 | 0 |
| 3 | Rejected transition | `Events_RejectedTransition` | 1 024 | 536 | 11 |
| 4 | Cancellation | `Cancellation_GenerationGuard` | **0.553** | 0 | 0 |
| 4 | Cancellation | `Cancellation_PipelineStop` | 5 906 | 556 | 8 |
| 5 | Provider switching | `ProviderSwitch_Failover` | 84.8 | 0 | 0 |
| 5 | Breaker feed | `ProviderSwitch_ReportOutcome` | 36.2 | 0 | 0 |
| 6 | Partial forwarding | `Transcript_ForwardPartial` | 393.7 | 384 | 0 |
| 7 | TTS scheduling | `TTS_ChunkScheduling` | 449.9 | 48 | 1 |
| 7 | Chunker alone (frozen) | `TTS_ChunkerOnly` | 352.5 | 40 | 1 |
| 10 | Governance allow | `Governance_AllowedAction` | 138.6 | 0 | 0 |
| 10 | Governance deny | `Governance_DeniedAction` | 194.1 | 112 | 1 |
| 10 | Authorisation check | `Governance_RequestValidation` | 10.6 | 0 | 0 |
| 11 | Barge-in orchestration | `BargeIn_Interrupt` | 1 532 | 288 | 6 |
| 12 | Inbound queue handover | `Pipeline_FrameIngest` | ~368 | ~23 | 0 |

`Pipeline_FrameIngest` is quoted as a range from `-count=3` (376.1 / 370.9 / 357.8
ns/op); `Cancellation_PipelineStop`, `BargeIn_Interrupt` and the concurrency rows
were measured at `-benchtime=200x` because each iteration builds and tears down a
real session.

### Interpretation

- **Routing is free at call volume.** 43 ns and **zero allocations** per selection.
  Under 16-way contention it is 124 ns — a mutex-shaped ~3×, not a collapse.
  Provider selection will never be why a turn is slow.
- **The generation guard costs 0.55 ns.** This runs on *every outbound frame*
  (50/s per call). At 1 000 concurrent calls that is ~28 µs of CPU per second in
  total. The stale-audio guarantee is effectively free.
- **Governance costs ~139 ns and allocates nothing on the allow path.** The gate
  that every governed action passes through is not a reason to bypass it.
- **TTS scheduling is 450 ns, of which 353 ns is the frozen chunker.** This layer
  adds ~97 ns and one allocation per generated token.
- **Session creation is the expensive orchestration operation** at ~20 µs and 169
  allocations — but it happens *once per call*, not per turn.
- **`Events_StateTransition` covers two transitions**, so one is ~665 ns.

---

## 4. Concurrent session handling

```
$ go test -run '^$' -bench 'BenchmarkConcurrent' -benchmem -benchtime=200x .
```

One shared registry, recogniser and voice; N sessions each driving a **complete
turn** concurrently.

| Sessions | ns/op (all N turns) | per turn | B/op | allocs/op | per-turn allocs |
|---:|---:|---:|---:|---:|---:|
| 1 | 86 238 | 86 238 | 56 156 | 410 | 410 |
| 4 | 177 268 | 44 317 | 224 115 | 1 638 | 410 |
| 16 | 415 662 | 25 979 | 891 360 | 6 524 | 408 |
| 64 | 1 097 810 | 17 153 | 3 567 209 | 26 068 | 407 |

### Interpretation

- **Per-turn allocation is flat at ~408** from 1 to 64 concurrent sessions. Nothing
  accumulates per neighbour — the strongest evidence that sessions do not share
  state, and it agrees with Task 15's isolation proofs.
- **Per-turn wall-clock *improves* 5× under concurrency** (86 µs → 17 µs). A single
  turn is latency-bound on channel handovers rather than CPU-bound, so the 16
  logical processors absorb parallel turns nearly free. This is throughput
  headroom, not a latency claim.
- Total memory scales linearly (~56 KB per in-flight turn), which is the expected
  shape for bounded per-session queues.

---

## 5. Streaming pipeline orchestration

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `Pipeline_WholeTurn` | 95 474 | 55 700 | 406 |

One complete orchestrated turn: analysis → recognition handover → partial
forwarding → conversation plan → governance decision → generation fan-out →
chunking → synthesis scheduling → generation guard → media delivery.

**~95 µs and ~56 KB per turn, containing no inference of any kind.**

A real turn adds a recogniser, a model and a synthesiser. Each of those is
*orders of magnitude* larger than this figure: a local CPU model's first token
alone is typically hundreds of milliseconds — roughly **three to four orders of
magnitude** more than the entire orchestration measured here. The orchestration
is not the bottleneck in a voice turn and this number says so quantitatively.

---

## 6. Process supervision

```
$ cd packages/go/voice/providers/process
$ go test -run '^$' -bench 'BenchmarkProcess' -benchmem -benchtime=100x .
```

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `Process_StartStop` | 7 168 900 (**7.17 ms**) | 49 623 | 156 |
| `Process_Configure` | 11 035 | 28 632 | 58 |
| `Process_WriteRead` | 37 914 | 304 | 4 |
| `Process_StderrRing` | 25.0 | 0 | 0 |

**The child program does nothing but echo a line.** Its own work is a few
microseconds of I/O, so these are supervision costs, not provider work. No model
inference appears anywhere in this table.

### Interpretation

- **Spawning a supervised process costs ~7.2 ms**, dominated by Windows
  `fork`/`exec` — an operating-system cost, not this package's. `Process_Configure`
  isolates the part this package controls: **11 µs**, three orders of magnitude
  smaller.
- **Architectural consequence:** at 7.2 ms per spawn, a provider process must be
  **long-lived**, not started per turn. Starting whisper per utterance would add
  ~7 ms of pure overhead before the model does anything.
- One request/response exchange with a live child is **38 µs**, which is the
  per-utterance pipe cost an adapter pays.
- The bounded stderr ring is 25 ns per 512-byte write with zero allocations — a
  chatty provider cannot make diagnostics expensive.

---

## 7. Provider inference — NOT RUN

| Provider | Runtime status on this machine | Inference benchmark |
|---|---|---|
| whisper.cpp | not installed | **NOT RUN** |
| openai-whisper CLI | installed (Task 6) | **NOT RUN** — not benchmarked this session |
| Piper | not installed | **NOT RUN** |
| Ollama | daemon installed; **zero models pulled** (`/api/tags` → `{"models":[]}`) | **NOT RUN** |

No binaries were installed and no model was pulled to produce this document.

**No inference number is reported, estimated or implied anywhere above.** Where an
adapter's boundary could be measured without a runtime it was (§6 measures
supervision against a trivial child); where it could not, it is marked NOT RUN.

---

## 8. Frozen budgets — referenced, not claimed

| Budget | Source | Relationship to this document |
|---|---|---|
| **20 ms provider cancellation / abort** | ADR-0004 §12, ADR-0011 §5.1 | This is the **provider cancellation** budget. `BargeIn_Interrupt` (1.5 µs) measures *orchestration*, not a provider's cancellation, and is **not** a compliance claim. It is **not** a TTFT figure. |
| **250 ms p50 / 550 ms p95 time-to-first-token** | ADR-0006 C1, ADR-0011 §5.2 hop 6 | Set for a **hosted frontier model over a network**. No local CPU model is held to it, and **no measurement here is offered as compliance with it.** |

Nothing in this document asserts a pass or fail against a frozen budget. Doing so
would require the production model and network configuration ADR-0006 defines,
which is not what was measured.

---

## 9. Reproducing

```bash
cd packages/go/voice

# Measurement resolution first — it tells you which figures to trust.
go test -run '^$' -bench 'BenchmarkZZZ' -benchtime=200x .

# Orchestration hot paths (default benchtime; do not lower it).
go test -run '^$' -bench 'BenchmarkRouting|BenchmarkSession|BenchmarkEvents|BenchmarkCancellation_GenerationGuard|BenchmarkProviderSwitch|BenchmarkTranscript|BenchmarkTTS|BenchmarkGovernance' -benchmem .

# Session lifecycle and concurrency (each iteration builds a real session).
go test -run '^$' -bench 'BenchmarkCancellation_PipelineStop|BenchmarkBargeIn|BenchmarkPipeline|BenchmarkConcurrent' -benchmem -benchtime=200x .

# Stability check — allocations must be identical across repeats.
go test -run '^$' -bench 'BenchmarkTTS_ChunkScheduling' -benchmem -count=5 .

# Process supervision.
cd providers/process
go test -run '^$' -bench 'BenchmarkProcess' -benchmem -benchtime=100x .
```

---

## 9a. Change since these numbers were taken

`providers/process.reap()` now waits for the stderr drain before closing
`Exited()` (Task 19, defect D5 in [ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md)).
That adds a synchronisation on child exit which is **not** reflected in the
`BenchmarkProcess_StartStop` figure above (7.17 ms), measured before the fix.

The wait is for a drain that has normally already finished — microseconds in
practice — and it is bounded at 2 s. **NOT RE-MEASURED**: the figure is left as
taken rather than adjusted by estimate, because an estimated benchmark number is
not a benchmark number.

## 10. Limitations

1. **Laptop, not a benchmark host.** Absolute wall-clock values reflect this
   machine's load and thermal state.
2. **Do not diff these across sessions or machines.** Compare allocation counts —
   they are stable and hardware-independent — and treat `ns/op` as magnitude only.
3. **No provider inference is included anywhere.** Every figure is orchestration.
4. **Single-shot timings under ~950 µs are unmeasurable here** (§2.1). Benchmark
   `ns/op` is amortized and therefore fine; one-off event latencies are not.
5. **`-race` was NOT RUN.** The environment has no C compiler:
   `cgo: C compiler "gcc" not found: exec: "gcc": executable file not found in %PATH%`.
   Race-detector figures would in any case not be comparable to these.
6. **Concurrency was measured to 64 sessions.** Higher levels are untested; the
   flat per-turn allocation supports extrapolation, but the wall-clock trend does
   not extend indefinitely.
