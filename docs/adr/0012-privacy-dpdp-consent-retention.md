# ADR-0012: Privacy — DPDP compliance, consent, recording, retention, PII and residency

- **Status:** Accepted
- **Date:** 2026-08-06
- **Deciders:** Lead Staff Engineer
- **Consulted:** Legal, Security
- **Informed:** All engineering, Product
- **Depends on:** ADR-0002, ADR-0005, ADR-0007, ADR-0008, ADR-0009, ADR-0010

---

## 1. Context

This product answers people's phone calls and records what was said. It processes
the personal data of two distinct groups:

- **The subscriber**, who consented to the service and is our customer.
- **The caller**, who did not consent to anything, is often not a customer, and
  in many cases does not know an AI answered.

That asymmetry is the defining privacy problem here, and it is why this cannot be
a consent checkbox at signup. India's **Digital Personal Data Protection Act
2023** applies to both parties, and the caller's data is arguably the more
sensitive: we hold a recording of them speaking, their number, and an AI's
judgement about whether they were attempting fraud.

Phase 1 already committed the architecture to schema-level data classification —
`contracts/proto/callscreen/common/v1/annotations.proto` — precisely so that
privacy would be a property of the system rather than a discipline engineers must
remember. This ADR is the policy that annotation enforces.

## 2. Problem Statement

Four questions, each with a wrong answer that is easy to ship:

1. **Lawful basis.** On what basis do we process the *caller's* data, when the
   caller is not our customer and has not agreed to our terms?
2. **Recording.** May we record the call, and what must the caller be told?
3. **Retention.** How long may we keep audio, transcripts, and fraud verdicts —
   and how do we actually delete them, across five stores including Kafka
   (ADR-0009 §10)?
4. **Residency.** Personal data stays in India (ADR-0008 C1) — but ADR-0005 and
   ADR-0007 both contemplate cross-border vendor calls. How is that reconciled?

## 3. Constraints

| # | Constraint | Source |
|---|---|---|
| C1 | DPDP Act 2023 obligations: notice, consent, purpose limitation, erasure, grievance redressal | Statute |
| C2 | Consent must be free, specific, informed, unconditional and unambiguous, by clear affirmative action | DPDP §6 |
| C3 | The caller is a Data Principal whose data we process without a prior relationship | §1 |
| C4 | Personal data at rest and in normal processing stays in India | ADR-0008 C1 |
| C5 | Erasure must be executable across Aurora, Redis, S3, Kafka and backups | ADR-0009 §10 |
| C6 | TRAI TCCCPR 2018 governs commercial calling and DND; we must not become an offender | Statute |
| C7 | Children's data requires verifiable parental consent and prohibits tracking | DPDP §9 |
| C8 | Data-breach notification to the Data Protection Board carries a statutory clock | DPDP §8(6) |

## 4. Considered Options

**Lawful basis for caller data:**
1. Rely on the subscriber's consent as agent for the caller
2. Legitimate-use / implied consent from the act of calling
3. Explicit caller notice at call start, with recording as a separate opt-in
4. Do not process caller data at all — decline to record, transcript only

**Retention:**
5. Retain everything indefinitely for model improvement
6. Fixed short retention with no user control
7. Tiered retention by data class, user-configurable within bounds

## 5. Decision

**Option 3 for lawful basis. Option 7 for retention. Residency is default-India
with a narrow, consent-gated, logged exception.**

### 5.1 Notice and consent

**Caller.** Every screened call opens with a **Tier 0 announcement** (ADR-0006 §5)
identifying the agent as an AI, stating that the call is being screened on the
subscriber's behalf, and stating that it is being recorded. It is generated
without a model so it cannot vary, be suppressed by prompt injection, or be
altered by a model update.

The announcement is the notice. **Continuing the call is the caller's affirmative
action.** A caller who hangs up during the announcement has their audio and
number discarded within the ephemeral window and no durable record is created.

