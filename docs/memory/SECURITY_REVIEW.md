# Security Review — Enterprise Memory Engine

**Phase 10C** · `packages/go/memory` · 2026-08
**Verdict: APPROVE FOR INTEGRATION — NOT FOR PRODUCTION UNTIL R1, R2 AND A2 CLOSE**

A memory engine is the highest-value target in this platform. It is the one
component that holds, by design, a durable record of what people said on the
phone. This review is written accordingly.

---

## 1 · Threat model

**Assets:** conversation content · caller identity and contact graph · stated
preferences · business configuration · fraud signals · the *fact* that a memory
exists (itself disclosive — "the system remembers a call from this number").

**Adversaries considered:**

| # | Adversary | Capability |
|---|---|---|
| T1 | Compromised consumer of the engine | In-process, calls the API arbitrarily |
| T2 | Cross-namespace confusion | A fraud agent reading assistant memories |
| T3 | Log / event-stream reader | Reads Kafka, metrics, logs — not the store |
| T4 | Subject of the data | Exercises access, correction and erasure rights |
| T5 | Regulator / auditor | Asks "who read this, when, on what basis" |
| T6 | Insider with operator access | Runs sweeps, forces rollbacks, reads dumps |
| T7 | Downstream model provider | Receives whatever the context builder assembled |

**Out of scope for 10C:** at-rest encryption of a durable store (no durable store
exists yet — Phase 10D), transport security, host compromise, and key
management. `Encryptor` is the declared seam and is deliberately unimplemented.

---

## 2 · Controls

### C1 · Consent enforced at write — T1, T4, T5

A Personal or Sensitive record without a valid consent reference is **refused**,
not stored-and-flagged. `Policy.admit` is the single authority (the duplicate
check in `validate` was removed — ENGINEERING_AUDIT §F1).

**Why at write:** an unlawful memory that is created and later detected by an
audit has already been processed, replicated into whatever read it, and possibly
sent to a model provider. Refusal at write means it never exists.

`ConsentChecker` is an interface, so the authority lives in the Identity
context. This engine does not decide what consent is; it refuses to act without
one.

### C2 · Secret classification refused outright — T1

`Sensitivity == Secret` is rejected by `Record.validate`. No configuration
enables it.

A memory engine that accepted credentials, tokens or keys would be one more
place they leak from — one with a 90-day retention window, a promotion ladder
and a summarisation path. Authentication material belongs in Identity under its
own handling.

### C3 · Events carry identifiers, not content — T3

Frozen invariant **I7**. `Event` has key, tier, version, timestamp, actor and
type. **There is no field that could hold a payload.**

The test applied during design: *if this Kafka topic were retained forever and
could never be deleted, would that be a compliance failure?* It must be no.
Kafka cannot delete a record, so an erasure right that depends on Kafka deletion
is not an erasure right.

Asserted mechanically: `TestEvents_CarryIdentifiersNotContent` publishes records with
distinctive payload strings and asserts no published event contains any of them.

### C4 · Namespace isolation — T2

Four namespaces, isolated by construction. The same `Key` in two namespaces is
two different records; there is no API that reads across namespaces except
`Coordinator`, which exists for erasure and is the deliberate exception.

A fraud agent cannot reach the assistant's memory by guessing a key. It cannot
reach it by construction, not by a permission check that could be misconfigured.

### C5 · Erasure is complete and reports what survived — T4, T5

`Coordinator.Forget` walks **every** namespace. A subject's memories are spread
across four stores; an erasure reaching one is a compliance failure that reports
success.

`ErasureReport.Complete` is false whenever anything survived, and survivors are
named per namespace with their basis. **An erasure that silently retains is
worse than one that refuses** — the subject believes they were forgotten.

Legal-hold records are **redacted, not deleted**: payload and indexed attributes
destroyed, existence retained and provable. That is the shape both obligations
take at once.

### C6 · Redaction destroys indexed attributes — T4

`Store.Redact` clears `Value.Data` *and* removes every secondary-index entry.

Leaving attributes would keep the record discoverable by content that was
supposed to be gone. A record you can still find by searching for the thing you
redacted has not been redacted. `TestIntegration_RedactDestroysIndexableAttributes`.

### C7 · Sensitive reads are audited — T5, T6

Every retrieve of a `Sensitive` record calls `Auditor.Record(actor, key,
operation)`. `RequireAuditor` defaults **true**: an engine holding Sensitive
data with no configured auditor **fails the read** rather than serving it
silently.

Only Sensitive reads are audited. Auditing every Internal read would bury the
entries that matter, and an audit log nobody can read is not a control.

### C8 · Clone-under-lock — T1

Every read returns an independent copy produced under the shard lock
(INV-MEM-1). A caller cannot mutate stored state, cannot bypass a version bump,
and cannot observe a torn record mid-write. This closed a real latent race —
ENGINEERING_AUDIT §F2.

### C9 · Unscoped queries refused — T1, T6

