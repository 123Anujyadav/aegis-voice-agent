# Security Review

**Phase 11D** · `packages/go/audiointel`, `packages/go/audiobridge` · 2026-08-11

---

## 1. The question this module has to answer

This engine listens to every call the platform handles. The only question that
matters is: **where does the audio go?**

The answer is that it is read once, inside one function, and never leaves it.

## 2. Where audio exists, and for how long

| Location | Holds audio | Lifetime |
|---|---|---|
| `media.Frame.Payload` | Yes — borrowed from Phase 11B's ring | Until 11B's buffer wraps |
| `FrameAnalyzer.Analyze` | Reads it, in place | The call |
| `FrameFeatures` | **No** — scalars only | Bounded window |
| `FeatureWindow` | **No** | 100 entries, fixed |
| `AudioEvent` | **No** | Published |
| `Analysis` (returned) | **No** | Caller's |
| Metrics | **No** | Process |
| Logs | **No** | — |

`FrameAnalyzer.Analyze` is the only function in this module that dereferences a
payload. Everything downstream works from scalars, so §24 is satisfied
structurally rather than by policy somebody has to remember.

### Enforced, not asserted

| Check | What it proves |
|---|---|
| `TestAudioEvent_CarriesNoAudio` | No event field at any depth is a byte sequence, map, interface or pointer |
| `TestScenarios_NoPCMEscapesTheEngine` | Overwrites every frame payload after analysis and asserts retained results are unchanged — nothing held a reference |
| `assertNoByteSlices` over `Analysis` | The return path carries no byte sequence, walking into `media` types too |

The event test also rejects field names containing `payload`, `audio`, `sample`,
`pcm`, `frame`, `buffer`, `recording`, `transcript`, `text`, `content`,
`utterance`, `phone`, `number`, `msisdn`, `caller`, `credential`, `token` or
`key`. Blunt on purpose, with one named exception (`FrameCount`, a count of
frames rather than a frame).

## 3. Is there any debug facility that can contain PCM?

**No.** §24 asks this explicitly and the answer is unqualified.

- There is **no snapshot type** in this module. Phase 11B has one and it can
  carry PCM behind an opt-in flag; this module has no equivalent, because it has
  no buffered audio to restore.
- `String()` on every type renders scalars. `FrameFeatures.String` prints
  levels in dBFS and a sample *count*.
- `Explanation.String` prints thresholds and measurements.
- The logger records **lifecycle only** — sessions opening, closing, being
  refused at capacity. Nothing per-frame, and nothing carrying a level trace or
  a classification stream, because a debug log is the easiest place for a
  recording to appear by accident.
- The test fixtures generate audio; they never read or write one.

If a future change adds a snapshot, it must state whether it can contain PCM and
this section must be updated. Phase 11B's `SnapshotConfig.IncludeAudio` is the
precedent for how to do that: default false, bounded, and documented as a
recording system with the retention obligations that implies.

## 4. Metric labels

Every label value is a constant declared in `classifications.go`. Nothing
derived from input reaches a metric.

`TestMetrics_LabelNamesAreBoundedAndDeclared` walks the registry and rejects any
label name containing `session`, `call`, `turn`, `correlation`, `stream`,
`phone`, `number`, `msisdn`, `caller`, `subscriber`, `user`, `account`,
`transcript`, `text`, `content`, `utterance`, `word`, `token`, `key`,
`credential`, `secret`, `id` or `uuid`.

`TestSession_MetricLabelsAreFromTheDeclaredVocabulary` goes further: it drives
real audio through a session and then checks that **every label value actually
emitted** is in the declared vocabulary. The name check proves the schema; this
proves the data.

The tempting labels here are the dangerous ones. A phone number would be
genuinely useful for debugging one caller's bad audio and would put subscriber
PII into a system with no erasure path. A session identifier would give the
backend one time series per call.

Per-session detail lives in `SessionStats`, pulled on demand.

