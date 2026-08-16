# Security Review — Enterprise Tool Calling Runtime

**Phase 10D** · `packages/go/toolruntime` · 2026-08
**Verdict: APPROVE FOR INTEGRATION — NOT FOR PRODUCTION UNTIL R1, R2 AND A2 CLOSE**

Every other module in this platform reads, transforms or remembers. **This one
acts on the outside world.** It books, cancels, charges, sends and calls. A
compromise here does not leak data; it takes actions. This review is written
accordingly.

---

## 1 · Threat model

**Assets:** the ability to take an action on a person's behalf · the arguments
of that action (frequently personal data) · the audit trail proving who
authorised it · downstream credentials held by adapters · the fact that an
action was taken.

| # | Adversary | Capability |
|---|---|---|
| T1 | Compromised in-process consumer | Calls the runtime API arbitrarily |
| T2 | **Hostile or buggy tool implementation** | Runs in-process with full process privileges |
| T3 | Prompt-injected conversation | Causes a plausible-looking intent to be emitted |
| T4 | Log / event-stream reader | Reads Kafka, metrics, logs — not the store |
| T5 | Subject of the action | Exercises access and erasure rights |
| T6 | Regulator / auditor | Asks "who did that, on whose authority" |
| T7 | Insider with operator access | Installs overrides, drains tools, reads dumps |
| T8 | Downstream service | Receives whatever an adapter sends it |

**Out of scope for 10D:** transport security, credential storage (adapters own
theirs), host compromise, and the durable audit store (none exists yet).

---

## 2 · Controls

### C1 · One invocation path — T1, T2, T3

`Executor.Execute` is the **only** function in the module that calls a `Tool`.
Every other file plans, permits, records or reports.

That is what makes this claim checkable rather than aspirational: **no tool is
ever invoked without a permission decision, a deadline, a budget and an audit
entry**, because there is exactly one path and it does all four.

### C2 · Effect classification drives safety — T3, T8

`Effect` is not a label. It is enforced:

| Effect | Enforced consequence |
|---|---|
| `EffectIrreversible` | `MaxAttempts > 1` refused at registration; fallback chain refused at planning |
| `EffectIrreversible` | Cannot be `Compensable` — if it can be undone it is not irreversible |
| Mutating | Idempotency key mandatory |
| Mutating + timeout in a fallback | Chain stops rather than trying the next candidate |

**The irreversible case is the one that matters against T3.** A prompt-injected
conversation that produces an intent for "send this message" gets exactly one
attempt, no fallback, and an explicit permission requirement — it does not get a
retry loop that sends it three times.

### C3 · Permission decided before anything else — T1, T3, T6

Refused work never reaches a ledger, a slot or a tool. `Decision` carries **why**,
including exactly which permissions are missing, so a caller can request the
right thing rather than guessing.

Consent is separate from permission and surfaces as `ErrConsentRequired`, which
is actionable: the caller can ask.

### C4 · Overrides are bounded, attributed and loud — T7

Emergency overrides exist because **systems that cannot be overridden get
overridden anyway**, by somebody editing a database at 3 a.m. with no record.
Making the path explicit makes it reviewable.

| Rule | Enforced at |
|---|---|
| `ExpiresAt` required | Construction — an override with no expiry is a permanent policy change |
| `AuthorisedBy` required | Construction — an anonymous override cannot be reviewed |
| `Reason` required | Construction |
| Must cover **every** missing permission | `overrideCovers` — waiving two of three is not a decision about the third |
| Audited at install, not only at use | `AddOverride` |
| Audited when it fires | `PermissionEngine.record` |
| Listed in the health report | `Coordinator.Health` — an override nobody notices is one nobody withdraws |

### C5 · Idempotency is derived, not trusted — T1, T3, T8

A caller cannot choose an execution key. It is derived from the pinned
descriptor, the actor, the correlation scope and a canonical encoding of the
arguments.

**A caller-chosen key is a caller-chosen bug** — two different requests sharing
one, or a fresh key per attempt that deduplicates nothing. Deriving it means a
compromised caller cannot make two different actions collide, and cannot make
one action run twice by varying a key.

### C6 · Events and audit carry fingerprints, not payloads — T4, T5

Frozen invariant **I7**. `Event` and `AuditEntry` have **no field capable of
holding arguments or results**.

