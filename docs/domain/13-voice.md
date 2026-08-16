# 13 · Voice

**Subdomain:** Supporting · **Prefix:** `VO` · **Topic domain:** `voice`

---

## 13.1 Purpose

Turn caller speech into text and assistant text into speech, inside the latency
budget, across three ASR and three TTS vendors — and persist audio only when a
current consent says we may.

## 13.2 Responsibilities

**Owns**

- ASR streaming, endpointing and barge-in detection (ADR-0005, ADR-0011)
- TTS synthesis and sentence-level streaming (ADR-0007)
- Provider routing and failover across primary / secondary / shadow
- `RecordingArtefact` — persisted audio, and the consent gate that permits it
- The voice catalogue the Subscriber chooses from
- **Amplitude frames** — the real data behind the honesty contract (U8)

**Does not own**

| Not owned | Owned by |
|---|---|
| What is said | AI Orchestration |
| The `Transcript` as a durable record | AI Orchestration |
| The media path and session lifetime | Telephony (Partnership) |
| Recording *consent* | Identity |
| Whether recording is *shown* | The interface, bound by U5 |

> **Voice produces `Utterance`s. AI Orchestration decides which become
> `TranscriptTurn`s.** An interim `Utterance` never crosses that line
> (INV-VO-2), which is why they are different types rather than one type with a
> flag.

---

## 13.3 Domain Entities

### `VoiceSession` — aggregate root, ephemeral

**Attributes**

```
id             : VoiceSessionId          INTERNAL  · EPHEMERAL
callSessionId  : CallSessionId <ref>     INTERNAL  · EPHEMERAL
asrRoute       : ProviderRoute           INTERNAL  · EPHEMERAL
ttsRoute       : ProviderRoute           INTERNAL  · EPHEMERAL
languages      : LanguageTag[]           PUBLIC    · EPHEMERAL
state          : VoiceSessionState       PUBLIC    · EPHEMERAL
openedAt       : Instant                 INTERNAL  · EPHEMERAL
lastFrameAt    : Instant?                INTERNAL  · EPHEMERAL
failoverCount  : Int                     INTERNAL  · SHORT
```

**Relationships** — One per `CallSession`, or one per Personal Assistant voice
interaction. References the call by identity; owns nothing durable.

**Lifecycle** — Opened when Telephony answers, closed when the call ends. Lives
**entirely in Redis** (ADR-0009 C3): it mutates every few hundred milliseconds
and is worthless once the call ends. Nothing here is a system of record.

**Validation Rules** — `languages` must be a subset of the `AssistantProfile`'s
configured set; a language not selected by the Subscriber is never used, even if
the caller speaks it. A session with no frames for longer than the stall
threshold transitions to a stalled state — it does not silently persist.

**Privacy Classification** — `EPHEMERAL` throughout. The audio flowing through
it is `SENSITIVE` and is never at rest here.

**Audit Requirements** — None at the session level. Recording start and stop are
audited on `RecordingArtefact`.

---

### `SpeechSegment`

**Attributes**

```
id            : SegmentId            INTERNAL  · EPHEMERAL
sessionId     : VoiceSessionId <ref> INTERNAL  · EPHEMERAL
speaker       : Speaker              PUBLIC    · EPHEMERAL
utterance     : Utterance            SENSITIVE · EPHEMERAL · residency-bound
isFinal       : Boolean              PUBLIC    · EPHEMERAL
startOffsetMs : Int                  INTERNAL  · EPHEMERAL
endOffsetMs   : Int?                 INTERNAL  · EPHEMERAL
provider      : AsrProvider          INTERNAL  · SHORT
revisionCount : Int                  INTERNAL  · EPHEMERAL
```

**Relationships** — Contained in `VoiceSession`. A **final** segment is
published to AI Orchestration, which may promote it to a `TranscriptTurn`. An
**interim** segment is never published beyond the live surface.

**Lifecycle** — Created on each ASR result. Interim segments are replaced in
place. On endpoint, one final segment is emitted and the interims are
**discarded**. Nothing survives the call.

**Validation Rules** — An interim segment cannot be marked final retroactively;
finality comes from the endpointing decision, not from confidence. A segment
that has revised more than the stability threshold within one second is
suppressed from display — text rewriting itself under the reader is noise, not
responsiveness.