## 5. The event topic

`audio.intel.<event>.v1`. Kafka cannot delete an individual record, so anything
placed there is retained for as long as the topic is, regardless of what an
erasure request later says.

The test applied during design, the same one Phases 11B and 11C applied: *if
this topic were retained forever and could never be deleted, would that be a
compliance failure?* It must be no.

`AudioEvent` carries identifiers, bounded classifications and numeric
measurements. A level in dBFS says how loud somebody was; it says nothing about
what they said and cannot be reassembled into anything that does.

`EventDetail` is a **fixed struct and not a map**, deliberately. A
`map[string]any` on an event is a hole in every review this document describes:
nobody can tell by reading the type what a producer might put in it, the
reflection test cannot prove anything about its contents, and the first time
somebody needs "just a bit more context" the transcript ends up in it.

## 6. Identifiers

`SessionID` is six bytes of millisecond timestamp plus ten of `crypto/rand`.
`math/rand` would make a live session identifier guessable by anyone who can
reach the API. The timestamp prefix makes identifiers roughly sortable, which is
what makes a log grep useful.

`math/rand` **is** used, in `fixtures.go`, for signal generation — where
reproducibility is the requirement and nothing mints an identifier or a secret.
That use is annotated at the call site.

`CallID`, `TurnID` and `Language` are opaque and supplied by the caller, and all
three are validated as labels because they reach events.

## 7. Credentials

None. This module has no network client, no provider SDK, no configuration file
reader and nothing that could hold a key. `TestDependencies_NoForbiddenImports`
rejects every provider SDK by name.

## 8. Supply chain

```
go list -deps ./...
```

Returns `packages/go/media`, `packages/go/metrics`, `packages/go/runtime` and
the standard library. Nothing else.

`audiobridge` adds `packages/go/speech`, itself a stdlib-closure module.

**Zero third-party dependencies** across both modules — the property every
module in this plane has held since Phase 10A, and the reason this platform has
no third-party supply-chain surface to review.

## 9. Session isolation

Two sessions share no detector, no window, no floor estimate and no lock. There
is no package-level mutable state.

`TestSession_IsIsolated` opens 24 sessions in parallel, feeds half of them
speech and half silence, and asserts each reports only what it was given. A
session reporting speech it was never given would be reading another's state.

## 10. Denial of service

| Vector | Control |
|---|---|
| Unbounded sessions | `MaxSessions`, checked **before** the analyser allocates |
| Unbounded memory per session | Every window fixed at construction; ~11 KB, constant |
| Unbounded event volume | `speech_detected` emitted once per run, not per frame |
| Unbounded metric cardinality | Every label a declared enum |
| Malformed frames | Refused with a bounded reason; format validated at construction |
| Blocking on a broker | Publisher errors are ignored; a broker outage cannot stop detection |

The capacity check runs before construction deliberately: a session's windows are
allocated at construction, so building one and then refusing it would allocate
exactly the memory the bound exists to protect.

## 11. Residual risks

| Risk | Status |
|---|---|
| **A caller could hold a `media.Frame` and read the payload themselves** | Out of scope. The payload arrives from Phase 11B already; this module does not widen that exposure. |
| **`-race` has not been run** | Environment limitation, not a design one. No C compiler. See [EVALUATION_REPORT.md](EVALUATION_REPORT.md) §1. |
| **A future snapshot facility could carry PCM** | Not present today. §3 records the obligation to update this document. |
| **Overlap detection cannot separate echo from double-talk** | Documented, confidence-based, not a security issue but a correctness limitation — see [OVERLAP_DETECTION.md](OVERLAP_DETECTION.md). |

## 12. Conclusion

No durable audio storage. No PCM in events, metrics, logs or returned results.
No credentials. No third-party dependencies. Bounded memory, bounded labels,
bounded event volume, isolated sessions.

The one gate that could not be executed is the race detector, and it is reported
as NOT RUN rather than passed.
