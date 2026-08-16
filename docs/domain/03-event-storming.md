# 3 · Event Storming Summary

What actually happens, in time order, across the whole platform — and what the
storming session revealed that the architecture had not yet named.

> **Notation.** `▣` domain event (orange) · `▢` command (blue) ·
> `◇` policy (lilac) · `▤` aggregate (yellow) · `☺` actor (small yellow) ·
> `⌘` external system (pink) · `❓` hotspot — a genuine disagreement or an
> unresolved question (red).

---

## 3.1 The big picture

Seven timelines. Everything in the platform is one of these, or supports one.

| # | Timeline | Trigger | Terminal event |
|---|---|---|---|
| **T1** | Subscriber onboards | App install | `identity.subscriber.activated` |
| **T2** | **A call is screened** | Inbound call, unanswered | `telephony.call.ended` |
| **T3** | Subscriber reviews and acts | Notification or app open | `consumer.call_action.recorded` |
| **T4** | Forwarding lapses and recovers | Health interrogation | `telephony.forwarding.verified` |
| **T5** | Money moves | Plan selection or renewal | `billing.invoice.issued` |
| **T6** | An organisation operates | Org creation | `business.routing.changed` |
| **T7** | An operator investigates | Support ticket or alert | `admin.access.released` |

**T2 is the product.** The other six exist to make it possible, payable,
lawful, or repairable.

---

## 3.2 T2 · A call is screened

The core timeline, at full fidelity. Every other timeline is summarised.

```
☺ Caller
  │
  ▢ dials the Subscriber's number
  │
  ⌘ Carrier ── handset rings 5 s ──────────────────────────── ADR-0002
  │
  ├── ☺ Subscriber answers ──▶ ▣ (nothing. We were never involved.)  ❓
  │
  └── not answered
        │
        ▤ ScreeningPreferences ── ◇ P-CO-2 pre-filter, ON DEVICE
        │
        ├─▣ consumer.pre_filter.decided (RING_THROUGH)  known contact
        ├─▣ consumer.pre_filter.decided (REJECT)        blocklisted
        ├─▣ consumer.pre_filter.decided (SILENCE)       blocklisted, silent
        │
        └─▣ consumer.pre_filter.decided (SCREEN)        unknown
              │
              ⌘ Carrier forwards to DID ────────────────────── CFNRy
              │
              ▤ DirectInwardDialNumber
              ▢ AdmitCall
              │
              ◇ P-TE-1 ── no valid Diversion? ──▶ ▣ telephony.call.refused_undiverted
              │                                    (hostile — I10)
              ◇ P-TE-2 ── capacity exhausted? ──▶ ▣ telephony.call.shed
              │                                    (rings through — I11)
              ▼
              ▣ telephony.call.received
              ▤ CallSession created
              │
              ▢ AnswerCall
              ▣ telephony.call.answered
              │
              ▢ PlayAnnouncement ────── ▤ Announcement (immutable VO)
              ▣ telephony.call.announcement_played  ◀── I1. THE LAWFUL BASIS.
              │                                        Never absent. Never a model.
              │
              ├─◇ P-VO-1 CALL_RECORDING consent granted?
              │      └─ yes ──▶ ▣ voice.recording.started
              │                  ▤ RecordingArtefact
              │
              ├──▶ ▣ notifications.notification.dispatched (screening_live)
              │     ☺ Subscriber is now watching
              │
              ▼
        ┌─────────── THE CONVERSATION LOOP ───────────────────────┐
        │                                                          │
        │  ⌘ ASR ──▶ ▣ voice.utterance.interim   (EPHEMERAL,      │
        │            │                            never persisted) │
        │            └─▣ voice.utterance.finalised                 │
        │                     │                                    │
        │                ▤ ScreeningConversation                   │
        │                ◇ P-AI-5 caller speech is UNTRUSTED (I4)  │
        │                     │                                    │
        │                ◇ TierRoutingService ── ADR-0006 ladder   │
        │                     ├─▣ ai.conversation.tier_escalated   │
        │                     │                                    │
        │                ◇ P-AI-2 thinking enabled (I3)            │
        │                ◇ SafetyLayerService ── never shed (I11)  │
        │                     │                                    │
        │                     ├─▣ ai.injection.detected      ❓     │
        │                     ├─▣ ai.safety.intervention           │
        │                     │                                    │
        │                ▣ ai.conversation.turn_completed           │
        │                     │                                    │
        │                     ├──▶ ▤ FraudAssessment (async)       │
        │                     │     ◇ P-FR-3 never shed (I11)      │
        │                     │     ▣ fraud.assessment.completed   │
        │                     │          │                         │
        │                     │          ◇ P-NO-6 confidence ≥ MED?│
        │                     │          └─▶ ▣ notifications...    │
        │                     │              (fraud_alert)         │
        │                     │                                    │
        │                     ├──▶ ◇ EmergencyDetection            │
        │                     │     ▣ ai.emergency.detected   ◀── U7│
        │                     │     ══▶ BREAKS THE LOOP            │
        │                     │                                    │
        │                ⌘ TTS ──▶ ▣ voice.speech.synthesised      │
        │                     │                                    │
        │                     └─◇ barge-in ≤ 20 ms ──▶ back to top │
        └──────────────────────────────────────────────────────────┘
                              │
        ┌─────────────────────┼─────────────────────┬──────────────┐
        ▼                     ▼                     ▼              ▼
  ☺ Subscriber          ☺ Subscriber          conversation    ☺ Caller
  ▢ EngageTakeover      ▢ DeclineCall          concludes      hangs up
        │                     │                     │              │
  ◇ P-TE-4 failure       ▣ telephony.call    ▣ ai.conversation  ▣ telephony
  NEVER ends the call      .declined            .ended           .call.ended
        │                     │                     │              │
  ▣ telephony.call            │              ◇ RoutingPolicy       │
    .taken_over               │              (business lines only) │
        │                     │              ▣ telephony.call      │
        │                     │                .transferred        │
        └─────────────────────┴─────────────────────┴──────────────┘
                              │
                              ▼
                    ▣ telephony.call.ended
                              │
              ┌───────────────┼────────────────┬─────────────────┐
              ▼               ▼                ▼                 ▼
     ▣ ai.transcript   ▣ ai.summary     ▣ voice.recording  ▣ billing.usage
       .finalised        .generated       .stopped           .recorded
              │               │                │                 │
              └───────────────┴────────────────┴─────────────────┘
                              │
                              ▼
                   ▤ CallHistoryEntry  (Consumer read model)
                   ▣ notifications... (screening_summary, silent)
                              │
                              ▼
                    ☺ Subscriber reviews ──▶ T3
```