A query with no subject, no attribute and no time range is a full-store dump.
`ErrUnscopedQuery`. The first time an accidental dump happens in production it
is either a latency incident or a privacy one; this makes it an error at the
call site.

### C10 · Derived memories do not reach long term by default — T4, T7

An inferred memory promoted to permanence is the platform deciding, on its own,
to permanently remember something a person never told it. Guarded on **both** the
promotion path and the write path, because a caller could otherwise construct a
long-term derived record directly and bypass the ladder.

### C11 · Sensitive records are never compressed — T7

Compression would mean sending Sensitive content to a model provider. The engine
does not make that decision on a deployment's behalf.

### C12 · Classification inheritance is strictest-wins — T7

A summary inherits the strictest sensitivity and longest retention of its
inputs. Summarising three Internal and one Personal record yields a Personal
record. A summary classified more loosely than its sources is a laundering path
— content downgraded by being paraphrased.

### C13 · Timestamps are engine-owned — T1

`CreatedAt`, `UpdatedAt`, `ExpiresAt` and `AccessedAt` come from the injected
clock; caller values are overwritten. A caller could otherwise backdate a record
past its retention window, or forward-date one into immortality. Retention is
not a field a caller fills in.

### C14 · Size cap — T1

`MaxRecordBytes` (256 KiB) refused at admission. An unbounded payload is an
in-process memory-exhaustion vector, and a 40 MB "memory" is not a memory.

---

## 3 · Findings

### R1 · Indexed attributes are unenforced — **must close before production**

**Severity: high.**

INV-MEM-7 says indexed attributes carry no Personal content. **Nothing enforces
it.** A caller can write `Value.Attributes["contact"] = "+91XXXXXXXXXX"`, and
that phone number then lives in the secondary index in plaintext, outside the
payload, outside any `Encryptor`, and outside the classification that governs
the record.

Redaction *does* remove attributes (C6), so it is not an erasure hole. It is a
**classification hole**: a Personal identifier sitting in an index structure
that the sensitivity model assumes contains only opaque references.

**Recommended fix:** validate attribute values at admission against a
conservative pattern set (E.164, long digit runs, `@`), and refuse rather than
warn. Alternatively require attribute values to be opaque handles by
construction. Either is a contained change to `Policy.admit`.

**Not done in 10C** because a pattern set is a judgement call about false
positives that should be made with the Identity context's rules in hand, not
invented here.

### R2 · `Encryptor` is unimplemented — **must close before production**

**Severity: high, by design.**

The hook is wired on both store and retrieve paths and proven by
`ReverseEncryptor`, which is explicitly labelled *NOT CRYPTOGRAPHY*. There is no
real implementation, no key management, no rotation, no envelope encryption.

**In 10C this is correct** — the brief excludes it and there is no durable store
to encrypt. **It becomes a blocking gap the moment anything persists.** Phase
10D must supply a KMS-backed `Encryptor` with per-subject key derivation and
documented rotation before a single record leaves the process.

Recorded here rather than left implicit, because "the interface exists" reads as
"encryption exists" to anyone skimming.

### R3 · No authorisation model — accepted for this phase

**Severity: medium.**

`actor` is a string passed to the auditor. The engine does **not** check whether
that actor may read that record. Authorisation is the caller's responsibility.

**Accepted** because authorisation belongs to Identity and duplicating it here
would create two policy sources that drift — the same failure mode as F1, at
platform scale. But it must be stated plainly: **this engine authenticates
nothing and authorises nothing.** A compromised in-process consumer reads
whatever it names, within its namespace.

Mitigations that do exist: namespace isolation (C4), `MaxSensitivity` on every
read, scope requirement (C9), and an audit trail on Sensitive reads (C7).

### R4 · Snapshot and rollback are an insider primitive — T6

**Severity: medium.**

`Snapshot`/`Rollback` let an operator revert a subject's memories to an earlier
version. `Rollback` refuses to revert a legal hold (INV-MEM-11), which is the
important guard. But rollback across a **consent withdrawal** would restore
records that were lawfully erased.

**Recommended:** treat a completed erasure as a rollback barrier — snapshots
taken before an erasure cannot be applied after it. Not implemented in 10C.
Currently mitigated only by the fact that `Forget` deletes rather than
soft-deletes, so there is nothing for a rollback to resurrect *within the
store* — but a snapshot taken beforehand is held by the caller, and the engine
does not refuse it.

### R5 · Metrics label cardinality — T3

**Severity: low.** Metrics are labelled by namespace, kind, tier and outcome —
all bounded enums. **No subject identifier is ever a label.** A subject-labelled
metric would put an identifier into a monitoring system with its own retention,
outside every control in §2. Verified by inspection of `metrics.go`; no test
asserts it, which is worth adding.

### R6 · Error messages could echo content — T3

**Severity: low.** `ConfigError` and the typed sentinels carry keys and field
names, never payloads. Checked by inspection. A test asserting no error string
contains payload bytes would make this durable rather than reviewed-once.

### R7 · No integrity verification — T6