The design test: *if this Kafka topic were retained forever and could never be
deleted, would that be a compliance failure?* It must be no. Kafka cannot delete
an individual record, so an erasure right that depends on deleting from a topic
is not an erasure right.

Asserted mechanically: `TestEvents_CarryNoPayload` executes with a distinctive
payload string and asserts no published event contains it.

The same rule covers the dead-letter queue, which operators read during incidents
and occasionally paste into tickets — three places personal data must not travel
to. `TestIntegration_ExhaustedRetriesLandInTheDeadLetterQueue` asserts it.

### C7 · Output validation at the boundary — T2, T8

A tool that returns garbage poisons everything downstream of it. `ValidateOutput`
is the last place the runtime can still tell truth from garbage cheaply, and a
violation **fails the execution** rather than being passed on.

Undeclared output fields are refused unless a contract opts in. Undeclared
**input** is refused with no opt-out at all — an ignored argument presents to a
person as "it ignored what I asked for".

### C8 · Panic containment — T2

A panicking tool becomes a failed execution, not a dead process. **The panic
value is deliberately not put in the error**: a panic value can contain caller
content, and errors travel into logs and metrics.

### C9 · Bounded reason codes — T4

`shortReason` maps every error to a fixed vocabulary before it becomes a metric
label or an event field. Passing an error's text through would let a downstream
service's error message determine this platform's metric cardinality — a denial
of service with extra steps.

### C10 · Budgets on input, output and streams — T2

Input size refused at admission. Output charged **incrementally**, so an
unbounded stream is caught rather than only an oversized final result. The
metered sink wraps whatever the caller supplied, so enforcement does not depend
on anyone watching.

### C11 · Load shedding at admission — T1

Frozen invariant **I11**. `ErrQueueFull` at admission rather than an unbounded
queue. A shed execution never reaches the tool at all, and the counters add up —
`TestStress_OverloadShedsCleanlyAndIsAccountedFor`.

### C12 · Compensation on cancellation — T5

A cancelled execution still compensates. Hanging up must not leave a half-made
booking.

### C13 · Registry immutability under execution — T7

Copy-on-write snapshots mean a plan resolved against one snapshot cannot have
its tool changed underneath it. An operator (or an attacker with registry
access) cannot substitute an implementation into an execution already in flight.

### C14 · Draining rather than interrupting — T7

Retiring a version mid-execution would fail live calls. Draining lets pinned
plans finish while refusing new ones.

---

## 3 · Findings

### R1 · The sandbox is not isolation — **must close before untrusted tools**

**Severity: high. By design, and named so nobody assumes otherwise.**

`BudgetSandbox` enforces concurrent slots, per-tool concurrency and output bytes.
It does **not** enforce memory or CPU, and it **cannot**: an in-process Go tool
runs on the same scheduler, in the same address space, with the same file
descriptors and network access as the runtime.

A hostile in-process tool can allocate until the process dies, spin a core, open
a socket, read another tool's memory, or call `os.Exit`. **None of that is
detectable or preventable from inside the same process.**

Worse for availability: **Go cannot kill a goroutine.** A tool that ignores
cancellation is abandoned and holds whatever it holds — a connection, a buffer,
a lock — possibly forever. The runtime counts abandonments
(`Supervisor.Abandoned()`), which converts an invisible leak into a dashboard
number, but counting is not containment.

**What this means today:** every tool in this runtime must be treated as trusted
code, reviewed and owned like any other part of the platform. That is a
reasonable posture for first-party adapters and an unacceptable one for anything
third-party or dynamically loaded.

**Required before untrusted tools:** an out-of-process `Sandbox` — subprocess,
container or remote worker — where the runtime can enforce a memory limit and
actually kill. The interface exists and the executor already asks permission
before invoking, so this is a constructor change rather than a redesign.

### R2 · Idempotency is per-process — **must close before multi-replica writes**

**Severity: high.**

The ledger is in-memory. Two replicas do not share one, so **exactly-once holds
within a replica and at-least-once holds across them.**

For `EffectIdempotentWrite` this is harmless — the `Invocation` carries the key
and the downstream deduplicates. For `EffectWrite` a cross-replica duplicate
does the thing twice: two bookings, two charges, two callbacks.

Mitigations today: correlation-scoped keys mean duplicates only arise from
genuine cross-replica retries, and the key is passed to the tool so an adapter
can deduplicate downstream. **Neither is a substitute for a shared ledger.**