**Privacy Classification** — `SENSITIVE` (it is speech content) and
`EPHEMERAL`. This combination is deliberate: the strictest sensitivity with the
shortest retention.

**Audit Requirements** — None. Nothing persists to audit.

---

### `RecordingArtefact` — aggregate root

The only durable thing this context owns, and the only one gated by consent.

**Attributes**

```
id              : RecordingId              INTERNAL  · STANDARD
callSessionId   : CallSessionId <ref>      INTERNAL  · STANDARD
subscriberId    : SubscriberId <ref>       INTERNAL  · STANDARD
objectRef       : ObjectReference          SENSITIVE · STANDARD · residency-bound
consentRecordRef: ConsentRecordId <ref>    INTERNAL  · LEGAL_HOLD
format          : AudioFormat              PUBLIC    · STANDARD
durationMs      : Int                      INTERNAL  · STANDARD
startedAt       : Instant                  INTERNAL  · STANDARD
stoppedAt       : Instant?                 INTERNAL  · STANDARD
expiresAt       : Instant                  INTERNAL  · STANDARD
deletedAt       : Instant?                 INTERNAL  · STANDARD
deletionReason  : DeletionReason?          PUBLIC    · STANDARD
```

**Relationships** — References a `CallSession`, a `Subscriber`, and — crucially
— the **specific `ConsentRecord` under which it was created**. That reference is
what makes the artefact's lawfulness provable after the fact.

**Lifecycle** — Created **only** if `CALL_RECORDING` consent is currently
granted (P-VO-1), by a path separate from the media relay (I9). Expires at
`expiresAt`, derived from the Subscriber's `RetentionPreference`, never
exceeding the statutory ceiling. Deleted on expiry, on consent withdrawal, on
call deletion, or on erasure — and `deletionReason` is retained so the interface
can say *why* audio is absent rather than showing a disabled control.

**Validation Rules** — Cannot be constructed without a resolvable, currently
granted `consentRecordRef` (INV-VO-5). `expiresAt` cannot exceed the
Subscriber's audio retention preference (INV-VO-4). An artefact whose consent is
later withdrawn must be deleted, and the deletion is reported with a count.

**Privacy Classification** — `objectRef` is `SENSITIVE` and residency-bound.
`consentRecordRef` is `LEGAL_HOLD` — it must outlive the audio, because proving
we were allowed to record survives the recording itself.

**Audit Requirements** — **Access** level. Every read, every playback, and every
deletion produces an `AuditEntry`. Playing audio through Administration is a
**separately audited action** from reading the transcript — they are different
intrusions.

---

### `VoiceProfile` — reference entity

**Attributes**

```
id           : VoiceId              PUBLIC · STANDARD
provider     : TtsProvider          INTERNAL · STANDARD
displayName  : String               PUBLIC · STANDARD
languages    : LanguageTag[]        PUBLIC · STANDARD
tier         : EntitlementTier      PUBLIC · STANDARD
sampleRef    : ObjectReference      PUBLIC · STANDARD
status       : VoiceStatus          PUBLIC · STANDARD
```

**Relationships** — Selected by Consumer's `AssistantProfile`. Reference data,
not per-subscriber.

**Lifecycle** — Published, deprecated, retired. **A retired voice is replaced by
a stated substitute, never silently swapped** — a caller who has heard this
subscriber's assistant before should not encounter a different person without
the subscriber knowing.

**Validation Rules** — A voice must support at least one of the Subscriber's
configured languages to be selectable. The **sample plays the actual
announcement text**, not a generic phrase, so what is previewed is what callers
hear.

**Privacy Classification** — `PUBLIC`. Catalogue data with no personal content.

**Audit Requirements** — **Change** level on publication and retirement.

---

## 13.4 Value Objects