### Hotspots found in T2

| ❓ | The disagreement | Resolution |
|---|---|---|
| **The subscriber answered** | Is a call the subscriber answered part of our domain at all? | **No.** We were never involved and have no record beyond the handset's own log. Modelling it would require reading the call log for no product benefit and real permission cost. `CallHistoryEntry` shows only calls we touched |
| **Injection detected** | Is a detected prompt injection a fraud signal, a safety event, or both? | **Both, separately.** `ai.injection.detected` is a safety event owned by AI Orchestration. Fraud may consume it as a `RiskSignal`, but the two have different consumers and different urgencies. Collapsing them would make a security event invisible in the safety metrics |
| **Emergency during fraud** | If emergency and fraud are both detected, which wins? | **Emergency, always.** It is a control-flow event; fraud is a display judgement. The pretext-call case — urgent *and* a scam — surfaces both facts, with emergency in control (`UX_FREEZE` / `A72`) |
| **Verdict after the call ended** | Does a late assessment update the record, or is it discarded? | **Updates.** `FraudAssessment` is asynchronous by design. The `CallHistoryEntry` shows a pending verdict as a skeleton, never as `UNKNOWN`, because pending and unassessable are different facts |
| **Who owns the 5-second ring?** | Telephony, or nobody? | **Nobody in our domain.** It is carrier behaviour we configure and do not control. Modelling it as a state would imply we can observe it. We observe only the forwarded call |

---

## 3.3 T1 · Subscriber onboards

