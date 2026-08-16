# 40 · Cross-Context Interaction Matrix

Who talks to whom, how, and what happens when the conversation fails.

---

## 40.1 The matrix

Rows **produce**; columns **consume**. Read as "row → column".

| Legend | |
|---|---|
| **E** | Asynchronous domain event (Kafka, via the transactional outbox) |
| **Q** | Synchronous query against the owner's published interface |
| **C** | Command issued to the owning context |
| **SK** | Shared-kernel value object, no runtime call |
| **·** | No relationship. **An empty cell is a design statement** |

| ↓ produces / consumes → | ID | CO | TE | VO | AI | FR | BU | BI | NO | AN | AD |
|---|---|---|---|---|---|---|---|---|---|---|---|
| **Identity** (ID) | — | E Q | E Q | Q | Q | · | E Q | E | E | · | Q |
| **Consumer** (CO) | · | — | E SK | · | E Q | E | · | · | · | E | · |
| **Telephony** (TE) | · | E | — | E C | E C | E | E | E | E | E | E |
| **Voice** (VO) | · | E | E | — | E | · | · | E | · | E | E |
| **AI Orchestration** (AI) | · | E | E C | C Q | — | E | · | E | E | E | E |
| **Fraud** (FR) | · | E | · | · | E | — | E | · | E | E | E |
| **Business** (BU) | · | E | E C | · | · | E | — | E | E | E | E |
| **Billing** (BI) | · | E | E | E | E | · | E | — | E | E | E |
| **Notifications** (NO) | E | · | · | · | · | · | · | · | — | E | E |
| **Analytics** (AN) | · | · | · | · | · | · | · | · | · | — | E |
| **Administration** (AD) | C Q | Q | C Q | Q | C Q | C Q | Q | C Q | Q | Q | — |

### What the empty cells say

| Empty pair | Statement |
|---|---|
| **AN → anything** | Analytics is a pure sink. It produces only to Administration, and never influences a domain decision. A metric must never become a control |
| **anything → ID** *(except NO)* | Nothing writes to Identity except Identity and Administration. Consent and identity are not side effects of other work |
| **CO → TE** *(as C)* | Consumer never commands Telephony. The pre-filter decision is a **shared-kernel value** the handset produces, not a remote call — that is what makes it free and offline-capable |
| **FR → TE** | Fraud never routes a call. It publishes a judgement; routing is Consumer's allowlist and Business's policy |
| **VO → FR** | Fraud consumes finalised turns from AI, never raw utterances from Voice. One supplier, one contract |
| **NO → most** | Notifications is a terminal consumer. It produces only delivery outcomes and token invalidation |
| **ID → AN** | **Separate Ways.** Analytics never learns who anyone is |

---

## 40.2 The hot path, in sequence

The interactions that must complete inside the ADR-0011 budget. Everything else
in the platform is off this path by design.

```
   Caller                                                      p50 budget
     │
     ▼
  ⌘ CARRIER ─── forwards ───────────────────────────────  25 ms
     │
     ▼
  TELEPHONY   AdmitCall
     │        ├─ DiversionValidationService  (I10)
     │        └─ CallAdmissionService        (I11)
     │
     ├──E──▶ CONSUMER   pre-filter already decided ON DEVICE (0 ms, free)
     │
     ▼
  TELEPHONY   AnswerCall ──────────────────────────────    15 ms
     │
     ├──C──▶ AI ORCHESTRATION  PlayAnnouncement
     │         └─ AnnouncementService — DETERMINISTIC, NO MODEL (I1)
     │
     ▼
  ╔══════════════ THE TURN LOOP ═══════════════════════╗
  ║                                                     ║
  ║  VOICE  ASR stream ────────────────────────  180 ms ║
  ║    │                                                ║
  ║    ├──E──▶ AI  voice.utterance.finalised            ║
  ║    │        (identifiers only — content fetched     ║
  ║    │         across the PARTNERSHIP boundary)       ║
  ║    ▼                                                ║
  ║  AI ORCHESTRATION                                   ║
  ║    ├─ PromptInjectionDefenceService  (I4)           ║
  ║    ├─ TierRoutingService             (ADR-0006)     ║
  ║    ├─ ToolAuthorisationService       (I4)           ║
  ║    ├─ model inference ───────────────────── 410 ms  ║
  ║    └─ SafetyLayerService  ── NEVER SHED   (I11)     ║
  ║    │                                                ║
  ║    ├──E──▶ FRAUD  turn_completed   ◀── ASYNC.       ║
  ║    │        RiskScoringService is OFF the hot path  ║
  ║    │        but is NEVER SKIPPED (I11)              ║
  ║    ▼                                                ║
  ║  VOICE  TTS synthesis ──────────────────────  190 ms║
  ║    │                                                ║
  ║    └─ barge-in preempts within ONE FRAME ── ≤ 20 ms ║
  ╚═════════════════════════════════════════════════════╝
                                              ─────────
                                    p50 total ≈  820 ms
                                    budget    ≤  900 ms
                                    p95 ceiling  1500 ms
```

