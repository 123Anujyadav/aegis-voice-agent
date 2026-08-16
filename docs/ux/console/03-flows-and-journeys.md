# Console · 3 · Flows and Journeys

---

## 3.1 Flow index

| # | Flow | Role | Terminal states |
|---|---|---|---|
| K1 | Support ticket: "my calls aren't being screened" | `support` | Diagnosed · Escalated |
| K2 | Support ticket: "what did the assistant say to my caller?" | `support` | Answered under break-glass · Refused |
| K3 | Fraud case triage | `fraud_analyst` | Confirmed · False positive · Rule change · Escalated |
| K4 | Break-glass access | `support` `fraud_analyst` | Granted · Denied · Expired · Released |
| K5 | Incident response | `sre` | Mitigated · Resolved · Postmortem |
| K6 | Prompt change to production | `ai_engineer` | Rolled out · Rolled back · Blocked by eval |
| K7 | Feature flag change | `sre` `ai_engineer` | Applied · Reverted |
| K8 | Access review | `admin` `auditor` | Reviewed · Revocations issued |
| K9 | Subscriber data access request (DPDP) | `admin` | Fulfilled |

---

## K1 · "My calls aren't being screened"

The most common ticket in the product, by a wide margin (ADR-0002 §15 —
forwarding lapse likelihood **high**).

```
  Ticket arrives with the subscriber's number
        │
        ▼
  C20 Lookup by MSISDN  ──▶ no match ──▶ "no subscriber matches"
        │                                (no enumeration — same message
        │                                 whether or not the number exists)
        ▼
  C21 Subscriber detail (redacted)
        │
        ▼
  C23 Forwarding diagnostics
        │
        ├── interrogation history
        ├── expected vs observed DID
        ├── carrier + circle + matrix notes
        └── recent inbound attempts to the DID
        │
        ▼
  DIAGNOSIS — rendered as a sentence the agent can read aloud
        │
   ┌────┼─────────────┬──────────────┬──────────────┬─────────────┐
   ▼    ▼             ▼              ▼              ▼             ▼
 never  cleared    wrong SIM     wrong DID     carrier can't   platform
 set up (SIM swap,  (dual-SIM)   (unknown —     verify         side
   │    ##002#)         │         hostile?)         │              │
   ▼        ▼           ▼              ▼            ▼              ▼
 "they   "set it up  "switch      ESCALATE:    "we can't      C50 incident
 stopped  again in   the SIM in   toll-fraud   check on       — this is not
 at step  Settings → Settings →   runbook,     your carrier;  a support
 4"       Forwarding" Forwarding"  security     here's a test  problem
                                   channel      call"
   │        │           │              │            │              │
   └────────┴───────────┴──────────────┴────────────┴──────────────┘
                              │
                              ▼
                    Agent reads the script.
                    NOTHING IS CHANGED FROM THE CONSOLE.
                    Forwarding is the subscriber's carrier
                    configuration; only they can alter it.
```

**Target: under 60 seconds to diagnosis**, measured as
`console.support.forwarding_diagnosed.time_to_diagnosis_s`. This is the
single highest-leverage efficiency metric on the surface.

**Nothing here requires break-glass.** Forwarding state is operational metadata,
not call content, and designing the common case to need no privileged access is
what keeps break-glass rare enough to mean something.

---

## K2 · "What did the assistant say to my caller?"

```
  Ticket, with a session reference
        │
        ▼
  C20 → C21 → C12 Historical session detail
        │
        ▼
  Metadata is visible:
  turns, timings, tier, verdict, outcome
        │
   ┌────┴─────────────────────────────────┐
   │                                      │
 metadata answers it              content is needed
   │                                      │
   ▼                                      ▼
 Answer. No access request.         C22 Break-glass
 THIS IS THE PREFERRED OUTCOME            │
 and the screen is designed to      reason ≥ 20 chars
 make it possible often.            ticket reference
                                    duration 15/30/60 min
                                          │
                                    role needs approval?
                                    ┌─────┴─────┐
                                   yes          no
                                    │            │
                                    ▼            ▼
                              C91 approval   granted
                              by support_lead    │
                                    │            │
                                    └─────┬──────┘
                                          ▼
                              Transcript unlocks.
                              Persistent bar in the frame:
                              what is unlocked, time left.
                                          │
                              ┌───────────┼───────────┐
                              ▼           ▼           ▼
                          answered    released     expired
                          ticket      early        │
                              │       (healthy     ▼
                              │        signal)  content re-locks
                              │           │      in place, with a
                              └───────────┴──▶   re-request control
                                          │
                                          ▼
                              Audit entry: operator, reason,
                              ticket, every field revealed.
                              The subscriber can request this.
```

