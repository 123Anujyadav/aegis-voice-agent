# Security Review

**Phase 11C** · `packages/go/speech` · Reviewed 2026-08-11

Speech is the most sensitive data this platform handles: it is what somebody
actually said, in their own words.

---

## 1. Content boundaries

Transcript text is conversation content. Three boundaries keep it in one place.

| Boundary | Rule | Enforced by |
|---|---|---|
| **Logs** | No transcript text at default level; no raw audio ever | `TranscriptSegment.Redacted()`, and `String()` is redacted too |
| **Metrics** | No content in any label | All labels are bounded enums or authored identifiers |
| **Events** | No text, no audio, no credentials | `TestSpeechEvent_CarriesNoContent`, by reflection |

### `String()` is redacted, deliberately

The commonest way transcript content reaches a log is somebody printing a struct
with `%v` while debugging something else. `TranscriptSegment.String()` therefore
returns the redacted form — identifiers, character count, confidence, provider —
so the careless path is also the safe one.
`TestTranscriptSegment_RedactedOmitsText` asserts both forms.

### Events carry a length, not a transcript

`SpeechEvent.CharCount` is the number of runes recognised. That is enough to
spot a provider emitting nothing, or emitting nonsense, and it tells nobody what
was said.

The test applied to `SpeechEvent` during design: *if this topic were retained
forever and could never be deleted, would that be a compliance failure?* It must
be no. Kafka cannot delete an individual record, so anything placed there is
retained for as long as the topic is, regardless of what an erasure request
later says.

`TestSpeechEvent_CarriesNoContent` enforces this **structurally**: it rejects any
field whose name contains `text`, `transcript`, `audio`, `pcm`, `payload`,
`sample`, `content`, `credential`, `token`, `secret` and others; any `[]byte`
field; and any `map` field. A later field addition cannot quietly break it.

### Metric labels cannot be widened by a provider

Every label is a bounded enum (`language`, `reason`, `stage`, `queue`,
`outcome`) or an authored `ProviderID`. Two clamps make that hold against
hostile input:

- `Language.Label()` returns `other` for anything over 16 characters or
  containing anything but alphanumerics and hyphen. A provider reporting a
  malformed tag cannot inject unbounded cardinality — or content — through it.
- `boundedReason()` lowercases, replaces spaces and hyphens with underscores,
  strips everything else, and truncates to 48 characters.

`TestSpeechMetrics_NoContentBearingLabels` and `TestBoundedReason_IsSafeAsALabel`
assert both.

## 2. No durable audio, and no audio at all past the boundary

`TranscriptSegment` has **no PCM field and will not get one**. A transcript
carrying the audio it was derived from would turn every transcript store into a
recording system — the obligation MEDIA-PCM-1 (Phase 11B) was written to bound.

This package stores no audio. Frames pass through `STTOrchestrator.Push` into a
provider stream and are never retained beyond it; synthesised frames pass through
the output channel and are never accumulated.

**Phase 11C introduces no persistence of any kind.** There is no store, no file,
no database adapter, no object storage.

## 3. Retention

| Scope | Bound | Where |
|---|---|---|
| Turns per assembler | 256, oldest evicted | `maxTurnsRetained` |
| Transitions per turn | 32, oldest evicted | `maxTurnHistory` |
| Turns per manager | 256, oldest evicted | `SpeechTurnManager.evictLocked` |
| Events in the recorder | 4 096 default, with a dropped count | `RecordingEventPublisher` |
| Session transcript | Discarded on `Close` | `assembler.Reset()` |

**Retention hooks are defined; no permanent retention policy is invented here.**
Durable transcript retention belongs to ADR-0012 (privacy, DPDP, consent,
retention), and this phase deliberately does not pre-empt it. What this package
guarantees is only that nothing grows without bound in memory and nothing
survives the session it belonged to.

## 4. Session ownership and isolation

Every session owns its own turn manager, assembler and both orchestrators. Two
sessions share no state.

Isolation is **structural**, not conventional:

