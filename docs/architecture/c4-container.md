# C4 Level 2 — Containers

The deployable pieces and how they connect.

**Source ADRs:** 0004 (media), 0005–0007 (AI tier), 0008 (cloud), 0009 (data),
0010 (identity), 0011 (latency).

---

## Diagram

```mermaid
flowchart TB
    CPAAS["<b>CPaaS</b> 🇮🇳<br/>Exotel · Plivo"]
    SUB(["<b>Subscriber</b><br/>Android handset"])

    subgraph android["📱 Android client [Kotlin]"]
        APP["<b>:app</b><br/>DI graph · nav host"]
        TEL["<b>:core:telephony</b><br/>CallScreeningService<br/>MMI forwarding"]
        SEC["<b>:core:security</b><br/>Keystore key · Play Integrity"]
        NET["<b>:core:network</b><br/>Connect-RPC · offline queue"]
    end

    subgraph edge["Edge"]
        EDGEAPI["<b>edge-api</b> [Go]<br/>Only public ingress.<br/>Local JWT + PoP verification."]
    end

    subgraph telephony["Telephony plane — latency critical"]
        TGW["<b>telephony-gateway</b> [Go]<br/>SIP signalling · DID pool<br/>admission control"]
        RELAY["<b>media-relay</b> [Go]<br/>Internal audio bus · VAD<br/>jitter buffer · barge-in"]
        ORCH["<b>session-orchestrator</b> [Go]<br/>Call FSM · turn-taking<br/><b>owns the latency budget</b>"]
    end

    subgraph aitier["AI tier [Python]"]
        ASRG["<b>asr-gateway</b><br/>streaming STT · failover"]
        AIO["<b>ai-orchestrator</b><br/>tier routing · tools · safety"]
        TTSG["<b>tts-gateway</b><br/>segmenter · streaming TTS"]
        FRAUD["<b>fraud-engine</b><br/>scam classification"]
        TRANS["<b>transcript-service</b><br/>persistence · redaction"]
        PROMPT["<b>prompt-registry</b><br/>versioned prompts"]
    end

    subgraph app["Application plane [Go]"]
        IDENT["<b>identity</b><br/>MSISDN · devices · consent"]
        CONTACTS["<b>contacts-sync</b><br/>hashed matching"]
        NOTIF["<b>notification-fanout</b><br/>FCM"]
        BILL["<b>billing</b><br/>subscriptions"]
    end

    subgraph data["Data 🇮🇳 ap-south-1"]
        PG[("<b>Aurora PostgreSQL</b><br/>4 clusters, one per<br/>bounded context")]
        REDIS[("<b>Redis</b><br/>live session state<br/>rate limits · nonces")]
        KAFKA[["<b>MSK / Kafka</b><br/>event backbone<br/>outbox · DLQ"]]
        S3[("<b>S3</b><br/>audio · transcripts<br/>lifecycle-expired")]
    end

    VENDOR["<b>AI vendors</b><br/>STT 🇮🇳 · Claude 🌐 · TTS 🌐"]

    CPAAS ==>|"WSS media<br/>+ SIP signalling"| TGW
    TGW ==> RELAY
    RELAY ==>|"internal audio bus"| ORCH
    ORCH ==> ASRG
    ORCH --> AIO
    AIO --> PROMPT
    AIO --> FRAUD
    ORCH ==> TTSG
    TTSG ==> RELAY
    RELAY ==>|"8 kHz µ-law"| TGW

    ASRG --> VENDOR
    AIO --> VENDOR
    TTSG --> VENDOR

    SUB --- APP
    APP --- TEL
    APP --- SEC
    APP --- NET
    NET -->|"Connect-RPC / HTTPS"| EDGEAPI
    RELAY ==>|"WebRTC live-listen<br/>and takeover"| NET

    EDGEAPI --> IDENT
    EDGEAPI --> CONTACTS
    EDGEAPI --> BILL
    EDGEAPI --> TRANS

    ORCH -.->|"call.* events"| KAFKA
    KAFKA -.-> TRANS
    KAFKA -.-> NOTIF
    KAFKA -.-> BILL
    NOTIF -.->|"live-screening alert"| SUB

    ORCH <--> REDIS
    IDENT --> PG
    BILL --> PG
    TRANS --> PG
    TRANS --> S3
    TGW --> PG

    classDef go fill:#00ADD8,stroke:#007d9c,color:#000
    classDef py fill:#3776AB,stroke:#28557a,color:#fff
    classDef kt fill:#7F52FF,stroke:#5c3bb8,color:#fff
    classDef store fill:#4A5058,stroke:#31363c,color:#fff
    classDef ext fill:#6E7781,stroke:#4a5058,color:#fff

    class EDGEAPI,TGW,RELAY,ORCH,IDENT,CONTACTS,NOTIF,BILL go
    class ASRG,AIO,TTSG,FRAUD,TRANS,PROMPT py
    class APP,TEL,SEC,NET kt
    class PG,REDIS,KAFKA,S3 store
    class CPAAS,SUB,VENDOR ext
```