**Subscriber.** Layered consent at onboarding, granular and independently
revocable:

| Consent | Default | Governs |
|---|---|---|
| Core screening | Required | Call metadata, screening outcome |
| Transcript retention | Opt-in | Storing the text of screened calls |
| Audio retention | **Opt-in, off** | Storing call recordings |
| Cross-border AI processing | **Opt-in, off** | Routing to a non-India vendor (§5.4) |
| Product improvement | **Opt-in, off** | Any use of content beyond serving the call |

Consent records live in `identity` (ADR-0009), are versioned, timestamped, and
carry the notice text shown at the time. Revocation takes effect on the next call
and triggers erasure of data held solely on that basis.

### 5.2 Recording

**Audio recording is off by default and requires both** the subscriber's opt-in
**and** the caller having heard the announcement. Either absent, no audio is
persisted — `media-relay` never writes audio to disk regardless (ADR-0004 §10);
persistence is an explicit, policy-gated action by `transcript-service`.

Transcripts are treated as a separate and lesser disclosure than audio: text, no
voice biometric, and subject to a shorter default retention.

### 5.3 Retention schedule

| Data | Class | Default | User-configurable range | Basis |
|---|---|---|---:|---|
| Call audio | `SENSITIVE` | **30 days** | 7–90 days | Consent |
| Transcript | `SENSITIVE` | **90 days** | 7–180 days | Consent |
| Fraud verdict + features | `PERSONAL` | **90 days** | Fixed | Legitimate use — fraud prevention |
| Call metadata (number, time, outcome) | `PERSONAL` | **12 months** | Fixed | Service provision |
| Consent records | `PERSONAL` | **Life of account + 3 years** | Fixed | Legal obligation (C1) |
| Audit logs (auth, access, erasure) | `PERSONAL` | **`LEGAL_HOLD`** | Fixed | Security, ADR-0010 §10 |
| Billing records | `PERSONAL` | **8 years** | Fixed | Companies Act / IT Act |
| Telemetry, traces | `INTERNAL` | **30 days** | Fixed | Operations |

Retention is enforced by **S3 lifecycle rules and a scheduled deletion job**,
driven by the `Retention` annotation on the schema, not by a manually-maintained
list.

### 5.4 Residency

**Default: all personal data is processed and stored in India** (C4). Aurora,
Redis, MSK and S3 are region-locked in `ap-south-1` with `ap-south-2` for DR
(ADR-0008), enforced technically by service control policies, not by convention.

**The narrow exception.** ADR-0005 permits Deepgram (no Indian region) as ASR
secondary, and ADR-0007's default TTS providers process outside India. Each
cross-border call is permitted **only** when all four hold:

1. The subscriber has granted the cross-border consent in §5.1;
2. The routing decision is recorded in the audit trail with the vendor and the
   basis;
3. The vendor is under a DPA prohibiting retention and training on our data;
4. The transferred payload is minimised — for TTS, the agent's own generated text
   rather than caller audio.

**Without that consent, the pipeline stays in India** and degrades to the
in-country provider. Residency is a routing input, never a load-balancing
side effect.

### 5.5 Rights and governance

- **Access, correction, erasure, and grievance redressal** exposed in-app with a
  documented SLA, per DPDP.
- **A named Grievance Officer** and published contact, per DPDP.
- **A DPIA** completed before launch and re-run on any change to the data map.
- **The data map is generated** from schema annotations (`docs/compliance/`), not
  hand-maintained — a hand-maintained map drifts within one sprint.

## 6. Why This Option Was Selected

**Because the caller's position is the hard part, and Option 3 is the only answer
that treats them as a Data Principal rather than as an object of the
subscriber's consent.**

- **Option 1 fails on C3.** A subscriber cannot consent on a stranger's behalf.
  DPDP consent must be given by the Data Principal, and the caller is one. Relying
  on the subscriber's agreement to process the caller's voice is the kind of
  argument that survives until the first complaint.