| Value object | Definition | Notes |
|---|---|---|
| `Utterance` | text + `LanguageTag` + confidence + `isFinal` | The atom of speech recognition. Distinct from `TranscriptTurn` |
| **`AmplitudeFrame`** | normalised RMS + timestamp, sampled at 30 Hz | **The data behind the honesty contract.** Absence produces no frame — never a synthetic one (U8) |
| `EndpointDecision` | silenceMs + decision + confidence | Window 250 ms p50 / 350 ms p95 (ADR-0011) |
| `BargeInSignal` | detectedAt + frameOffset | Must preempt TTS within one frame interval, ≤ 20 ms |
| `ProviderRoute` | primary + secondary + shadow, with current | Google STT v2 🇮🇳 / Deepgram / Sarvam shadow (ADR-0005); ElevenLabs Flash / Cartesia / Sarvam shadow (ADR-0007) |
| `AudioFormat` | codec + sample rate + channels | |
| `LanguageTag` | BCP-47, e.g. `hi-IN`, `en-IN`, `ta-IN` | |
| `Speaker` | `CALLER` · `ASSISTANT` | Never a name |
| `VoiceSessionState` | `OPENING` · `LISTENING` · `SPEAKING` · `STALLED` · `CLOSING` · `CLOSED` · `FAILED` | |
| `DeletionReason` | `RETENTION_EXPIRED` · `CONSENT_WITHDRAWN` · `CALL_DELETED` · `ERASURE` | Shown to the Subscriber so absence is explained, not implied |
| `SynthesisRequest` | text + voice + language + sentence boundary | Sentence-level streaming is fixed by ADR-0007 |

### Why `AmplitudeFrame` is a modelled value object

It would be easy to treat amplitude as a rendering detail. It is not: Invariant
U8 makes "the orb animates from real data or stays static" a product guarantee,
and a guarantee that lives only in the UI layer is a guarantee that will be
optimised away by someone adding a nice pulse animation.

Modelling the frame — and modelling its **absence** as the absence of a frame
rather than a zero value — puts the honesty contract in the domain, where the
interface can only reflect it.

---

## 13.5 Aggregates

| Aggregate | Root | Contains | Store |
|---|---|---|---|
| **VoiceSession** | `VoiceSession` | `SpeechSegment[]` | **Redis only.** Never a system of record |
| **RecordingArtefact** | `RecordingArtefact` | — | `content` Aurora + S3 |
| **VoiceProfile** | `VoiceProfile` | — | Reference data |

```
┌────────────────────────────────────────────┐
│ VoiceSession  «root»   EPHEMERAL — REDIS   │
│  callSessionId · asrRoute · ttsRoute       │
│  ┌──────────────────────────────────────┐  │
│  │ SpeechSegment[]                      │  │
│  │  interim ──▶ replaced in place       │  │
│  │  final   ──▶ published to AI         │  │
│  │  INTERIM NEVER BECOMES A TURN        │  │
│  └──────────────────────────────────────┘  │
│  AmplitudeFrame stream ──▶ live surface    │
│   (no data = no frame, never synthetic)    │
└────────────────────────────────────────────┘
                    │
          consent gate (P-VO-1)
                    ▼
┌────────────────────────────────────────────┐
│ RecordingArtefact  «root»    DURABLE       │
│  objectRef (S3, SENSITIVE)                 │
│  consentRecordRef ◀── LEGAL_HOLD, outlives │
│                       the audio itself     │
│  expiresAt ≤ subscriber retention pref     │
└────────────────────────────────────────────┘
```

---

## 13.6 Domain Services

| Service | Responsibility | Notes |
|---|---|---|
| `AsrRoutingService` | Select and fail over across primary / secondary / shadow | Shadow traffic is evaluated, never served |
| `TtsRoutingService` | Same for synthesis | Sentence-level streaming so the first word arrives fast |
| `EndpointingService` | Decide when a caller has finished a turn | 250 ms p50 / 350 ms p95 — a fixed number from ADR-0011, not a tuning knob |
| `BargeInDetectionService` | Detect caller speech over assistant speech and preempt within one frame | ≤ 20 ms. Interrupting the assistant is the single most important courtesy in a phone conversation |
| **`RecordingConsentGate`** | The only path by which a `RecordingArtefact` can be created | Re-checks consent at the moment of creation, not at session start — consent can be withdrawn mid-call |
| `LanguageDetectionService` | Identify the caller's language within the configured set | Never selects a language the Subscriber did not configure |

---

## 13.7 Repositories

`RecordingArtefactRepository` · `VoiceProfileRepository`

**`VoiceSession` has no repository.** It is ephemeral state in Redis, and
giving it a repository would invite treating it as durable — which is precisely
the mistake ADR-0009 C3 exists to prevent.

---

## 13.8 Domain Events

