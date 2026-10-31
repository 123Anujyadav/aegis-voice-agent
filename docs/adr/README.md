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
| [0026](0026-distributed-tracing.md) | Distributed tracing | Accepted | **W3C Trace Context with a stdlib implementation of the existing `runtime.Tracer` port in `packages/go/telemetry`**, log exporter first; backend deferred to owner and SRE. Adapter implemented (#87); edge spans at `edge-api`'s and `identity`'s listeners, log exporter only; no backend yet |
| [0027](0027-kubernetes-manifest-tree.md) | One Kubernetes manifest tree | Accepted | **`infra/k8s` is the single manifest tree**, with the P1-37/P1-39 staging path amended to `infra/k8s/overlays/staging/`; Helm (A2), the kubeconform policy and A5 remain open |
| [0028](0028-tracing-backend-xray.md) | Tracing backend | Proposed (owner-selected, conditional) | **OpenTelemetry Collector to AWS X-Ray in `ap-south-1`**, over a VPC endpoint, IRSA for the Collector only; Accepted only when SRE records P1 (trace-id compatibility) and P2 (cost and budget) as met. Nothing provisioned |
| [0029](0029-access-token-and-proof-of-possession.md) | Access-token and proof-of-possession profile | Proposed (decided under owner delegation) | **EdDSA `at+jwt` access tokens, opaque rotating refresh tokens, RFC 9449 DPoP on every protected request**; stdlib fixed profile (no JWT library) with strict-JSON, RFC-vector and fuzz conditions; key-set rotation gates production issuance; DB TLS verify-full; amends ADR-0010 §5(c); implemented through #98–#105 (owner ratification, Play Integrity and an OTP provider remain open) |
| [0030](0030-identity-hmac-key-separation-and-rotation.md) | Identity HMAC keys | Proposed (decided under owner delegation; D30.3 needs the owner) | **One key per purpose (OTP, refresh, pseudonym, subscriber), keyrings with stored key ids, and stored random subscriber ids** so every identity HMAC key is rotatable; not implemented |
| [0031](0031-play-integrity-at-issuance.md) | Play Integrity at token issuance | Proposed (decided under owner delegation) | **Verdict bound to the request's DPoP proof (`requestHash = SHA-256(proof)`), `Integrity-Token` header, tier recorded on the session (`certified`/`restricted`/`unverified`)**; failed verdicts downgrade, rejected tokens refuse; recorded, not enforced until the owner sets the restricted tier's permissions; implemented (no Google verifier yet, so production sessions are `unverified`) |
| [0032](0032-additional-authentication-methods.md) | Additional authentication methods | Proposed: high-risk; awaiting independent human review | **Owner's direction: passwordless email code primary, Sign in with Google as an alternative, SMS deferred to phone-ownership verification**; would supersede ADR-0010 §5(a) and change INV-ID-1/2/7/8, so it needs the owner's decision plus an independent human review (none available); nothing accepted or implemented. Review packet: [reviews/0032](reviews/0032-architecture-review-packet.md) (AI-performed analysis) |

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

## Approval

The rules are `ARCHITECTURE_FREEZE.md` §6 and its governance amendment §6.1
(2026-10-31). This section says how they are applied and recorded.

### Who decides

| Role                                      | May                                                                                                                                                                                               | May not                                                                                                                                                                                                                                                                   |
| ----------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Repository owner**                      | Accept, reject, amend, supersede or deprecate an ADR; sign off an invariant; make the final classification of a decision as high-risk or not; decide owner-reserved questions; change these rules | Count their own decision as the independent human review                                                                                                                                                                                                                  |
| **Independent human reviewer**            | Review a high-risk ADR at a stated commit and record a verdict                                                                                                                                    | Be the repository owner, or anyone who drafted the decision; decide in the owner's place                                                                                                                                                                                  |
| **Claude Code, delegated Technical Lead** | Propose ADRs and their classification; make routine, reversible, in-scope engineering decisions clearly outside the high-risk definition; perform and record AI-performed reviews                 | Accept, reject, supersede or deprecate an ADR; sign off an invariant; make the final classification of a decision; make an owner-reserved decision, including legal, privacy and security-governance decisions; act as the independent human reviewer; change these rules |

- **An ADR Claude Code decides under delegation** must be clearly outside the
  high-risk definition. It is marked "Proposed: decided under owner
  delegation" until the owner ratifies it, and becomes a plain proposal if the
  owner classifies it as high-risk.
- **A high-risk ADR** Claude Code writes is plain "Proposed". It has no effect
  on implementation until it is accepted through this process.

### High-risk decisions

A decision is **high-risk** if it:

- changes, narrows or reinterprets a frozen invariant
  (`ARCHITECTURE_FREEZE.md` §3) or a domain invariant (`docs/domain/`);
- supersedes, amends or deprecates an Accepted ADR, in whole or in part;
- changes the authentication or authorisation model, a trust boundary, or how
  identities, sessions or keys work;
- changes personal-data collection, residency, retention or erasure;
- adds an external vendor with lock-in that processes personal data.

**When genuinely uncertain, it is high-risk.** The ADR's author proposes the
classification. The owner's classification is final, and the owner may
reclassify a decision at any time. A decision classified as not high-risk
records the reason in its approval record.

### What counts as an independent human review

- It is made by a **person**, never an AI system, who records their own
  verdict.
- That person is **not the repository owner** and **did not draft or
  co-draft** the decision.
- It reviews an **exact version**: the commit SHA named in the record.
- It is recorded **under the reviewer's own identity**: a GitHub review on the
  pull request, or a review record the reviewer wrote, linked from the ADR.
- It states a **verdict** (approve, approve with conditions, or reject) and
  any conditions.

### AI-performed reviews

- **Always labelled** "AI-performed review", with the tool, the date and the
  commit reviewed.
- **Useful evidence, never a substitute.** An AI-performed review never
  satisfies the independent human review, and is never an approval or a
  sign-off.

### When the required human review is unavailable

- The ADR **stays Proposed**. Its status may say "Proposed: awaiting
  independent human review".
- **Routine work that does not depend on the decision continues.** Work that
  depends on it waits.
- No ADR is accepted because a reviewer could not be found.

### The approval record

Every ADR accepted from 2026-10-31 records its approval in its own "Approval
record" section (see `0000-template.md`). For a high-risk ADR every row is
required:

| Field                    | What it records                                                                                                                                           |
| ------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Decision                 | Accept, amend or reject, and the exact scope accepted                                                                                                     |
| Classification           | High-risk or not, and why                                                                                                                                 |
| Version                  | The commit SHA, and the ADR file's SHA-256, that were decided on                                                                                          |
| Review trigger           | For a superseding ADR: the trigger that fired, and its observable evidence (`ARCHITECTURE_FREEZE.md` §6, rule 2)                                          |
| Owner decision           | The date, how it was given, the pull request that records it, and any conditions                                                                          |
| Independent human review | The reviewer's name and GitHub account, a statement of independence, the date, the commit reviewed, the verdict, the conditions, and a link to the review |
| Invariant sign-offs      | For each invariant: the current wording, the new wording, who signed off, and the date                                                                    |
| Other approvals          | Privacy and legal, Security or SRE, where required: who, when, and a link                                                                                 |
| AI involvement           | What was drafted or reviewed by AI, labelled as AI-performed                                                                                              |
| Does not                 | What the acceptance does not authorise                                                                                                                    |
| Supersedes               | The ADR and section superseded; that ADR then gets a "Superseded by" note                                                                                 |

Deprecating an ADR adds a dated note to it carrying the same fields; its
original text is not changed.

### Reading historical records

Interpretations recorded here; the historical files are **not** edited
(`ARCHITECTURE_FREEZE.md` §6.1, rule 11):

- **"Lead Staff Engineer"**, the decider in ADR-0001 to ADR-0012, refers to the
  repository owner, who confirmed this on 2026-10-31.
- **A review a record calls "independent"** without naming a human reviewer is
  read as an AI-performed review.
- **ADRs already marked Accepted keep that status.** These rules apply to
  decisions taken from 2026-10-31 and are not applied retroactively.
- **ADR-0029, ADR-0030 and ADR-0031 keep their status**, "Proposed: decided
  under owner delegation", and the effect on implementation they state. Their
  future ratification follows this process if the owner classifies it as
  high-risk. Each changes how authentication, sessions or keys work, so that
  classification is expected.

## Format

Every ADR from 0002 onward uses the 17-section structure: Context · Problem
Statement · Constraints · Considered Options · Decision · Why This Option Was
Selected · Trade-offs · Alternatives Rejected · Operational Impact · Security
Impact · Cost Impact · Performance Impact · Scalability Impact · Migration
Strategy · Risks · Future Review Trigger · References.

Every ADR states its **future review trigger** as an observable condition. "When
we grow" is not a trigger; "when p95 exceeds 1 500 ms for 7 days" is.