**Nothing durable is on this path.** Aurora appears nowhere: live state is in
Redis, events publish through the outbox after the turn, and a producer that
blocks a turn on a broker acknowledgement is a bug (ADR-0009 §12).

---

## 40.3 Integration contracts

Every relationship, with its failure behaviour. **The failure column is the part
that matters** — an integration whose failure mode is undefined will define it
itself, in production, badly.

### Identity as Open Host Service

| Consumer | Needs | Mechanism | On failure |
|---|---|---|---|
| Consumer | Subscriber existence, consent state | Q + E | Cached consent; **unknown fails closed** (P-ID-3) |
| Telephony | Line ownership, subscriber status | Q + E | Cached; a suspended subscriber's calls ring through |
| Voice | `CALL_RECORDING` consent at artefact creation | Q | **No consent resolvable ⇒ no recording.** Fails closed |
| AI | Retention preference for the transcript | Q | Falls back to the statutory default, never to unlimited |
| Business | Subscriber resolution for membership | Q | Invitation acceptance blocks; nothing partial is created |
| Billing | Holder existence | E | Subscription creation defers |
| Notifications | Device tokens, revocation | E | Undelivered rather than misdelivered |
| Administration | Redacted subscriber lookup | Q | Support degrades to "cannot look up right now" |

### Billing as Open Host Service

`billing.entitlement.changed.v1` is the most widely consumed event in the
platform. Every context caches the entitlement set and re-resolves on the event.

| Failure | Behaviour |
|---|---|
| Entitlement unresolvable | **Fail closed on premium capability, open on `RiskVerdictVisible`** (INV-BI-6). Safety is never withheld because billing is unreachable |
| Event lost | The next event is a full set, not a delta, so a missed event self-heals |
| Cache stale | Bounded TTL; a stale entitlement grants nothing that was revoked more than the TTL ago |

### The two Partnerships

| Pair | Shared concern | Change discipline |
|---|---|---|
| **Telephony ↔ AI** | The turn contract, session lifetime, handover semantics | Neither changes turn boundaries, endpointing or barge-in semantics without the other. Both share the ADR-0011 budget |
| **Telephony ↔ Voice** | Media path, session lifetime, codec | ADR-0004 binds them. A media-transport change is a joint decision |

**A Partnership is a real commitment, not a label.** It means a pull request
touching the shared contract requires both CODEOWNERS, and it is why only two
exist.

### The anticorruption layers

| ACL | Guards against | Translation |
|---|---|---|
| **Carrier / CPaaS** (Telephony) | Exotel and Plivo modelling a call differently | Both to one `CallSession`. Provider-specific MMI acknowledgement formats to one `ForwardingVerdict` |
| **ASR / TTS** (Voice) | Three ASR and three TTS vendors, incompatible streaming | All to `Utterance` and `SynthesisRequest`. **Provider choice is partly a residency decision** (I2) |
| **LLM** (AI) | Provider message shape, tool protocol | To `ConversationTurn` and `ToolInvocation`. The tier ladder is ours |
| **Play Integrity** (Identity) | Verdict shape and versioning | To `IntegrityVerdict` |
| **Payment provider** (Billing) | Instrument data, provider error strings | To `PaymentMethodReference` and domain `PaymentFailureReason`. **No instrument data crosses** |
| **FCM** (Notifications) | Provider error codes | To `DeliveryOutcome`. **Payloads carry no content**, so the transport never receives personal data |
| **SSO** (Administration) | Corporate identity shape | To `Operator` |
| **Business registry** (Business) | Verification source | To `VerifiedBusinessIdentity`, naming its source |

---

## 40.4 Event flow — one screened call

Every event a single call produces, in order, with its consumers.