- **Option 2 is weaker than it looks.** "You called them, so you consented" may
  support processing the *number*; it does not obviously support recording the
  *voice* and running fraud inference on it. Building on an implied basis for the
  most sensitive artefact is a poor foundation.
- **Option 3 is honest and it is also better product.** Telling the caller they
  have reached a screening assistant sets the interaction up correctly. Callers
  behave more cooperatively when they know what they are talking to, and
  fraudsters frequently hang up at the announcement — which is a **feature**: a
  call that ends during the announcement is the cheapest possible outcome across
  telephony, inference and storage simultaneously.
- **Tier 0 delivery is what makes it trustworthy.** The announcement is
  deterministic. It cannot be suppressed by prompt injection (ADR-0006 §10),
  cannot drift with a model update, and cannot be A/B tested away.

**Option 7 for retention** because Options 5 and 6 both fail C1 in opposite
directions — indefinite retention breaches purpose limitation and storage
limitation; a fixed short window with no user control fails subscribers who
genuinely want their call history. Tiering by class with bounded user control
satisfies both.

**Default-India-with-a-narrow-exception** rather than blanket prohibition,
because a blanket prohibition would force ADR-0007's default TTS choice to a
slower provider and breach the latency budget (ADR-0011). The exception is
explicit, consented, logged, and minimised — and §16 makes eliminating it a
tracked goal rather than a permanent accommodation.

## 7. Trade-offs

**Accepted.**

- **The announcement costs seconds on every screened call** — telephony billing,
  and caller patience, on a call that has not yet started. It is the price of the
  lawful basis and it is not negotiable.
- **Audio off by default reduces product value.** Many subscribers would want
  recordings and will have to find the setting. Correct default nonetheless:
  DPDP's data-minimisation posture, and the caller's interest, both point the
  same way.
- **Bounded user-configurable retention adds implementation complexity** —
  per-subscriber lifecycle rather than one global rule — for a genuine autonomy
  benefit.
- **The cross-border exception is a permanent compliance surface.** It must be
  argued in the DPIA, defended in an audit, and maintained in the data map.
  Genuinely eliminating it (§16) is worth real engineering effort.
- **Erasure across Kafka constrains event schema design forever** (ADR-0009 §10).
  Events carry identifiers, not content. This is restrictive and it is the only
  way erasure works.

## 8. Alternatives Rejected

**Option 1 — subscriber consents for the caller.** Rejected on C3, as above.

**Option 2 — implied consent from calling.** Rejected as an insufficient basis
for voice recording and inference. Retained as a *supporting* argument for
processing the number and call metadata, not as the primary basis.

**Option 4 — no caller data at all.** The maximally private design: screen without
recording, discard everything. Rejected because it removes the product's core
value — the subscriber cannot see who called and why, which is the reason they
subscribed. Preserved as the **default for audio** specifically (§5.2), which is
where the sensitivity is concentrated.

**Option 5 — indefinite retention for model improvement.** Rejected on purpose
limitation and storage limitation (C1). Also rejected on principle: training on
callers' voices without their meaningful consent is not something we will do,
which is why product-improvement consent is separate, opt-in, and off.

**Option 6 — fixed short retention, no user control.** Rejected as
paternalistic and as a product failure — a subscriber who wants 90 days of call
history has a legitimate interest we should serve within bounds.

## 9. Operational Impact

- **Erasure is a rehearsed runbook across five stores** (C5) — Aurora, Redis
  (TTL-bounded, automatic), S3, Kafka (compaction + tombstones), and backups.
  It is tested per release, not assumed. Backups are explicitly in scope: a
  deleted subject reappearing from a restore is a compliance failure, not a
  curiosity.
- **The retention job is a monitored, alerting production service.** A silently
  failed deletion job is an accumulating breach.
