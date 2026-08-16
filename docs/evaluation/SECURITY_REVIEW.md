# Security Review — Phase 10F

**Scope:** `packages/go/evaluation`, `packages/go/evalsubjects`

---

## 1. What this component is, from a security point of view

An evaluation platform is not a request path. It has no external listener, no
authentication, no user input and no network. Its threat model is therefore
unusual, and getting it right means naming what it *actually* is:

**It is a system that decides whether other systems may ship, and it stores
recordings of what those systems did.**

That produces exactly two categories of risk:

1. **Integrity of judgement** — anything that makes the platform say "green"
   when it should say "red". This is the dangerous category, because the
   platform's output is trusted by definition.
2. **Confidentiality of recordings** — observations and goldens are durable
   records of subsystem behaviour, and the subsystems being recorded handle
   personal data.

Traditional application threats (injection, authn/authz bypass, SSRF, XSS) do
not apply: there is no interface through which an attacker reaches this code.
The realistic adversary is **a well-meaning engineer under deadline pressure**,
and the design's job is to make the wrong thing hard rather than to detect an
intruder.

---

## 2. Integrity of judgement

### C1 — A golden cannot be approved by accident

`GoldenStore.Record` produces a **candidate**, never a baseline. Promotion
requires `Approve(id, by, reason)`, and both `by` and `reason` are refused if
empty.

This is the single most important control in the phase. A platform that updates
its own baseline when it sees a change reports no drift, ever — a silent
regression with a green dashboard. The auto-record path
(`AutoRecordCandidates`) files candidates and **never approves**; the run still
reports `NoBaseline` or `Drift`.

`RecordAndApprove` exists for first adoption and is deliberately named so it
cannot be reached by accident. It still demands an author and a reason: the
convenience is skipping a round trip, not skipping the review.

### C2 — Approval history is immutable and attributed

Every approval records `ApprovedBy`, `ApprovedAt`, `Reason` and `Supersedes`.
Superseded goldens are retained, so "what did we consider correct in March, and
who changed it" is answerable.

ENGINEERING_AUDIT F3 was a defect in exactly this control: approving a
replacement silently discarded the record of what it replaced. Fixed.

### C3 — A verdict cannot be influenced by the subject

`Compare` is a **pure function** of `(Golden, Observation, Tolerances)`. It
holds no state, consults no registry, and calls nothing. A subject cannot reach
it, and cannot influence its own verdict beyond producing the observation that
gets compared.

### C4 — The platform cannot be tuned into silence per subsystem

The core imports nothing it evaluates, so there is no way to write
`if subject == "governance" { relax }`. The boundary is checkable:

```console
$ go list -deps ./... | grep callscreen
.../packages/go/runtime
.../packages/go/evaluation
```

A special case per subsystem is not discouraged here; it is unavailable.

### C5 — Losing coverage is reported

A scenario that stops being evaluated — retired golden, wiped store, version
bump without re-approval — is now reported as a `coverage` regression rather
than passing silently. This closes the most attractive route to a green gate:
not making a scenario pass, but making it stop asking. See ENGINEERING_AUDIT F6.

### C6 — Verdicts do not move with machine load

Latency ratios require a measurable denominator, so a busy machine cannot
manufacture drift and — more importantly — cannot be blamed for real drift. See
ENGINEERING_AUDIT F5.

### C7 — Determinism is checked, not assumed

If a subsystem does not reproduce, drift is meaningless and every golden is a
coin flip. `PlatformReadiness.Ready` is false when any subject fails its
determinism check, independently of whether anything blocked.

### C8 — Unevaluated subsystems are named

`PlatformReadiness.UnevaluatedSubjects` lists registered subjects with **zero
scenarios**. A subsystem with no scenarios scores nothing, appears in no
scorecard, and would otherwise be invisible in a report that looked entirely
green. It also forces `Ready` to false.

This is the control that prevents the most plausible real-world failure: a
subsystem is added, nobody writes scenarios, and the readiness report keeps
saying green because it is only reporting on what it was asked about.

### C9 — Scenario provenance is anchored

Every run records the registry version and digest it ran under, and every golden
records the scenario digest it was recorded against. A scenario edited without a
version bump is detectable even though the version check passed.

### C10 — Failure injection is capability-gated

A scenario requiring an injection its subject does not declare is **skipped with
the missing capability named**, not passed.
`TestVerification_InjectionCapabilitiesAreDeclared` enforces the declaration
across the whole library. A silently ineffective injection would be
indistinguishable from working safety coverage.

---

## 3. Confidentiality of recordings

### C11 — Trend history carries fingerprints, not content

`TrendPoint` holds the run, scenario, subject, verdict, behaviour fingerprint,
total step seconds and timestamp. **No observation.**
`TestIntegration_TrendPointCarriesNoObservation` enforces it by rendering the
struct and refusing to find step data.

This is the same reasoning as frozen invariant **I7** — events carry identifiers
and fingerprints, never content, because Kafka cannot delete a record. A trend
history is the platform's long-lived table; making it content-free is what keeps
a years-long history from becoming a permanent archive of everything every
subsystem ever did.

### C12 — Every stored collection is bounded