---

## Why the split is where it is

**Go for the telephony plane, Python for the AI tier.** This is not a taste
decision. The telephony plane holds thousands of long-lived stateful connections
under a hard latency budget with strict shutdown semantics — Go's concurrency and
memory profile fit that exactly. The AI tier is vendor-SDK-shaped, streaming, and
evolves fastest; Python is where those SDKs live and where the eval tooling is.

**`session-orchestrator` owns the latency budget.** It is the only service that
sees a whole turn end to end, so it is the only place the budget in ADR-0011 can
be enforced and attributed. It is also the reason the AI services do not call
each other directly — a mesh of AI-to-AI calls would make the budget
unattributable.

**`edge-api` is the only public ingress.** Everything else is in private subnets
(ADR-0008 §10). The one deliberate exception is the CPaaS media WebSocket into
`telephony-gateway`, which is authenticated and rate-limited (ADR-0002 §10).

**Kafka is off the hot path entirely.** Every dashed arrow in the diagram happens
after the turn or after the call. A producer that blocked a turn on a broker
acknowledgement would be a bug — which is why the transactional outbox exists
(ADR-0009 §5).

---

## Containers at a glance

| Container | Lang | Shape | On the latency budget? |
|---|---|---|---|
| `edge-api` | Go | Request/response | No |
| `telephony-gateway` | Go | Long-lived sessions | **Yes** — hops 2, 10 |
| `media-relay` | Go | Long-lived streams | **Yes** — hops 1, 3, 9 |
| `session-orchestrator` | Go | Stateful FSM | **Yes** — hop 5, owns the total |
| `asr-gateway` | Python | Streaming proxy | **Yes** — hop 4 |
| `ai-orchestrator` | Python | Streaming | **Yes** — hops 5, 6, 7 |
| `tts-gateway` | Python | Streaming | **Yes** — hop 8 |
| `fraud-engine` | Python | Request/response | Overlapped |
| `transcript-service` | Python | Event consumer | No |
| `prompt-registry` | Python | Request/response, cached | No |
| `identity` | Go | Request/response | No — auth is off the call path |
| `contacts-sync` | Go | Batch + request | No |
| `notification-fanout` | Go | Event consumer | No |
| `billing` | Go | Request/response | No |

Six of fourteen services are on the budget. The other eight can be slow without a
caller noticing — a distinction that should govern where optimisation effort goes.

---

## Two properties worth stating explicitly

**Auth is not on the call path.** A screened call is triggered by the carrier, not
by an authenticated client request. `identity` never appears in the voice
pipeline, and ADR-0011's budget contains no auth hop. This is deliberate: it means
an `identity` outage degrades app functionality without stopping call screening.

**The Android client is a thin shell over `core` modules.** `:app` holds the DI
graph, the nav host and the manifest, and nothing else. `:core:telephony` is the
only module that touches `CallScreeningService` and the MMI forwarding codes —
the two most dangerous surfaces in the client — and it is owned by both the
Android and telephony teams in CODEOWNERS.