- **The data map is generated in CI** from schema annotations and diffed. A PR
  adding a `SENSITIVE` field without classification fails the gate.
- **Breach notification has a statutory clock that starts on awareness, not on
  understanding** (C8). The runbook must be executable before the investigation
  concludes — report early, revise later. `SECURITY.md` already states this for
  privacy incidents.
- **Consent state is queried on the call path** and cached; a revocation must
  propagate within one call, which makes it an event on the Kafka backbone rather
  than a periodic refresh.
- **DND / TCCCPR (C6).** We receive calls rather than placing them, so the
  regulation largely does not bind us — but the notification path (`notification-
  fanout`) must not become an unregistered commercial-messaging channel.

## 10. Security Impact

Privacy and security are the same programme here, and Phase 1's schema-level
classification is the mechanism for both:

- **Classification drives enforcement automatically.** The `Sensitivity`,
  `RedactionStrategy`, `Retention` and `residency_bound` annotations on every
  field drive log redaction (`packages/*/platform`), retention jobs, egress
  policy, and analytics exclusion. **An unclassified field fails closed** — it is
  treated as `PERSONAL`. That default is the single most important line in the
  annotations file.
- **Log redaction is enforced in the handler, not at the call site** (ADR-0005
  §10, and the Phase 2 implementation). There is no code path from a log call to
  a sink that bypasses it.
- **The transcript is the highest-value target in the system** — searchable,
  attributable, and containing whatever the caller said. It is `SENSITIVE`,
  encrypted with a CMK, access-audited individually, and excluded from analytics
  export by policy.
- **Fraud verdicts are personal data about the caller** and are subject to the
  same rights. A caller may request access to a judgement made about them.
- **Children's data (C7).** We cannot reliably determine a caller's age. Where a
  subscriber is known to be a minor, tracking and behavioural processing are
  disabled entirely and consent requires the verifiable-parental-consent path.
- **Cross-border routing is an audited security decision** (§5.4), not a
  performance optimisation, and is logged as one.

## 11. Cost Impact

- **Retention length is directly a storage cost**, and the schedule in §5.3 is
  therefore both a compliance control and the primary S3 cost lever (ADR-0009
  §11). Shorter retention is cheaper *and* more compliant — a rare alignment worth
  exploiting.
- **Audio off by default is a substantial storage saving**, since audio dominates
  transcript volume by orders of magnitude.
- **The announcement costs telephony seconds on every screened call** (§7) — a
  real, recurring, non-negotiable line item.
- **Callers who hang up during the announcement save the entire cost of the
  call** across telephony, ASR, LLM and TTS. This measurably offsets the
  announcement's cost and should be tracked as such.
- **Erasure and retention jobs are ongoing compute**, modest but permanent.
- **The DPIA, Grievance Officer function, and audit readiness are real operating
  costs** that a privacy-by-design architecture reduces but does not remove.

## 12. Performance Impact

- **Consent evaluation is on the call path** and must be a cached lookup, not a
  database round trip. It sits within the orchestration allocation in ADR-0011
  §5.2 hop 5.
- **The announcement is Tier 0** (ADR-0006 §5) — no model, no inference latency,
  and it usefully covers pipeline warm-up (ADR-0011 §12).
- **Log redaction runs on every record.** The implementation must be efficient
  enough to be invisible; the allow-list-before-deny-list ordering in the Phase 2
  platform packages exists partly for this reason.
- **Residency routing is a policy lookup**, not a network probe, and adds nothing
  measurable.

## 13. Scalability Impact

- **Erasure must scale with subject count, not with data volume.** A per-subject
  erasure that scans every partition does not survive growth. Subject-keyed
  indexes and partitioning by subject where practical are a design requirement,
  not an optimisation.
- **Retention jobs must be incremental and resumable.** A nightly full scan of
  transcript storage stops working at volume.
- **Kafka tombstones and compaction have a throughput cost** that grows with
  erasure volume and must be sized for (ADR-0009 §13).