```
☺ Prospect
  ▢ RequestVerification ──▶ ▤ VerificationChallenge ──▶ ⌘ SMS provider
  ▢ VerifyChallenge     ──▶ ▣ identity.verification.succeeded
                              │
                        ⌘ Play Integrity
                        ◇ attestation ──▶ ▣ identity.device.enrolled
                              │            ▤ Subscriber (PENDING → ACTIVE)
                              ▼
                        ▣ identity.subscriber.activated
                              │
  ▢ SelectSim (dual-SIM) ─────┤
  ▢ ProvisionForwarding ──────┤──▶ ▤ Line + ForwardingConfiguration
                              │    ⌘ Carrier (MMI, client-constructed — I10)
                              │
                        ▢ VerifyForwarding ──▶ ⌘ Carrier interrogation
                              │
                    ┌─────────┼──────────────┬──────────────┐
                    ▼         ▼              ▼              ▼
              ▣ forwarding  ▣ forwarding  ▣ forwarding  ▣ forwarding
                .verified     .lapsed      .wrong_target  .unverifiable  ❓
                    │                                          │
                    │                                    NOT a failure.
                    │                                    A distinct state.
                    ▼
  ▢ GrantConsent × N ──▶ ▤ ConsentRecord (one per purpose — U9)
                        ▣ identity.consent.granted
                              │
  ▢ UpdateAssistantProfile ──▶ ▤ AssistantProfile
                              │
  ▢ PlaceTestCall ──────▶ full T2 against the subscriber's own number
                              │
                        ▣ identity.subscriber.onboarded
```

### Hotspots found in T1

| ❓ | The disagreement | Resolution |
|---|---|---|
| **Unverifiable forwarding** | Is a carrier that cannot be interrogated a failure? | **No.** `UNVERIFIABLE` is a first-class state with its own copy and its own remedy (a test call). Collapsing it into `LAPSED` produces false alarms, which are as corrosive as missed ones |
| **Is the Announcement a consent?** | It is disclosed to the caller; does the subscriber consent to it? | **No.** It is the *caller's* lawful basis under ADR-0012 §5.1. Modelling it as a `ConsentRecord` would make it withdrawable, which would make screening unlawful. It is deliberately absent from `ConsentPurpose` (INV-ID-5) |
| **Who consents for the caller?** | The caller never agreed to anything | Correct, and that is why the Announcement exists. The lawful basis is disclosure, not consent. This is the single most important thing in the model to get right |
| **Onboarding abandoned midway** | Is a subscriber with no forwarding a subscriber? | **Yes**, and they have a working product — the on-device pre-filter. The escalation ladder means every step leaves something working behind it |

---

## 3.4 T3 · Subscriber reviews and acts

```
☺ Subscriber
  ▢ (opens app or taps notification)
       │
  ▤ CallHistoryEntry ── projection, NEVER a decision source (INV-CO-4)
       │
  ┌────┼──────────┬────────────┬──────────────┬──────────────┐
  ▼    ▼          ▼            ▼              ▼              ▼
 read  ▢ Block   ▢ Allow    ▢ Report    ▢ DisputeVerdict  ▢ Export
       │          │            │              │              │
  ▣ consumer.caller_list.entry_added         │        ▣ consumer
       │          │            │              │          .transcript
  ◇ P-CO-1 a number is in at most one list   │          .exported
       │          │            │              │
  ◇ P-CO-2 applied to the on-device          │
    pre-filter IMMEDIATELY, synced later     │
       │                       │              │
       │                 ▣ fraud.report      ▣ consumer.verdict.disputed
       │                   .submitted              │
       │                       │                   ◇ P-FR-4
       │                       ▼                   │  · prioritises the case
       │                 ▤ FraudCase                │  · records a precision signal
       │                                            │  · does NOT mutate the
       │                                            │    Assessment              ❓
       ▼
  ▣ consumer.call_action.recorded  ── append-only; undo appends a compensation
```

### Hotspots found in T3