Runs (200), observations per scenario (50), benchmarks per scenario (50) and
trend points (500). `StorageStats.Evicted` reports when eviction has occurred,
so a reader can tell whether they are looking at the whole history.

Bounded retention is both a memory control and a privacy control: it caps how
far back recorded subsystem behaviour persists. ENGINEERING_AUDIT F8 fixed the
one collection that was unbounded.

### C13 — Free-text detail is excluded from fingerprints

`Detail` is excluded from `BehaviourPrint`. Beyond the stated reason (improving
an error message should not be drift), this limits the blast radius of a
subsystem that puts something sensitive in an error string: it does not enter
the fingerprint that propagates into trend points.

It **does** remain in the observation and the golden. See F-SEC-2.

---

## 4. Findings

### F-SEC-1 — Observations are unbounded in size *(medium)*

Nothing caps how much a subject may return in a `Value`, how many steps a
scenario may have, or how large a `StepResult`'s state may be. A subject
returning a large output grows the observation, its clone in storage, and its
golden — linearly and durably.

Phase 10C caps memory records at a fixed size and refuses oversized ones
(`INV-MEM-8`). This platform has no equivalent.

**Current mitigation:** adapter discipline. Every adapter in `evalsubjects`
returns small, synthetic values.
**Not a mitigation:** anything the platform enforces.

**Recommendation:** a configurable `MaxObservationBytes`, refusing at
`Observation` construction with the same shape of error `GoldenStore.Record`
already produces. Sizing should follow Phase 10C's precedent rather than being
invented here.

### F-SEC-2 — Recordings may contain personal data, and nothing says they must not *(medium)*

The memory engine handles personal data by design. Its adapter surfaces
retrieved values into `StepResult.Output`, which enters the observation, the
golden, and any report rendered from them. Goldens are approved and kept
indefinitely; the 90-day retention rule (**ADR-0012**) that governs transcripts,
memory and governance records has **no counterpart here**.

Today this is theoretical — the adapters use synthetic fixtures, and the store
is in-memory and dies with the process. It stops being theoretical the moment
A4's durable storage lands, which is precisely when it will be least convenient
to discover.

**Recommendation, before durable storage ships:**

1. State the rule: **scenarios must not use production data.** Make it a
   documented constraint on scenario authorship, in this document and in
   `evalsubjects/subjects.go`.
2. Give `Golden` a retention stamp and align it with ADR-0012.
3. Consider a `Redacted` flag on `Value` that adapters set for anything derived
   from a real record, excluded from storage but included in the fingerprint —
   so drift is still detectable without retaining the content.

### F-SEC-3 — No access control on approval *(low, by design for now)*

`Approve` takes an author string and trusts it. There is no identity, no
signature, no authorisation check. Anyone with process access can approve any
golden as anyone.

This is correct for an in-process library and wrong for a service. The
attribution is an **audit aid, not an authentication mechanism**, and this
document is the place that says so plainly so nobody later mistakes a recorded
name for a verified one.

**Recommendation:** when the evaluation platform is exposed as a service, the
approval path needs real identity and the reason field needs to become a linked
review artifact rather than a string.

### F-SEC-4 — Reason codes and labels are unbounded strings *(low)*

`Reason`, `Label` and `ApprovedBy` accept arbitrary strings, and they flow into
reports. Phase 10E hit the equivalent issue on a path into Kafka and added
`checkReasonCode` to bound it.

Here the strings do not reach an event bus, so the risk is report legibility
rather than data integrity. Worth bounding if these become externally supplied.

### F-SEC-5 — `-race` has never been run *(BLOCKING, shared with A2)*

Seven modules, none checked by the race detector, including this platform's
copy-on-write registry, golden store and shared metrics.

A data race in the golden store is not an ordinary availability bug — it is a
**wrong verdict**, which is the failure mode with the highest blast radius here,
because the output is trusted without further checking.

The concurrency tests pass, which is weaker evidence than it appears: they show
no race was severe enough to corrupt a verdict in those runs.

**This remains the single blocking finding for the phase.** Closing it needs a
CI runner with a C toolchain, not a code change.

---

## 5. Explicitly out of scope

Excluded by the brief and absent from the code: LLM safety APIs, prompt
handling, fraud detection, telephony intelligence, business logic, payment
policy, and any external evaluation or moderation service. The platform also
performs no content judgement of its own — it compares behaviour against
approved recordings and never assesses whether an output is *good*, which keeps
quality a human input to this system rather than an output of it.

---

## 6. Summary

| ID | Finding | Severity | Status |
|---|---|---|---|
| F-SEC-1 | Observations unbounded in size | medium | open |
| F-SEC-2 | No retention rule for recordings that may hold personal data | medium | open, blocking for A4 |
| F-SEC-3 | Approval attribution is not authentication | low | by design, documented |
| F-SEC-4 | Unbounded reason and label strings | low | open |
| F-SEC-5 | `-race` never run | **blocking** | open (infrastructure) |

Thirteen controls; five findings; one blocking, and it is a missing CI capability
rather than a defect in the code.

---

## 7. Related

- [ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md)
- [PLATFORM_READINESS_REPORT.md](PLATFORM_READINESS_REPORT.md)
