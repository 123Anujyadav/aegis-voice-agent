# Architecture Decision Records

Immutable records of every decision that is expensive to reverse.

**An ADR is never edited once accepted.** It is superseded by a new ADR that
links back to it. The history of *why we were wrong* is the most valuable content
in this directory, and editing it away destroys the only reason to keep records
at all.

---

## Index

| ADR | Title | Status | Decision in one line |
|---|---|---|---|
| [0000](0000-template.md) | Template | — | Copy this to start a new ADR |
| [0001](0001-monorepo-structure-and-tooling.md) | Monorepo with native per-language tooling | Accepted | Monorepo; Gradle/Go/uv/buf natively; Bazel deferred |
| [0002](0002-telephony-architecture.md) | Telephony architecture | Accepted | **Carrier-side screening via conditional forwarding, with an on-device pre-filter** |
| [0003](0003-carrier-cpaas-selection.md) | Carrier / CPaaS selection | Accepted | Exotel primary, Plivo secondary, direct SIP trunk as the exit |
| [0004](0004-media-transport.md) | Media transport | Accepted | Provider WebSocket at the carrier leg, WebRTC at the app leg |
| [0005](0005-streaming-stt-architecture.md) | Streaming STT | Accepted | Google STT v2 primary 🇮🇳, Deepgram secondary, Sarvam in shadow |
| [0006](0006-llm-routing-strategy.md) | LLM routing strategy | Accepted | Four-tier ladder on Claude; Sonnet 5 carries the conversation |
| [0007](0007-streaming-tts-architecture.md) | Streaming TTS | Accepted | ElevenLabs Flash primary, Cartesia secondary, sentence-level streaming |
| [0008](0008-cloud-infrastructure.md) | Cloud infrastructure | Accepted | AWS `ap-south-1` primary, `ap-south-2` warm standby, EKS on Graviton |
| [0009](0009-database-and-event-backbone.md) | Database and event backbone | Accepted | Aurora per bounded context, Redis, MSK, S3 |
| [0010](0010-authentication-and-device-trust.md) | Authentication and device trust | Accepted | MSISDN identity, hardware-bound PoP tokens, Play Integrity |
| [0011](0011-end-to-end-latency-budget.md) | End-to-end latency budget | Accepted | **p50 ≤ 900 ms, p95 ≤ 1 500 ms**, allocated per hop with named owners |
| [0012](0012-privacy-dpdp-consent-retention.md) | Privacy — DPDP, consent, retention | Accepted | Caller announcement as lawful basis; audio off by default; India-resident |
| [0013](0013-metrics-exposition-format.md) | Metrics exposition format | Accepted | Prometheus text v0.0.4, stdlib adapter on the existing health port; **tracing deferred** |
| [0014](0014-correlation-identity.md) | Correlation identity | Accepted | **One `CorrelationID`, owned by `packages/go/runtime`**; four prior declarations become type aliases |
| [0015](0015-postgresql-driver-pgx.md) | PostgreSQL driver | Accepted | **pgx/v5** — the platform's first third-party dependency; durable goldens behind the existing port |
| [0016](0016-intent-classification.md) | Intent classification | Accepted | **Implement the existing `conversation.IntentClassifier` port**; deterministic rule/lexicon default, no model, no second engine |
| [0017](0017-service-wiring.md) | Service wiring | Accepted | **Register a `platform.Runner` in `services/go/voice` owning a `voiceintel.Bridge`**; existing seams only, provider-fed pipeline stages deferred |

---

## When an ADR is required

Write one when the answer to *"how expensive is this to undo in twelve months?"*
is **expensive**. Concretely:

- A new service, or any change to a service boundary
- A new external vendor or dependency with lock-in
- A breaking change to `contracts/`
- A datastore choice, or a change to retention or residency policy
- A change to the auth model or a trust boundary
- Any deviation from the Phase 1 repository conventions

**Not required** for library-level choices inside one service that are cheap to
reverse.

## Lifecycle

```
Proposed → Accepted → Deprecated → Superseded by ADR-NNNN
```

Scaffold a new one with:

```bash
task docs:adr TITLE="short title"
```

## Format

Every ADR from 0002 onward uses the 17-section structure: Context · Problem
Statement · Constraints · Considered Options · Decision · Why This Option Was
Selected · Trade-offs · Alternatives Rejected · Operational Impact · Security
Impact · Cost Impact · Performance Impact · Scalability Impact · Migration
Strategy · Risks · Future Review Trigger · References.

Every ADR states its **future review trigger** as an observable condition. "When
we grow" is not a trigger; "when p95 exceeds 1 500 ms for 7 days" is.