**Severity: low.** `StateCorrupt` exists in the lifecycle table but **nothing
sets it** — there is no checksum, no MAC, no verification on read. In an
in-memory engine with no durable store there is nothing to verify against. When
Phase 10D adds persistence, a per-record MAC verified on load is what makes
`StateCorrupt` reachable and makes tampering with the durable store detectable.

Recorded so the state is understood as a **prepared seam**, not dead code that
implies a check nobody implemented.

### R8 · Publisher failures are counted and swallowed — T5

**Severity: low, deliberate.** A failing publisher increments a metric; the
write proceeds. Correct for availability — one broken subscriber must not fail
memory writes for everyone. But it means **event loss is possible and the audit
trail derived from events is not complete by construction**. The audit trail
that matters for C7 is `Auditor`, which is synchronous and *does* fail the read.
Anyone treating the Kafka stream as an audit log is making a mistake this note
exists to prevent.

---

## 4 · DPDP alignment

| Obligation | Mechanism | Gap |
|---|---|---|
| Lawful basis before processing | C1, refused at write | — |
| Purpose limitation | Namespace isolation (C4), kind taxonomy | Not enforced across namespaces by policy |
| Data minimisation | Compression framework, tier expiry, size cap | Compression needs a summariser |
| Storage limitation | Retention classes; standard = 90 days per ADR-0012 | — |
| **Right to erasure** | C5 — spans namespaces, reports survivors | R4 rollback barrier |
| Right to correction | `Update` with CAS and version history | — |
| Right to access | `BySubject` returns everything held | Unindexed scan; fine at this scale |
| Security safeguards | C8, C14, namespace isolation | **R2 — no real encryption** |
| Breach detectability | Sensitive-read audit (C7) | **R7 — no integrity check** |
| Consent withdrawal | Records deleted, not filtered at read | — |

**Retention deliberately matches ADR-0012's 90-day transcript window.** A memory
layer outliving the transcripts it summarises would be a second, unregulated
copy of the same personal data with a different deletion date — the kind of
thing found during an audit rather than designed.

---

## 5 · Attack scenarios

| # | Scenario | Outcome |
|---|---|---|
| 1 | Consumer stores a Personal record without consent | **Refused** — `ErrConsentRequired` |
| 2 | Consumer stores an API key as a memory | **Refused** — Secret rejected (C2) |
| 3 | Fraud agent reads assistant memory by key | **Fails** — namespaces are separate stores |
| 4 | Attacker reads the Kafka stream to reconstruct conversations | **Yields keys only** (C3) |
| 5 | Consumer mutates a returned record to bypass versioning | **Mutates a clone** (C8) |
| 6 | Consumer issues an unscoped query to dump the store | **Refused** (C9) |
| 7 | Subject requests erasure; assistant namespace only is checked | **Cannot happen** — Coordinator walks all (C5) |
| 8 | Legal-hold record must survive erasure but not be readable | **Redacted, existence retained and reported** |
| 9 | Redacted record found by searching its old attribute | **Cannot** — attributes destroyed (C6) |
| 10 | Caller backdates a record past its retention window | **Overwritten by engine clock** (C13) |
| 11 | Caller writes a phone number as an indexed attribute | **Succeeds — R1, the top open finding** |
| 12 | Operator rolls back across a consent withdrawal | **Not prevented — R4** |
| 13 | 40 MB payload as a memory | **Refused** (C14) |
| 14 | Derived inference written straight to long term | **Refused** (C10) |
| 15 | Sensitive record sent to a model via compression | **Never selected** (C11) |
| 16 | Sensitive read with no auditor configured | **Read fails** (C7) |

Twelve of sixteen are refused by construction. The three that are not — 11, 12
and the encryption gap behind several — are R1, R4 and R2.

---

## 6 · Verdict

**APPROVE FOR INTEGRATION. NOT APPROVED FOR PRODUCTION.**

The engine's privacy posture is structural rather than procedural: consent
refused at write, Secret refused outright, events that cannot carry content,
namespaces that cannot be crossed, erasure that cannot miss a namespace and
cannot silently retain. Those are the controls that matter most and they are
enforced by absence, which is the only kind that cannot be misconfigured in
production.

**Three items block production:**

| | | Owner |
|---|---|---|
| **R1** | Attribute content unenforced — a Personal identifier can sit in plaintext in the index | 10C follow-up |
| **R2** | No real `Encryptor` — blocking the moment anything persists | Phase 10D |
| **A2** | Race detector never run, on three concurrent modules (ENGINEERING_AUDIT) | CI |

Then, in order: **R4** (rollback barrier across erasure), **R7** (integrity
verification once persistence exists), **R3** (an authorisation story owned by
Identity, referenced here).

**R3 deserves a sentence of emphasis.** This engine authenticates nothing and
authorises nothing. Every control in §2 protects against misuse of the API's
*shape*; none protects against a consumer that is entitled to call it and should
not be. That boundary belongs to Identity, and a deployment that assumes the
memory engine is enforcing it will be wrong.