- **Consent-state propagation is an event fan-out** and scales with subscriber
  count, not call volume — comfortable.
- **The generated data map scales with schema size**, which is bounded.

## 14. Migration Strategy

1. **Phase 1 (launch).** Policy as decided. DPIA completed, Grievance Officer
   appointed, data map generated, erasure runbook tested — **all four are launch
   blockers**, not follow-ups.
2. **Phase 2.** Eliminate the cross-border exception (§5.4) by promoting the
   in-country ASR and TTS options (ADR-0005 §14, ADR-0007 §14). This is the
   single highest-value compliance improvement available and is tracked as a goal
   rather than accepted as permanent.
3. **Phase 3.** Regulatory readiness for Significant Data Fiduciary obligations
   should the Board designate us — independent audit, a DPO resident in India,
   and periodic DPIA. Scale-triggered (§16).
4. **International expansion** does not extend this policy. A new jurisdiction
   gets its own residency boundary (`prod-global`, ADR-0008 §13) and its own
   analysis; GDPR and DPDP are similar in shape and different in detail.
5. **Consent-schema changes are versioned.** Existing consents remain valid under
   the notice text they were given with; a materially changed purpose requires
   fresh consent rather than a silent re-interpretation.

## 15. Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Caller's lawful basis challenged | Medium | Critical | Explicit Tier-0 announcement; recording opt-in; DPIA; legal review before launch |
| Erasure misses a store or a backup | Medium | Critical | Five-store runbook tested per release; generated data map; backups in scope with bounded retention |
| Personal data written into a Kafka payload | High | Critical | Identifiers-not-content rule (ADR-0009 §10); PII annotations reviewed by CODEOWNERS |
| Cross-border transfer without valid consent | Low | Critical | Consent gates routing in the provider port, not at the call site; every decision audited |
| Announcement suppressed by prompt injection | Low | Critical | Announcement is Tier 0 — deterministic, no model in the path |
| Retention job fails silently | Medium | High | Job is monitored and alerting; accumulating-volume anomaly detection |
| Unclassified field ships and is treated as public | Medium | High | Fails closed to `PERSONAL`; CI gate on new fields; CODEOWNERS on `contracts/common` |
| Breach-notification clock missed | Low | Critical | Runbook executable before investigation completes; report-early policy in `SECURITY.md` |
| Minor's data processed without parental consent | Low | Critical | Behavioural processing disabled for known-minor subscribers; verifiable-consent path |
| Subject-access or erasure request cannot be met in time | Medium | High | Subject-keyed indexes; erasure SLA monitored; rehearsed quarterly |

## 16. Future Review Trigger

Revisit when **any** holds:

- **DPDP Rules are notified** or amended, or the Data Protection Board issues
  guidance affecting voice processing — this is the highest-likelihood trigger
- In-country ASR **and** TTS both reach quality parity, allowing the cross-border
  exception in §5.4 to be **removed entirely** (Phase 2 goal, §14)
- Subscriber count or data volume approaches **Significant Data Fiduciary**
  designation thresholds
- Any erasure request cannot be completed within the statutory window using the
  current runbook
- Expansion to a jurisdiction with a different privacy regime
- A privacy incident of any severity — the ADR is reviewed as part of the
  post-incident process, not only the code

## 17. References

- Digital Personal Data Protection Act 2023 (India); TRAI TCCCPR 2018
- ADR-0002 (what we process), ADR-0005 / ADR-0007 (cross-border vendors),
  ADR-0008 (residency enforcement), ADR-0009 (erasure across stores),
  ADR-0010 (consent binding to identity), ADR-0011 (announcement latency)
- `contracts/proto/callscreen/common/v1/annotations.proto` — the enforcement
  mechanism for this entire policy
- `docs/compliance/` — data map, DPIA, retention schedule, erasure runbook
- `SECURITY.md` — incident reporting and secret handling