**The design goal is that most tickets are answered without break-glass.** That
is why `C12` shows turn counts, timings, tier escalation and verdict without
content — a surprising proportion of "what happened on my call" questions are
answered by "it escalated to a longer model at 19 seconds and flagged a
one-time-password request".

---

## K3 · Fraud case triage

```
  C30 Queue (sorted: disputed first, then confidence, then age)
        │
        ▼
  j/k to navigate · Enter to open        ◀── AF7: keyboard-only, no mouse
        │
        ▼
  C31 Case detail
        │
        ├── verdict + confidence + pattern
        ├── evidence turns (break-glass, reason = case reference)
        ├── model tier and routing decision
        ├── WHAT THE SUBSCRIBER DID       ◀── the most valuable field
        └── similar recent cases
        │
        ▼
  Resolve
        │
   ┌────┼──────────────┬─────────────────┬──────────────┐
   ▼    ▼              ▼                 ▼              ▼
 confirmed  false    needs rule       escalate      insufficient
   │        positive  change             │           data (past
   │          │          │               │           retention)
   ▼          ▼          ▼               ▼               ▼
 pattern   feeds     C32 rule       security      resolved on
 reinforced eval as  proposal →     channel       metadata, with
           a negative review                      the limitation
              │                                   recorded
              ▼
        Row collapses (short).
        Next case opens automatically
        ONLY in keyboard mode.
```

**Disputed cases are prioritised** above everything else in the queue. A
subscriber who pressed "It wasn't fraud" has given us the highest-quality
precision signal available, and it should be the first thing an analyst sees.

**Bulk resolution** exists only for cases sharing a pattern *and* a confidence
band, is capped, and writes one audit entry per case. Bulk-resolving mixed cases
is how a review queue becomes a rubber stamp.

---

## K4 · Break-glass access

Covered in `K2`. Stated separately because its **failure modes** are the
security-relevant part:

| Condition | Behaviour |
|---|---|
| Reason under 20 characters | Rejected inline. The friction is the feature |
| Grant expires mid-read | Content **re-locks in place**. Not a modal over still-visible content |
| Operator requests their own number | Permitted, **alerted on**, surfaced in access review |
| Repeated requests for one subscriber | Permitted, surfaced as a pattern in `K8` |
| Request fails for any reason | Grants nothing. Fails closed |
| P1 incident | `incident` justification self-approves and escalates review to immediate — but is still fully audited, and reviewed the same day rather than weekly |
| Operator releases early | Logged as a positive signal and reported in access review |

**The dialog tells the operator that the subscriber can request this record.**
That sentence does more behavioural work than every technical control on the
screen.

---

## K5 · Incident response

```
  Alert fires  (latency budget breach · carrier answer-rate drop ·
                DID pool exhaustion · forwarding verification rate ·
                admission shed · eval regression in production)
        │
        ▼
  C50 Incidents ──▶ C51 Detail
        │
        ├── symptoms, from C02's live metrics
        ├── affected components
        ├── linked sessions (redacted)
        └── WHAT CHANGED IN THE LAST HOUR      ◀── flags + prompts + deploys,
        │                                          answered before it is asked
        ▼
  C52 Runbook — EMBEDDED, not linked
        │
        ▼
  Execute steps, each recorded with who and when
        │
   ┌────┴──────────────┬─────────────────┐
   ▼                   ▼                 ▼
 mitigate by      roll back a       degrade a tier
 flag change      prompt            (Invariant I11:
   │                 │               NEVER skip fraud
   ▼                 ▼               scoring or safety)
 type the flag   C62 rollback           │
 key to confirm      │                  ▼
   │                 │            A77 banner appears
   └────────┬────────┴──────────────────┘   on subscriber
            ▼                                devices, honestly
      Resolve → postmortem                   naming the reduction
            │
            ▼
      Timeline is the postmortem's first draft,
      already attributed and timestamped.
```

**"What changed in the last hour" is on the incident screen by default.** It is
the first question in every incident, and a tool that makes a responder go
looking for it at 3 am has failed at its only job.

---

## K6 · Prompt change to production