**Required before running more than one replica with `EffectWrite` tools:** a
Redis- or Aurora-backed ledger with an atomic claim. Phase 10E.

### R3 · Grants are supplied, not verified — accepted, stated plainly

**Severity: medium.**

`PermissionEngine.Evaluate` evaluates the `Grant` the caller passed in. It does
not verify that the actor really holds those permissions; that is Identity's job.

**Accepted** because fetching per execution would put an availability dependency
on Identity in the middle of a live call, and duplicating Identity's rules here
would create two policy sources that drift — the failure mode Phase 10C
documented at record level, at platform scale.

But it must be stated: **a compromised in-process caller can grant itself
anything.** Every control in §2 protects against misuse of the API's *shape*;
none protects against a caller inside the trust boundary that lies about who it
is.

The boundary that matters is therefore the process boundary, and the mitigation
is that the conversation engine and the tool runtime are deployed together and
trusted together. A deployment that exposes this runtime over a network **must**
authenticate and re-derive grants at that edge.

### R4 · Prompt injection reaches the runtime as a well-formed intent — T3

**Severity: medium. Partially mitigated, and the residual risk is real.**

An injected conversation cannot invent a capability that does not exist, cannot
bypass permissions, and cannot make an irreversible action retry. But it **can**
cause a legitimate capability to be requested with attacker-chosen arguments —
"cancel the appointment", "send this text" — if the actor genuinely holds the
permission.

What the runtime contributes: irreversible actions get one attempt and require
an explicit permission; every execution is fingerprinted and audited with a
correlation; `Plan.Mutates()` and `Plan.Irreversible()` let a caller demand
human confirmation **before** anything runs.

What it cannot contribute: judgement about whether the request was really the
subscriber's. **The defence against T3 is upstream** — in the conversation
engine's safety layer and in requiring confirmation for irreversible plans — and
this module's job is to make that decision expressible, which `Plan` does.

**Recommended:** a deployment policy that no `EffectIrreversible` plan executes
without an explicit confirmation signal in the grant. The mechanism exists
(`RequiredPermissions` plus `RequiresConsent`); making it mandatory is a
configuration decision this module deliberately does not make for the operator.

### R5 · Audit is best-effort and in-memory — T6

**Severity: medium.**

An audit write failure is counted and the execution proceeds.

That is the right call: failing an already-completed action because its record
could not be written leaves the world changed **and** unrecorded, which is
strictly worse than changed and unrecorded-with-a-metric.

But two things follow. **The audit trail is not guaranteed complete**, and
`RecordingAuditor` is in-memory with a bounded size — the only implementation in
this module. A durable, append-only audit store is required before T6 can be
answered, and it is A3's scope.

`Config.RequireAuditor` defaults **true**, so a runtime cannot start without
*some* auditor; that stops the accidental case, not the storage gap.

### R6 · Fingerprints are not commitments — T4, T6

**Severity: low, and worth saying because the word invites the assumption.**

`Fingerprint` is SHA-256 truncated to 64 bits. It identifies content without
revealing it and lets an auditor ask "was this the same call as last time". It
is **not** a defence against an adversary who chooses the content: 64 bits is
findable, and nothing in this runtime treats a matching fingerprint as proof of
anything.

If a future requirement needs tamper-evidence rather than correlation, that
needs a full-width MAC with a managed key, which is a different mechanism.

### R7 · Arguments live in memory for the plan's lifetime — T2, T5

**Severity: low.**

`CompletedWork` retains the `Invocation` — including arguments — so compensation
can undo the thing it did. Undoing a booking requires the arguments that made it.

They are held for the duration of the plan and then dropped with the journal;
the journal is per plan rather than per runtime for exactly this reason. But
during that window the arguments, which are frequently personal data, sit in
process memory reachable by any in-process tool (R1).

**Accepted.** The alternative — re-deriving compensation arguments from an
external store — would make rollback depend on the availability of something
else at the moment things are already going wrong.

### R8 · No authorisation on operator surfaces — T7

**Severity: low in-process, high if exposed.**

`Coordinator.Cancel`, `Coordinator.Drain`, `Registry.Register`,
`PermissionEngine.AddOverride` and `Registry.SetHealth` have no access control.
Anything holding a `*ToolRuntime` can retire a tool, cancel every execution, or
install an override.

Consistent with R3 — this module authorises nothing — and acceptable in-process.
**Any deployment that exposes these over a network or a console must
authenticate and authorise at that edge.** Registration and override installation
are audited; cancellation and drain are not, which is a gap worth closing in 10E.