| Event | Payload | Consumers |
|---|---|---|
| `voice.session.opened.v1` | voiceSessionId, callSessionId, asrProvider, ttsProvider | AI, Analytics |
| `voice.utterance.finalised.v1` | voiceSessionId, segmentId, speaker, languageTag, confidence, durationMs | **AI Orchestration** |
| `voice.session.barge_in_detected.v1` | voiceSessionId, latencyMs | AI, Analytics |
| `voice.session.stalled.v1` | voiceSessionId, stallMs, stream | AI, Telephony, Administration |
| `voice.provider.failed_over.v1` | from, to, reason, stream | **Administration**, Analytics |
| `voice.recording.started.v1` | recordingId, callSessionId, consentRecordRef | Consumer, Administration |
| `voice.recording.stopped.v1` | recordingId, durationMs | Consumer, Billing |
| `voice.recording.deleted.v1` | recordingId, reason | Consumer, Administration |
| `voice.session.closed.v1` | voiceSessionId, outcome, failoverCount | Analytics |

**`voice.utterance.finalised.v1` carries no text.** It carries the segment
identifier, the speaker, the language and the confidence. AI Orchestration
fetches the content across the Partnership boundary. This is Invariant I7 applied
to the highest-volume event in the platform — and it is why a Kafka topic that
can never be deleted is not a compliance failure.

---

## 13.9 Commands

| Command | Refused when |
|---|---|
| `OpenVoiceSession(callSessionId, languages, routes)` | Call not answered; language outside the configured set |
| `StreamCallerAudio(voiceSessionId, frames)` | Session not open |
| `SynthesiseSpeech(voiceSessionId, text, voiceId)` | Voice not entitled; language unsupported by the voice |
| `StartRecording(callSessionId)` | **Consent not currently granted** |
| `StopRecording(recordingId)` | Already stopped |
| `DeleteRecording(recordingId, reason)` | — (always permitted) |
| `CloseVoiceSession(voiceSessionId)` | — |
| `PublishVoiceProfile(profile)` | Administration only |
| `RetireVoiceProfile(voiceId, substituteId)` | **Substitute required.** A retirement without a stated substitute is refused |

---

## 13.10 Queries

| Query | Scope |
|---|---|
| `GetVoiceCatalogue(languages, tier)` | Public catalogue, filtered by entitlement |
| `GetRecordingAvailability(callSessionId)` | Owner. Returns available / never-recorded / deleted-with-reason |
| `GetVoiceSessionState(voiceSessionId)` | Internal, live surface only |
| `GetProviderHealth()` | Administration |

**There is no `GetRecordingContent` query in this context.** Playback is a
streamed, separately audited operation, and modelling it as a query would invite
caching audio somewhere it should not be.

---

## 13.11 Policies

| # | Policy |
|---|---|
| **P-VO-1** | A `RecordingArtefact` is created **only** when `CALL_RECORDING` consent is currently granted, re-checked at creation. Consent can be withdrawn mid-call |
| **P-VO-2** | Interim utterances are never persisted, never announced to a screen reader, and never placed in a notification |
| **P-VO-3** | Amplitude frames are emitted only while genuinely produced. **Absence yields no frame** — never a synthesised one (U8) |
| **P-VO-4** | Provider failover is automatic and always emits an event. There is no silent switch |
| **P-VO-5** | When consent is withdrawn, delete every affected `RecordingArtefact` and report the count |
| **P-VO-6** | When retention is lowered, re-derive `expiresAt` on existing artefacts immediately |
| **P-VO-7** | When an ASR stream stalls, say so. The call continues; the transcript is marked delayed rather than silently lagging |
| **P-VO-8** | When a voice is retired, migrate affected profiles to the stated substitute and notify the Subscriber |

---

## 13.12 Invariants

| # | Invariant | Source |
|---|---|---|
| **INV-VO-1** | The media path never writes audio to disk. `RecordingArtefact` is created by a separate, consent-gated path | **I9** |
| **INV-VO-2** | An interim `Utterance` can never become a `TranscriptTurn` | |
| **INV-VO-3** | Barge-in preempts synthesis within one frame interval (≤ 20 ms) | ADR-0011 |
| **INV-VO-4** | A `RecordingArtefact`'s `expiresAt` never exceeds the Subscriber's audio retention preference | ADR-0012 |
| **INV-VO-5** | A `RecordingArtefact` cannot be constructed without a resolvable, currently granted `consentRecordRef` | ADR-0012 |
| **INV-VO-6** | No `AmplitudeFrame` is ever generated from anything other than real audio | **U8** |
| **INV-VO-7** | A language not configured by the Subscriber is never used for synthesis or recognition | |
| **INV-VO-8** | `VoiceSession` is never a system of record; nothing durable depends on its survival | ADR-0009 C3 |