| ❓ | The disagreement | Resolution |
|---|---|---|
| **Does a dispute change the verdict?** | The subscriber says we were wrong. Do we edit? | **No.** `FraudAssessment` is immutable (INV-FR-1). The *presentation* downgrades immediately in the subscriber's interface — their correction wins in their own record — and the assessment is superseded by a new version if review agrees. Editing history to match an opinion would destroy the evidence trail that makes the product credible |
| **Allowlisted number flagged as fraud** | Which wins? | **Both.** The allowlist wins for routing; the verdict is still recorded and shown (P-CO-3). The user's explicit instruction is obeyed; our opinion is still reported |
| **Blocking while offline** | Does a block work without a network? | **Yes.** The pre-filter is on-device (ADR-0002 §5). This is a genuine architectural guarantee and the product states it once, because users do not assume it |

---

## 3.5 T4 · Forwarding lapses and recovers

```
⌘ (SIM swap · ##002# · network event · carrier action)
       │
  ▤ ForwardingConfiguration ── silently no longer active           ❗
       │                        THE HIGHEST-SEVERITY SILENT FAILURE
       │                        IN THE PLATFORM (ADR-0002 §15)
  ◇ P-TE-3 continuous interrogation, not one-time
       │
  ▣ telephony.forwarding.lapsed
       │
  ┌────┴──────────────┬─────────────────┬────────────────┐
  ▼                   ▼                 ▼                ▼
 CLEARED         WRONG_TARGET       WRONG_SIM      UNVERIFIABLE
  │                   │                 │                │
  │            ❗ possible hostile      │          no alarm raised
  │              reconfiguration        │          (P-TE-7)
  └────────┬──────────┴─────────────────┘
           ▼
  ▣ notifications.notification.dispatched (forwarding_health, ongoing)
  ☺ Subscriber sees a persistent banner ── U2
           │
     ▢ ProvisionForwarding (again)
           │
     ▣ telephony.forwarding.verified ──▶ banner clears
```

**The insight this timeline produced:** forwarding health is not a setting, it
is an **aggregate with a lifecycle and a monitored state machine**. Every model
that treated it as a boolean field produced a product that silently stops
working and keeps billing.

---

## 3.6 T5 · Money moves

```
☺ Subscriber ▢ ChangePlan ──▶ ⌘ Payment provider (ACL — no instrument data)
                                    │
                              ▣ billing.payment.succeeded
                              ▤ Subscription
                              ▣ billing.subscription.activated
                              ▣ billing.entitlement.changed  ──▶ every context
                                    │
  ── during every call ──▶ ▣ billing.usage.recorded
                                    │
                              ◇ 80% ──▶ ▣ billing.quota.threshold_crossed
                              ◇ 100% ─▶ ▣ billing.quota.exceeded
                                          ◇ P-BI-3 degrade to ring-through,
                                            NEVER a dropped call
                                    │
                              ▣ billing.invoice.issued  (LEGAL_HOLD)

  payment fails ──▶ ▣ billing.payment.failed
                    ◇ P-BI-2 GRACE — screening continues              ❓
                    ▣ billing.subscription.lapsed (only after grace)
```

### Hotspot

| ❓ | The disagreement | Resolution |
|---|---|---|
| **Does a failed payment stop screening?** | Commercially tempting | **No, not during grace.** Cutting a subscriber's call screening over a failed card is disproportionate, and for a business it is an outage. And `RiskVerdictVisible` is an entitlement held by **every** plan including FREE, permanently (INV-BI-6) — charging someone to learn a call was a scam is not a business model |

---

## 3.7 T6 · An organisation operates

```
☺ Owner ▢ CreateOrganisation ──▶ ▤ Organisation
        ▢ AddBusinessLine ─────▶ ▤ BusinessLine ──┐
                                                   │ SAGA — spans two clusters
                                  ▤ Telephony Line ┘ (00 §0.7)
        ▢ InviteMember ────────▶ ▤ Invitation
                                  ◇ P-BU-3 bound to the invited MSISDN
                                    server-side; the token alone grants nothing
                                        │
☺ Member ▢ AcceptInvitation ──▶ consent screen, SAME SENTENCES as A56
                                  ▣ business.membership.accepted
                                        │
                                  ◇ VisibilityService
                                  ❗ PERSONAL LINES ARE NOT OBJECTS HERE
                                     (INV-BU-1 — an absence, not a check)
        ▢ ConfigureRouting ────▶ ▤ RoutingPolicy
                                  ◇ P-BU-4 unreachable rule rejected at save
                                  ◇ INV-BU-4 a terminal fallback always exists
```