```
telephony.call.received ─────────────▶ CO  AI  AN
consumer.pre_filter.decided ─────────▶ TE  AN
telephony.call.answered ─────────────▶ AI  VO
voice.session.opened ────────────────▶ AI  AN
telephony.call.announcement_played ──▶ AI  AD  AN     ◀── I1 EVIDENCE
voice.recording.started (if consented)▶ CO  AD
notifications.notification.dispatched▶ AN              (screening_live)

    ┌── per turn ──────────────────────────────────────────┐
    │ voice.utterance.finalised ──────▶ AI                 │
    │ ai.conversation.turn_completed ─▶ FR  AN             │
    │ ai.conversation.tier_escalated ─▶ AN  BI             │
    │ fraud.assessment.completed ─────▶ CO  NO  AN         │
    └──────────────────────────────────────────────────────┘

telephony.call.taken_over ───────────▶ CO  NO  AN      (conditional)
ai.emergency.detected ───────────────▶ TE  NO  CO      (conditional, U7)
telephony.call.transferred ──────────▶ BU  AN          (business lines)
telephony.call.ended ────────────────▶ CO  AI  VO  FR  BI
ai.transcript.finalised ─────────────▶ CO  FR
ai.summary.generated ────────────────▶ CO
voice.recording.stopped ─────────────▶ CO  BI
billing.usage.recorded ──────────────▶ AN
notifications.notification.dispatched▶ AN              (screening_summary)
```

**Twenty-two events, and not one carries a phone number, a transcript turn, a
summary, or a caller's words.** That is Invariant I7 holding across the
platform's busiest workflow — and it is what makes a Kafka topic that can never
be deleted acceptable.

---

## 40.5 Consistency and compensation

| Interaction | Consistency | If the second step fails |
|---|---|---|
| Enrol device → revoke previous | **Transactional** (one aggregate) | — |
| Grant consent → capability enabled | Transactional | — |
| Add BusinessLine → provision Telephony Line | **Saga** across clusters | Compensations in [16 §16.13](16-business.md). A half-configured number is never live |
| Cancel plan → entitlement observed | Eventual, bounded by TTL | Fails closed on premium, open on `RiskVerdictVisible` |
| Withdraw consent → data deleted | Eventual | Capability stops **immediately**; deletion completes async and is confirmed with counts |
| Revoke device → sessions invalid | Eventual + revocation check | Access tokens are 15 minutes; the check makes it immediate in practice |
| Call ends → transcript finalised | Eventual | `CallHistoryEntry` shows the call with a pending transcript. Never empty, never missing |
| Assessment published → notification | Eventual | Suppressed past the staleness threshold rather than delivered as stale news |
| Member removed → routing target invalid | Eventual | Falls back, alerts, and **shows the broken target** (P-BU-7) |
| Usage recorded → invoice | Eventual, idempotent | Replay cannot double-bill (INV-BI-7) |
| Erasure requested → six stores cleared | **Saga**, never partial | `BLOCKED` is an open incident, not a background job that gave up |

---

## 40.6 Failure propagation

What each context does when a dependency is unavailable. **The direction of
every degradation is toward "the phone still works".**

| Unavailable | Consequence | Degradation |
|---|---|---|
| **Identity** | Consent unresolvable | Recording stops; screening continues on the announcement's lawful basis |
| **Telephony** | Calls not forwarded | **The carrier's forwarding fails and the call rings through.** The designed failure mode (ADR-0002 §6) |
| **Voice — ASR** | No transcript | Screening continues; transcript marked delayed; **takeover stays available** |
| **Voice — TTS** | Agent cannot speak | Session ends gracefully; the call rings through |
| **AI Orchestration** | No conversation | Call is not screened; rings through |
| **AI safety layer** | Cannot validate output | **Conversation ends.** There is no mode that proceeds unsafely (P-AI-12) |
| **Fraud** | No verdict | `UNKNOWN` with `SCORING_UNAVAILABLE`. **Never `SAFE`** (P-FR-10) |
| **Billing** | Entitlements unresolvable | Last-known set; fails closed on premium, open on `RiskVerdictVisible` |
| **Notifications** | No alerts | Screening unaffected. The subscriber finds out on next open |
| **Analytics** | No measurement | **Zero product impact.** By design |
| **Administration** | No operator access | Product unaffected; support degrades |
| **Redis** | Live session state lost | `DEGRADED`, not `UNHEALTHY`. Session-state loss ends the call **gracefully** rather than hanging (ADR-0009 §7) |
| **Kafka** | Events queue in the outbox | Nothing on the hot path blocks. Consumers catch up on replay |
| **Aurora** | Durable writes fail | Live calls continue; the record is written from the outbox on recovery |