---

## 4 · Attack scenarios

| # | Scenario | Outcome |
|---|---|---|
| 1 | Caller requests a capability nothing provides | **Refused** — `ErrNoCapability` |
| 2 | Caller invokes a tool it lacks permission for | **Refused** before the ledger or the sandbox |
| 3 | Caller supplies an idempotency key to collide with another request | **Cannot** — keys are derived (C5) |
| 4 | Retry storm against a booking tool | **One invocation, N answers** (measured: 64 → 1) |
| 5 | Injected conversation asks to send an SMS three times | **One attempt, no fallback, no retry** (C2) |
| 6 | Tool returns a field its contract does not declare | **Execution fails**, not retried |
| 7 | Tool streams forever | **Output budget exhausted mid-stream** |
| 8 | Tool panics | **Failed execution**; panic value not logged |
| 9 | Tool ignores cancellation | **Abandoned and counted** — but not stopped (R1) |
| 10 | Tool allocates until OOM | **Not prevented** — R1 |
| 11 | Attacker reads the Kafka topic to reconstruct actions | **Yields identifiers and fingerprints only** |
| 12 | Attacker reads the dead-letter queue | Same |
| 13 | Operator installs a permanent override | **Refused** — expiry required |
| 14 | Operator installs an anonymous override | **Refused** — author required |
| 15 | Override waives two of three missing permissions | **Refused** — must cover all |
| 16 | Registry swapped mid-execution | **Cannot affect the in-flight plan** (C13) |
| 17 | Version retired while a plan is running | **Drain lets it finish** (C14) |
| 18 | Overload from a burst of requests | **Shed at admission**, counted, tool never entered |
| 19 | Compromised caller forges its own grant | **Succeeds — R3** |
| 20 | Two replicas both execute the same booking | **Succeeds — R2** |

**Seventeen of twenty are refused by construction.** The three that are not are
R1, R2 and R3, and they are the three findings.

---

## 5 · DPDP alignment

| Obligation | Mechanism | Gap |
|---|---|---|
| Lawful basis before processing | `RequiresConsent` on the contract, refused at permission | Consent is evaluated, not verified — R3 |
| Purpose limitation | Capability + permission scoping | Not enforced across capabilities |
| Data minimisation | Fingerprints in events and audit; no payloads anywhere | — |
| Storage limitation | Ledger TTL; journal dropped with the plan | Audit store does not exist yet — R5 |
| Right to erasure | **Nothing durable is stored by this module** | Adapters store; that is their obligation |
| Security safeguards | C7, C8, C10, C11 | **R1 — no isolation** |
| Accountability / traceability | Correlation on every event and audit entry; `Trace` assembles a turn | **R5 — best-effort, in-memory** |

**The strongest DPDP position this module has is that it stores almost nothing.**
The ledger holds results for a deduplication window and the journal holds
arguments for a plan's lifetime; both are bounded, in-memory and gone. The
personal data risk sits in adapters and in the audit store that does not exist
yet.

---

## 6 · Verdict

**APPROVE FOR INTEGRATION. NOT APPROVED FOR PRODUCTION.**

The runtime's safety posture is structural rather than procedural: one
invocation path that cannot be bypassed, irreversible effects that cannot be
retried, keys that cannot be chosen, events that cannot carry content, overrides
that cannot be permanent or anonymous, and a registry that cannot change under
an execution already running. Seventeen of twenty attack scenarios are refused by
construction.

**Three items block production:**

| | | Owner |
|---|---|---|
| **R1** | The sandbox is a budget, not a jail. Every tool must be trusted code until an out-of-process sandbox exists | Phase 10E |
| **R2** | Idempotency is per-process. Multi-replica `EffectWrite` can duplicate | Phase 10E |
| **A2** | Race detector never run, now across four concurrent modules | CI |

Then, in order: **R5** (durable audit store), **R4** (mandatory confirmation
policy for irreversible plans), **R8** (audit cancellation and drain).

**R3 deserves a closing sentence of emphasis.** This runtime authenticates
nothing and verifies no grant. It is safe only inside a trust boundary that
something else enforces. A deployment that exposes it over a network without
authenticating and re-deriving grants at that edge has handed an attacker the
ability to take actions on subscribers' behalf, and no control in §2 would stop
it.
