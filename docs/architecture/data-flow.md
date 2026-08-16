# Data Flow

Where personal data goes, under what classification, and for how long.

**Source ADRs:** 0009 (stores), 0012 (classification, retention, residency).
**Enforcement:** `contracts/proto/callscreen/common/v1/annotations.proto`.

---

## Classification

Every field in every contract carries a classification. **An unclassified field
fails closed** — it is treated as `PERSONAL`. That default is the most important
line in the annotations file.

| Class | Logging | At rest | Example |
|---|---|---|---|
| `PUBLIC` | Verbatim | Plain | Region code, duration |
| `INTERNAL` | Verbatim | Plain | Shard id, service name |
| `PERSONAL` | **Keyed HMAC** | Encrypted | Phone number, device id, IP |
| `SENSITIVE` | **Keyed HMAC** | Encrypted + access-audited | **Call audio, transcript, contacts** |
| `SECRET` | **Never** | Never persisted plain | Tokens, attestation nonces |

Classification is not documentation. It drives log redaction, retention jobs,
egress policy and analytics exclusion **automatically**, in the platform
libraries — so privacy is a property of the system rather than a discipline
engineers must remember.

---

## Flow of caller data

The caller never consented to us. Their data is the most sensitive thing in the
system.

```mermaid
flowchart TB
    CL(["<b>Caller</b><br/>no prior relationship"])

    CL -->|"voice · SENSITIVE"| MR["media-relay<br/><b>never writes to disk</b>"]
    CL -->|"number · PERSONAL"| TG["telephony-gateway"]

    MR ==>|"audio frames"| AS["asr-gateway"]
    AS -->|"in-country default"| G["Google STT 🇮🇳"]
    AS -.->|"consent-gated<br/>+ audited"| D["Deepgram 🌐"]

    AS -->|"transcript · SENSITIVE"| AI["ai-orchestrator"]
    AI -->|"delimited untrusted text"| LLM["Claude API 🌐<br/>consent-gated"]
    AI --> FR["fraud-engine"]
    FR -->|"verdict · PERSONAL<br/>a judgement about a person"| SO["session-orchestrator"]

    SO -.->|"identifiers only"| K[["Kafka<br/><b>no personal content</b>"]]
    K -.-> TR["transcript-service"]

    TR -->|"policy-gated"| DEC{"consented?"}
    DEC -->|"transcript opt-in"| PG[("Aurora content<br/><b>90 days</b>")]
    DEC -->|"audio opt-in<br/><b>OFF by default</b>"| S3[("S3<br/><b>30 days</b>")]
    DEC -->|"neither"| DROP["<b>discarded</b>"]

    TG -->|"metadata"| PGT[("Aurora telephony<br/><b>12 months</b>")]
    FR -->|"verdict"| PGF[("Aurora content<br/><b>90 days</b>")]

    classDef sens fill:#8B2635,stroke:#5e1923,color:#fff
    classDef ok fill:#1F7A3D,stroke:#145227,color:#fff
    classDef store fill:#4A5058,stroke:#31363c,color:#fff
    classDef ext fill:#8B5A00,stroke:#5e3d00,color:#fff
    class CL,MR sens
    class DROP,K ok
    class PG,S3,PGT,PGF store
    class G,D,LLM ext
```

**Three properties to notice.**

`media-relay` **never writes audio to disk**, regardless of consent. Persistence
is an explicit, policy-gated action by `transcript-service` — never a side effect
of relaying (ADR-0004 §10).

**Audio recording is off by default.** Both the subscriber's opt-in and the
caller having heard the announcement are required. Either absent, no audio is
persisted.

**Kafka carries identifiers, not content.** This is not a style preference — you
cannot delete a single record from a Kafka topic, so putting personal data there
would make erasure impossible. It constrains every event schema we will ever
write (ADR-0009 §10).

---

## Flow of subscriber data

```mermaid
flowchart LR
    S(["<b>Subscriber</b><br/>consented customer"])

    S -->|"msisdn · PERSONAL"| I["identity"]
    S -->|"public key only<br/>private key never leaves<br/>the Keystore"| I
    S -->|"consent record"| I
    S -->|"contacts · SENSITIVE"| CS["contacts-sync"]

    CS -->|"<b>hashed on device</b><br/>raw contacts never<br/>reach the server"| PGI[("Aurora identity")]
    I --> PGI
    I -->|"subscription"| BL["billing"]
    BL --> PGB[("Aurora billing<br/><b>8 years</b> — statutory")]

    I -.->|"consent state"| K[["Kafka"]]
    K -.->|"gates every<br/>data-flow decision"| ALL["all services"]

    classDef store fill:#4A5058,stroke:#31363c,color:#fff
    classDef ok fill:#1F7A3D,stroke:#145227,color:#fff
    class PGI,PGB store
    class CS ok
```