- `TranscriptAssembler` belongs to one session and returns an error for a
  segment carrying any other session identifier. A provider callback with the
  wrong session cannot contaminate a transcript — it fails.
- `STTOrchestrator` discards results whose turn is not the live turn.
- No channel, queue or provider stream is reachable from another session.

`TestSession_CrossSessionIsolation` asserts a segment from session one is
refused by session two, and that session two cannot see session one's turn.
`TestAssembler_RefusesForeignSession` asserts the same at the unit level.

## 5. Cancellation cleanup

No goroutine outlives its session. Each orchestrator runs exactly one
consumer/pump goroutine which exits on context cancellation or on the provider
closing its channel; `Cancel` joins it before returning.

`TestSession_CloseWithActiveSTT` and `TestSession_CloseWithActiveTTS` snapshot
`runtime.NumGoroutine()` and assert it returns to baseline after close.

**D-2 in [ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md)** was a resource leak of
this class: closing a session freed its goroutines but leaked its runtime
registry entry, so an idle process would eventually refuse new sessions. It was
found by a benchmark and is now guarded by
`TestRuntime_ClosingASessionFreesItsSlot`.

## 6. Credentials

**No credential appears in this module, in any form.** No API key, no token, no
secret, no environment variable read, no config field capable of holding one.
`STTConfig` and `TTSConfig` carry session, turn, language, format, voice,
prosody and a timeout — nothing else.

Credential handling belongs to a provider adapter, outside this module. No test
uses a key, because no test calls a provider.

## 7. Untrusted input

Everything from a provider is untrusted: it originates from a third party
processing a caller's speech.

| Input | Guard |
|---|---|
| Transcript segment | `Validate()` — identifiers well-formed, confidence in range, times ordered, model name bounded |
| Session identity | Compared against the owning assembler |
| Sequence number | Never trusted to be contiguous, ordered or unique — reordering, duplication and gaps are first-class outcomes with their own counters |
| Language tag | Clamped by `Label()` before reaching a metric |
| Provider identifier | Authored, validated to lowercase alphanumerics, hyphen, underscore — because it becomes a metric label and a topic segment |
| Audio frame | `Validate()` plus a format match; refused, never resampled |

## 8. Denial of service

| Vector | Control |
|---|---|
| Unbounded sessions | `MaxSessions`, refused with `ErrBackpressure`, not queued |
| Unbounded inbound audio | 50-frame queue, `ErrBackpressure` |
| Unbounded transcripts | 256-segment queue; the assembler bounds retention independently |
| Unbounded TTS text | 32-chunk queue, `ErrBackpressure` |
| Unbounded outbound audio | 100-frame queue; blocks on context rather than growing |
| A dead provider consuming the latency budget | Circuit breaker fails fast with `ErrProviderCircuitOpen` |
| Unbounded event accumulation | Bounded recorder that counts what it dropped |
| Unbounded history growth | 32 transitions per turn, 256 turns per session |
| A runaway generation with no sentence end | `MaxChars` forces a chunk break |

**Session identifiers use `crypto/rand`**, not `math/rand`: they appear in events
that cross a service boundary, and a predictable session identifier lets anyone
who can reach the API guess a live one.

## 9. What this review does not cover

- **No `-race` run.** No C compiler in this environment, so the race detector
  could not run. Concurrency correctness rests on `-count=10 -shuffle=on`, three
  concurrency-bearing tests and two goroutine-settle tests — **none of which
  detects a data race the way the detector does.** This is the most significant
  unverified property in the module (F-1).
- **No transport security.** There is no network here. TLS, authentication and
  authorisation of a provider connection belong to the adapter phase.
- **No fuzzing.** Transcript and audio input are validated but not fuzzed.
- **No supply-chain review.** Not applicable — the dependency closure is the Go
  standard library plus three first-party modules.
- **No review of a durable transcript store**, because none exists. Any phase
  that adds one must redo this review, and must satisfy ADR-0012 rather than
  inventing a retention policy.