### Hotspot

| ❓ | The disagreement | Resolution |
|---|---|---|
| **Can an admin see a member's personal calls?** | The question every member asks | **There is nothing to see.** Personal lines are not addressable objects in the Business context. This is deliberately an absence rather than a permission check, because a permission check can be misconfigured and an absence cannot |
| **Caller asks to be transferred** | Is that a routing instruction? | **No.** Caller speech is untrusted input (I4). Routing follows configuration and nothing else (P-BU-5) |

---

## 3.8 T7 · An operator investigates

```
☺ Operator ▢ LookupSubscriber ──▶ ▤ Subscriber (REDACTED BY DEFAULT)
                                    ◇ P-AD-1
                                        │
                        ┌───────────────┴────────────────┐
                        ▼                                ▼
              metadata answers it              content is needed
              ❗ THE PREFERRED OUTCOME                   │
              (measured: share of tickets      ▢ RequestAccess
               resolved WITHOUT break-glass)   ▤ AccessGrant
                                                ◇ reason ≥ 20 chars
                                                ◇ expiry ≤ 60 min
                                                ◇ approval if role requires
                                                    │
                                              ▣ admin.access.granted
                                              ▤ AuditEntry (per field revealed)
                                                    │
                                    ┌───────────────┼──────────────┐
                                    ▼               ▼              ▼
                            ▣ admin.access   ▣ admin.access  ▣ admin.access
                              .released        .expired        .revoked
                              (healthy)        (re-locks
                                                in place)
                                    │
                                    ▼
                      ☺ Subscriber may request this record ── DPDP
                      ❗ The strongest control on the surface is the
                         sentence telling the operator this is true
```

---

## 3.9 What the storming revealed

Findings that changed the model, not merely documented it.

| # | Finding | Consequence |
|---|---|---|
| **1** | **The announcement is not a message; it is a lawful basis.** Everyone in the room initially modelled it as configurable greeting text | Modelled as an **immutable value object** rather than an entity, so it cannot acquire a setter. Deliberately absent from `ConsentPurpose` (INV-ID-5) |
| **2** | **Forwarding health is an aggregate, not a field.** It has four distinct failure states, one of which is "we could not check" | `ForwardingConfiguration` gained a state machine and a verification history. `UNVERIFIABLE` is not `LAPSED` |
| **3** | **A verdict and its evidence are one thing.** Treating evidence as an optional detail let unevidenced verdicts exist | `FraudAssessment` cannot be constructed without an `EvidenceReference` or an explicit `UNKNOWN` reason (INV-FR-3) |
| **4** | **Interim ASR is a different concept from a turn**, not an early version of one | `Utterance` and `TranscriptTurn` are separate types. An interim `Utterance` can never become a `TranscriptTurn` (INV-VO-2) |
| **5** | **The two agents needed separating in the model, not just the interface.** They were one context and nearly one aggregate | Separate aggregates, separate tool sets, separate prompts, separate scopes (INV-AI-8) |
| **6** | **The pre-filter runs on the handset**, and that is domain-significant rather than an implementation detail | `PreFilterDecisionService` is the one domain service whose execution location is part of its specification |
| **7** | **A dispute is a signal, not an edit.** The instinct was to let the subscriber correct the verdict | Presentation downgrades immediately; the assessment is immutable and superseded only by review (INV-FR-1, P-FR-4) |
| **8** | **Business spans two persistence clusters**, so adding a line is a saga | Compensations specified in [16 §16.13](16-business.md) rather than discovered in production |
| **9** | **"We could not check" and "it is broken" are different claims**, and conflating them was about to ship | A distinct state, distinct copy, no alarm (P-TE-7) |
| **10** | **Analytics' most important domain service rejects things.** It was drafted as a rich reporting context | Reduced to a catalogue, a guard, and a suppressor. A rich analytics domain here would be a privacy failure (INV-AN-1) |