```
  C60 Registry ──▶ C61 Detail
        │
        ▼
  Edit candidate version
        │
        ▼
  VALIDATION AT SAVE — not at review
        │
   ┌────┼────────────────────┬──────────────────────┐
   ▼    ▼                    ▼                      ▼
 removes/          disables thinking on a    exposes subscriber
 model-generates   tool-calling tier          PII to the caller-
 the announcement       │                     facing agent
   │                    │                          │
   ▼                    ▼                          ▼
 REJECTED           REJECTED                   REJECTED
 Invariant I1       Invariant I3               Invariant I4
        │
        ▼ (valid)
  C70 Run evals
        │
        ▼
  Six gates: accuracy · fraud recall · safety ·
             injection · latency · cost
        │
   ┌────┴──────────────────────┐
   ▼                           ▼
 all pass              safety or injection
   │                   regressed
   ▼                           │
 C62 Rollout                   ▼
 (eval run REQUIRED       ROLLOUT BLOCKED at any
  and attached)           percentage. No UI override.
   │                      Overriding requires a code
   ▼                      change and a review.
 5% → 25% → 50% → 100%
   │
   ├── automatic rollback triggers armed throughout
   │
   ▼
 Rolled out, attributed, versioned, auditable
```

**The invariants are enforced in the tool, not in review.** A reviewer who has
to remember Invariant I1 will eventually not. A save that rejects it will not.

---

## K7 · Feature flag change

```
  C80 List ──▶ C81 Detail
        │
        ├── owner · expiry · current rollout · last changed by
        │
        ▼
  Change ──▶ environment is prod? ──▶ type the flag key to confirm
        │                              (not a checkbox, not "are you sure")
        ▼
  Applied
        │
        ├── if an incident is open: automatically annotated onto its timeline
        └── audit entry with from/to
```

**Flags that would disable fraud scoring or the safety layer do not exist.**
Invariant I11 makes those unsheddable; representing them as toggles would be a
lie in the shape of a UI.

---

## K8 · Access review

Weekly, by `admin` and `auditor`.

```
  C92 Audit log, filtered to the review period
        │
        ├── every break-glass grant: operator, reason, ticket, revealed fields
        ├── self-lookups                       ◀── alerted, always reviewed
        ├── requests with no ticket reference
        ├── repeated access to one subscriber
        ├── exports and their row counts
        └── early releases                     ◀── recorded as a positive
        │
        ▼
  Outcomes: no action · coaching · role revocation · investigation
        │
        ▼
  Review itself is recorded. An unreviewed audit log is a log, not a control.
```

---

## K9 · Subscriber data access request (DPDP)

A subscriber may request the record of who accessed their data. This flow
fulfils it.

```
  Request arrives through the Grievance Officer
        │
        ▼
  C92 filtered to that subscriber (hashed)
        │
        ▼
  Every access: operator role (not name, unless legally required),
  reason category, resource, timestamp
        │
        ▼
  Export, itself audited
        │
        ▼
  Delivered through the Grievance Officer, within the statutory window
```

**This flow is the reason `C22`'s consequence sentence is true**, and the reason
it is the strongest control on that screen.

---

## 3.2 Operator journey

### First week

| Stage | Experience | Design response |
|---|---|---|
| Day 1 | Overwhelmed by nine sections | Sidebar shows **only** the sections their role can use. A support agent sees four |
| Day 2 | Learning the ticket flow | `K1` is designed as a linear path with a readable output. No interpretation required |
| Day 3 | First break-glass request | The dialog's consequence sentence lands hard, deliberately. Nobody forgets their first one |
| Day 5 | Discovers `⌘K` | Speed improves sharply. The palette is the single largest productivity feature on the surface |
| Week 2 | Keyboard triage | `j`/`k`/`Enter`/number-key resolution. Mouse use drops toward zero |

### The steady state

An experienced operator lives in three places: the command palette, one list,
and one detail. **They should almost never use the sidebar.** The sidebar is for
orientation and for new operators; the palette is for work.

### The failure we design against

> An operator, at speed, at the end of a shift, revealing more data than the
> ticket required — not maliciously, but because it was one click and the
> friction was gone.

Every control on this surface exists to make that specific failure hard: default
redaction, a typed reason, a time box, a persistent unlock bar, an audit entry
the subscriber can request, and a weekly review that a human actually reads.

**None of those are technical controls. All of them are interface decisions.**
That is why this surface is in a UX document rather than a security one.

---

## 3.3 Metrics

| Metric | Why it matters | Direction |
|---|---|---|
| Time to forwarding diagnosis (`K1`) | The most common ticket. Directly sets support cost | Down |
| **Share of tickets resolved without break-glass** | The privacy health of the surface | **Up** |
| Break-glass grants per operator per week | Outlier detection | Flat, low |
| Early releases as a share of grants | Operators respecting the boundary | Up |
| Fraud case time-to-resolve | Analyst throughput | Down |
| **Disputed cases resolved within 24 h** | Model quality feedback latency | Up |
| Rollouts blocked by eval | The gates are doing their job | Non-zero is healthy |
| Flags past expiry | Configuration debt | Down |
| Audit entries reviewed within the period | Whether the control is real | 100% |
| `console.access.denied_shown` per resource | A high rate means the role model is wrong, not the operators | Down |