**Contacts are hashed on the device.** The server never receives a raw contact
list — matching happens against hashes. This is the largest single data-minimisation
decision in the client, and it means a breach of `contacts-sync` does not disclose
anybody's address book.

**Consent state is an event, not a periodic refresh.** A revocation must take
effect within one call, which is why it propagates over Kafka rather than being
polled (ADR-0012 §9).

---

## Retention

| Data | Class | Default | User range | Basis |
|---|---|---:|---|---|
| Call audio | `SENSITIVE` | **30 d** | 7–90 d | Consent |
| Transcript | `SENSITIVE` | **90 d** | 7–180 d | Consent |
| Fraud verdict | `PERSONAL` | 90 d | Fixed | Legitimate use |
| Call metadata | `PERSONAL` | 12 mo | Fixed | Service provision |
| Consent records | `PERSONAL` | Account + 3 y | Fixed | Legal obligation |
| Audit logs | `PERSONAL` | `LEGAL_HOLD` | Fixed | Security |
| Billing | `PERSONAL` | 8 y | Fixed | Companies Act |
| Telemetry | `INTERNAL` | 30 d | Fixed | Operations |

Enforced by **S3 lifecycle rules and a scheduled deletion job driven by the
`Retention` annotation** — not by a hand-maintained list, which would drift within
a sprint.

Shorter retention is both cheaper and more compliant. That alignment is rare and
should be exploited.

---

## Erasure across five stores

```mermaid
flowchart LR
    REQ["erasure<br/>request"] --> F{"fan-out"}
    F --> A["<b>Aurora</b><br/>delete by subject key<br/><i>straightforward</i>"]
    F --> B["<b>S3</b><br/>delete by prefix<br/>+ lifecycle backstop"]
    F --> C["<b>Redis</b><br/>TTL-bounded<br/><i>automatic</i>"]
    F --> D["<b>Kafka</b><br/>compaction + tombstone<br/><b>identifiers only</b>"]
    F --> E["<b>Backups</b><br/>bounded retention<br/>erasure replayed on restore"]

    A & B & C & D & E --> V["verify all<br/>confirmed"]

    classDef hard fill:#8B2635,stroke:#5e1923,color:#fff
    class D,E hard
```

The two red boxes are where erasure programmes actually fail. **Kafka** cannot
delete a record, so the schema constraint above is the only mitigation.
**Backups** are routinely forgotten, and a deleted subject reappearing from a
restore is a compliance failure.

Erasure is a **rehearsed runbook tested per release**, not an assumption.

---

## Residency

```mermaid
flowchart TB
    subgraph IN["🇮🇳 default — all personal data"]
        AUR[("Aurora")]
        RD[("Redis")]
        MS[["Kafka"]]
        S3[("S3")]
        STT["Google STT 🇮🇳"]
    end

    GATE{"cross-border<br/>consent<br/>granted?"}

    subgraph OUT["🌐 narrow exception — ADR-0012 §5.4"]
        LLM["Claude API"]
        TTS["ElevenLabs · Cartesia"]
        DG["Deepgram"]
    end

    IN --> GATE
    GATE -->|"yes — audited,<br/>minimised, DPA"| OUT
    GATE -->|"no"| DEG["stay in-country<br/><i>degraded, not<br/>non-compliant</i>"]

    classDef ok fill:#1F7A3D,stroke:#145227,color:#fff
    classDef warn fill:#8B5A00,stroke:#5e3d00,color:#fff
    class IN,DEG ok
    class OUT warn
```

Four conditions, all required, for any cross-border call: subscriber consent, an
audit-trail entry naming the vendor and basis, a DPA prohibiting retention and
training, and a minimised payload.

Enforced in the **provider port**, not at the call site — a residency decision
made implicitly by a load balancer is a compliance failure.

**Eliminating this exception entirely** by promoting in-country ASR and TTS once
they reach quality parity is a tracked Phase-2 goal (ADR-0012 §14), not a
permanent accommodation.