---

## 13.13 State Machines

### `VoiceSession`

```
   OPENING ──ready──▶ LISTENING ⇄ SPEAKING ──call ends──▶ CLOSING ──▶ CLOSED
      │                   │  ▲         │                              «terminal»
      │                   │  └─barge-in┘  (≤ 20 ms, one frame)
      │                   │
      │              no frames > threshold
      │                   ▼
      │               STALLED ──recovers──▶ LISTENING
      │                   │
      └──provider fails───┴──all routes exhausted──▶ FAILED «terminal»
                                                      │
                                          call continues; transcript
                                          marked unavailable. Takeover
                                          remains available.
```

`FAILED` does not end the call. Telephony owns the call; Voice failing means the
Subscriber loses the transcript, not the ability to take over.

### `RecordingArtefact`

```
              consent granted
                    │
   (none) ──────────▼──────── RECORDING ──stop──▶ STORED
                                                    │
              ┌─────────────────────────────────────┤
              ▼                ▼            ▼       ▼
       RETENTION_EXPIRED  CONSENT_    CALL_DELETED  ERASURE
              │           WITHDRAWN        │          │
              └───────────────┴────────────┴──────────┘
                              ▼
                          DELETED «terminal»
                     reason retained, so the
                     interface can explain the
                     absence rather than show
                     a disabled control
```

---

## 13.14 Ownership

| Aspect | Value |
|---|---|
| Team | `callscreen/ai` |
| Services | `asr-gateway`, `tts-gateway` (Python 3.12) |
| Durable store | `content` Aurora, schema `voice` |
| Objects | **S3** — audio, SSE-KMS, region-locked, lifecycle rules implementing ADR-0012 retention |
| Ephemeral | Redis — session and stream state |
| CODEOWNERS | `docs/domain/13-voice.md`, `services/python/asr-gateway/**`, `services/python/tts-gateway/**` |

---

## 13.15 External Dependencies

| Dependency | Purpose | Guard |
|---|---|---|
| **Google STT v2** 🇮🇳 primary | Streaming recognition | **ACL.** Region-pinned for residency (I2) |
| **Deepgram** 🌐 secondary | Failover recognition | **ACL.** Cross-border — permitted only under the four-condition consent gate, and routed accordingly |
| **Sarvam** 🇮🇳 shadow | Evaluation only | **Never serves traffic.** Shadow results are scored, not returned |
| **ElevenLabs Flash** 🌐 primary | Synthesis | ACL. Sentence-level streaming |
| **Cartesia** 🌐 secondary | Synthesis failover | ACL |
| **Sarvam** 🇮🇳 shadow | Synthesis evaluation | Never serves |
| **S3** | Audio at rest | SSE-KMS, lifecycle rules, region-locked |

**Residency is a routing decision, not an afterthought.** A cross-border ASR
provider may only receive audio where the four-condition consent gate permits it
(I2), which means provider selection is partly a consent question and the ACL
must know it.

---

## 13.16 Security Constraints

| # | Constraint |
|---|---|
| 1 | **Audio is never written to disk by the media path** (I9). Persistence happens only through `RecordingConsentGate` |
| 2 | **Consent is re-checked at the moment of artefact creation**, not at session start |
| 3 | **Interim utterances never leave the live session** and are never persisted |
| 4 | **`voice.utterance.finalised.v1` carries no text** (I7) |
| 5 | **Amplitude frames cannot be synthesised.** There is no code path producing a frame without audio (U8) |
| 6 | **Cross-border provider routing is consent-gated** (I2) |
| 7 | **Shadow providers never serve production traffic** and never receive traffic the primary would not have received |
| 8 | **Playing audio through Administration is separately audited** from reading the transcript |
| 9 | **`consentRecordRef` is `LEGAL_HOLD`** and outlives the audio it authorised |
| 10 | **Deletion reasons are retained**, so the interface explains absence instead of implying we are withholding something |