**Not one row degrades toward a dropped call.** That is Invariant U1's domain
counterpart, and it is the property the whole architecture was chosen for.

---

## 40.7 Ownership map

| Context | Team | Services | Cluster |
|---|---|---|---|
| Identity | `callscreen/identity` | `identity`, `edge-api` | `identity` |
| Consumer | `callscreen/consumer` | `edge-api`, `contacts-sync`, Android | `identity` |
| Telephony | `callscreen/telephony` | `telephony-gateway`, `media-relay`, `session-orchestrator` | `telephony` |
| Voice | `callscreen/ai` | `asr-gateway`, `tts-gateway` | `content` + S3 |
| AI Orchestration | `callscreen/ai` | `ai-orchestrator`, `transcript-service`, `prompt-registry` | `content` |
| Fraud Detection | `callscreen/ai` | `fraud-engine` | `content` |
| Business | `callscreen/business` | `edge-api`, `identity`, `telephony-gateway` | `identity` + `telephony` |
| Billing | `callscreen/billing` | `billing` | `billing` |
| Notifications | `callscreen/platform` | `notification-fanout` | `identity` |
| Analytics | `callscreen/platform` | Ingestion pipeline | **None in production** |
| Administration | `callscreen/security` | Console backend, `edge-api` | `identity` + dedicated audit store |

**Three contexts share `callscreen/ai`** — Voice, AI Orchestration and Fraud.
They are separate contexts because their languages differ (`Utterance` vs
`ConversationTurn` vs `RiskSignal`) and their invariants differ, not because
different people own them. **A shared team is not a shared model.**

---

## 40.8 Invariant enforcement points

Where each frozen invariant is actually enforced, so a reviewer can check it
rather than trust it.

| Invariant | Enforced at | Mechanism |
|---|---|---|
| **I1** Announcement | `CallSession` state machine + `Announcement` VO | No `ANSWERED → SCREENING` edge exists; the VO is immutable |
| **I2** Residency | Every `PERSONAL` attribute + Voice provider routing | Platform egress policy from schema annotations |
| **I3** Thinking enabled | `PromptTemplate` save-time validation | Rejected at save |
| **I4** Untrusted caller speech | `ToolAuthorisationService`, `DisclosureScope`, `RoutingPolicy` | Refused at invocation; scope defaults empty |
| **I5** Device credential | `Device` construction | A transmitted credential is refused |
| **I6** Drain before terminate | `DrainService` | Readiness false, then close |
| **I7** Identifiers not content | Every event definition | No content field exists in any payload |
| **I8** Unclassified is PERSONAL | `ClassificationGuardService` | Reads the descriptor; fails closed |
| **I9** No audio to disk | Telephony media path; `RecordingConsentGate` | Persistence has one path, and it is consent-gated |
| **I10** Undiverted refused | `DiversionValidationService`; `MmiCommandFactory` | `CallSession` cannot be constructed without a diversion |
| **I11** Never skip fraud or safety | `CallAdmissionService`, `SafetyLayerService`, `FlagGuardService` | A flag disabling either cannot be created |
| **I12** Schema ownership | Separate schemas, separate credentials | Physical, not conventional |
| **U4** Provenance and confidence | `Provenance` VO; `FraudAssessment` construction | Mandatory fields |
| **U5** Recording never suppressed | `NotificationPreference` validation | No preference can reach it |
| **U7** Emergency handover | `ScreeningConversation` state machine | `EMERGENCY_HANDOVER` is terminal |
| **U8** No fake liveness | `AmplitudeFrame`; `ThinkingState` | No code path produces a frame without audio |
| **U9** Granular consent | `GrantConsent` accepts one purpose | No batch form exists |
| **U10** No caller string as chrome | `TemplateParameter`; `CallerIdentity` | No free-text field exists to put it in |
| **U11** No PII in analytics | `ClassificationGuardService` | Rejected at definition publication |
| **U12** Break-glass | `AccessGrant` construction; `RevealedField` | Reason floor, duration ceiling, per-field audit |

**The pattern worth noticing:** most of these are enforced by an **absence** —
a missing state-machine edge, a missing field, a missing command, a missing
constructor. Enforcement by absence cannot be forgotten, misconfigured, or
optimised away by someone who did not read this document.
