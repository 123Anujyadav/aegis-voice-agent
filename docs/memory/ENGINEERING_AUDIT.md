# Engineering Audit — Enterprise Memory Engine

**Phase 10C** · `packages/go/memory` · 2026-08
**Verdict: APPROVE WITH ONE BLOCKING FINDING (A2)**

A self-audit. It is written to be useful to a reviewer who wants to disagree
with it, so every defect found during construction is recorded with what it
actually was rather than what it looked like afterwards.

---

## 1 · Scope

| | |
|---|---|
| Module | `packages/go/memory` |
| Files | 17 Go files — 13 source, 4 test |
| Lines | **8,434** — 5,524 source, 2,910 test |
| Dependencies | **1** — `packages/go/runtime` (Phase 10A, frozen) |
| External dependencies | **0** (`go list -deps` shows stdlib only) |
| Tests | **77** — 39 unit, 30 integration/stress/recovery/failure, 8 evaluation |
| Benchmarks | 21 |
| `gofmt` | Clean |
| `go vet` | Clean |
| `-count=5 -shuffle=on` | Passes |
| `-race` | **NOT RUN — see A2** |

**Frozen artifacts untouched.** `packages/go/runtime` (10A) and
`packages/go/conversation` (10B) are unmodified; `go.work` gained one line
adding this module and the workspace builds all three. Verified by
`go build ./...` across the workspace.

---

## 2 · Compliance with the brief

### Prohibited dependencies — verified absent

LangChain Memory · LlamaIndex · CrewAI Memory · Semantic Kernel Memory ·
AutoGen Memory · Mem0 · Zep · Chroma · Pinecone · Weaviate · Milvus

`go.mod` has one `require` line, first-party. There is nothing to check beyond
that, and it is checkable in one command.

### Excluded implementations — verified absent

| Excluded | Evidence |
|---|---|
| LLM memory / summarisation | `Summarizer` declared, **no implementation in the module**. `ConcatSummarizer` (harness) concatenates and says so |
| Embeddings | No vector type, no float slice, no distance function anywhere |
| Vector database | Index is maps and buckets |
| Semantic search | `Search` filters on scalars only |
| Prompt templates | None |
| Conversation behaviour | No import of 10B; `KindConversation` is a label |
| Telephony logic | No import, no type |
| Fraud logic | `NamespaceFraud` is a name |
| Business workflows | `KindBusiness` is a label |

### Required

Go 1.25+ ✓ (`go.work` 1.25.0, toolchain 1.26.5) · provider agnostic ✓ (no
provider named anywhere) · own index layer ✓ · from scratch ✓.

Python 3.12, protobuf, gRPC, Kafka, Redis and Aurora are named in the brief's
stack. **This phase delivers the Go engine and the event contract only.** Topic
names follow the frozen `eventbus` convention so a Kafka binding is a wiring
task; `ColdStore` is the declared, unimplemented seam for Aurora/S3. No Python,
no proto file, no Redis client is delivered in 10C. Flagged as **A3** rather
than silently scoped out.

---

## 3 · Defects found and fixed during construction

Five. Recorded because a phase report claiming none would be a report nobody
should trust.

### F1 · Two paths enforcing one rule — consent

**Severity: high. Found by test, fixed.**

`Record.validate()` and `Policy.admit()` both checked for a consent reference on
Personal records. `validate` runs first and returned a `*ConfigError`, so
`Policy.admit`'s typed `ErrConsentRequired` sentinel **could never be observed by
a caller**. `TestPolicy_PersonalDataRequiresConsent` failed on the error type,
which is how it surfaced.

Fixed by deleting the check from `validate`:

> `validate` answers "is this record well-formed". `Policy.admit` answers "may
> this record exist here". Two paths enforcing one rule is how the wrong one
> wins.

**Why it mattered:** a caller branching on `errors.Is(err, ErrConsentRequired)`
to trigger a consent prompt would have fallen through to a generic error path.
The rule was enforced; the *response* to it was unreachable.

### F2 · `Index.Get` cloned outside the lock

**Severity: high. Found while optimising Search, fixed.**

`Get` took the read lock, fetched the pointer, released, then cloned. Two
failures in one: a concurrent writer could mutate the record mid-copy, and a
caller briefly held a live pointer into the store — mutable without a version
bump, defeating optimistic locking.

Fixed by cloning under the lock, with the reasoning recorded in the code:

> Returning a pointer out of a lock is returning a promise the lock cannot keep.

**Found by reading, not by a failing test** — a race the test suite did not
provoke and, without `-race` (A2), might never have.

### F3 · Exemption precedence — incidental reason reported over durable one

**Severity: low. Found by test, fixed.**

A pinned record inside the recent window was reported exempt because it was
`recent`. Correct outcome, misleading explanation. Precedence reordered to
**target → pinned → sensitive → recent → within_horizon**:

> "Pinned" is a property of the record; "recent" is a property of where the
> window happens to fall today.

**Why it mattered:** an operator reading a compression report and seeing
"recent" would reasonably conclude the record becomes compressible next week. It
never does.

### F4 · `FailAfter: -1` never failed

**Severity: low (test infrastructure). Found by inspection, fixed.**

`ConcatSummarizer` guarded on `FailAfter > 0`, so a test setting `-1` to mean
"always fail" silently never failed — a failure-injection test that injected
nothing and passed.

Fixed with an explicit `FailAlways bool` rather than a sentinel count:
overloading a count with a magic value is how a test ends up not testing what it
is named after.

### F5 · Duplicate field name in `ForgetResult`

**Severity: trivial. Compile error.** `Retained int` and `Retained Keys`.
Renamed to `RetainedCount` / `RetainedKeys`.

---

## 4 · Open findings

### A1 · Duplicate metrics instrument set — accepted

**Severity: low. Not fixed. Same finding as Phase 10B §A1.**

`runtime.Metrics` (10A) exports its types but keeps its constructors
unexported, so it is closed for extension. Both 10B and 10C therefore carry
their own near-identical instrument set — three copies of counter/gauge/
histogram plumbing across the platform.

**Not fixed because 10A is frozen.** The correct fix is a superseding ADR
exporting `runtime.NewCounter` and friends, then deleting the duplicates in 10B
and 10C. Recorded here so the third occurrence is on the record rather than
rediscovered in 10D.

**Risk if left:** instrument definitions drift between modules and a dashboard
built on one module's label conventions misreads another's.

### A2 · Race detector never run — **BLOCKING**

**Severity: high. Not fixed. Cannot be fixed on this machine.**

`go test -race` requires cgo, cgo requires a C toolchain, and there is no gcc on
this machine. **The memory engine has never been run under the race detector.**

This now applies to **three concurrent modules** — runtime (10A), conversation
(10B), memory (10C) — and 10C is the most concurrency-dense of them: 16 sharded
mutexes, a copy-on-write registry, a background sweeper mutating records while
readers clone them, and a coordinator walking every namespace during erasure.

**What exists instead:** concurrent-reader, concurrent-writer, reader-during-
sweep and erasure-during-write tests, passing at `-count=10 -shuffle=on`. F2 was
a real race in exactly this code and was found by **reading**, which is the
argument for the finding rather than against it.

**Required before production:** one CI job on Linux with cgo enabled running
`go test -race -count=5 ./...` across all three modules. Until it has run and
passed, the strongest honest claim is *"no data race was observed"*, not *"there
are no data races"*.

### A3 · Phase delivers the Go engine only

**Severity: informational.** See §2. Python 3.12 bindings, the `.proto`
definitions, the Kafka producer, the Redis cache tier and the Aurora persistence
adapter are named in the brief's stack and are **not** in this deliverable. Event
topic names and the `ColdStore` interface are the declared seams. Flagged so the
gap is a decision, not an omission.

### A4 · Compression cannot be exercised end to end

**Severity: informational, by design.** With no `Summarizer` implementation, the
compression path is proven with `ConcatSummarizer`. Selection, budget,
exemption precedence, classification inheritance and atomic replacement are all
tested; **the quality of a summary is not, and cannot be, tested in this phase.**
When a real summariser arrives, `TestIntegration_FailedSummarisationLeavesInputsIntact`
and the classification-inheritance tests transfer unchanged; nothing about
summary quality does.

### A5 · `Sweep` holds read locks while snapshotting

**Severity: low.** The metadata snapshot walks every shard under a read lock.
At 10,000 records this is 429 µs; at 1,000,000 it is ~43 ms of intermittent read
locking. `SweepBudget` caps the *apply* phase but not the snapshot. If the store
grows past ~500,000 records in one namespace, the snapshot should become
shard-at-a-time. Not fixed now: it would trade a simple, deterministic pass for
a partial view of the store, and there is no evidence yet that it is needed.

---

## 5 · Design decisions a reviewer should challenge

Listed because they are the places where a reasonable reviewer might land
elsewhere.

| Decision | The counter-argument |
|---|---|
| **Kind × Tier grid** instead of eleven flat types | The brief names eleven types; a reviewer could reasonably say the model should mirror the brief's vocabulary. Rebuttal: `KindOf` preserves the vocabulary, and eleven flat values makes every promotion rule an eight-way special case |
| **Memory does not import conversation** | A reviewer could argue conversation memory is the dominant case and should be privileged. Rebuttal: fraud, telephony and multi-agent all need memory and none needs dialogue |
| **Read path takes a write lock** | 1.7× contention for statistics. Rebuttal in PERFORMANCE §3: those statistics are what make promotion explainable |
| **Subject lookup unindexed** | O(n) erasure. Rebuttal: the alternative taxes every write for a query that runs on erasure |
| **Secret refused outright** | A deployment might want encrypted secrets in memory. Rebuttal: that is Identity's job, and a memory engine accepting credentials is one more place a token leaks from |
| **Derived memories do not auto-promote** | Reduces long-term recall quality. Rebuttal: permanently remembering something a person never said is a deployment choice, not a default |
| **Four "indexes" are one index with reserved attributes** | Less explicit. Rebuttal: four structures means four sets of removal bugs |

---

## 6 · Invariant enforcement

| # | Invariant | Enforced at | Test |
|---|---|---|---|
| INV-MEM-1 | Read returns a clone made under the lock | `Index.Get`, `Index.Touch` | `TestConsistency_CloneIsolatesCallers` |
| INV-MEM-2 | Personal/Sensitive require consent | `Policy.admit` **only** | `TestPolicy_PersonalDataRequiresConsent` |
| INV-MEM-3 | Secret never stored | `Record.validate` | `TestPolicy_SecretIsNeverStored` |
| INV-MEM-4 | Redaction destroys attributes too | `Store.Redact` | `TestIntegration_RedactDestroysIndexableAttributes` |
| INV-MEM-5 | Unscoped query refused | `Search`, `ContextBuilder.Build` | `TestRetrieval_UnscopedQueryIsRefused` |
| INV-MEM-6 | Derived not auto-promoted | `Policy.admit`, `Store.moveTier` | `TestPolicy_DerivedLongTermIsRefusedByDefault` |
| INV-MEM-7 | Attributes carry no Personal content | **Convention only** | — see SECURITY_REVIEW R1 |
| INV-MEM-8 | Oversized record refused | `Policy.admit` | `TestPolicy_OversizedRecordIsRefused` |
| INV-MEM-9 | Merge/split all-or-nothing | `Store.Merge`, `Store.Split` | `TestIntegration_MergeIsAllOrNothing` |
| INV-MEM-10 | Runtime starts once | `Runtime.Start` | `TestRuntime_StartStopIsClean` |
| INV-MEM-11 | Rollback never reverts legal hold | `Store.Rollback` | `TestRecovery_RollbackNeverRevertsLegalHold` |
| INV-MEM-12 | Erasure spans namespaces, reports survivors | `Coordinator.Forget` | `TestIntegration_ForgetSpansEveryNamespace` |

**INV-MEM-7 is the weak one.** It is a convention with no mechanical
enforcement — see SECURITY_REVIEW R1, where it is the top recommendation.

**INV-MEM-8 had no test until this audit found the gap.** The size cap was
enforced and unverified, which is the state a rule is in just before someone
refactors it away. `TestPolicy_OversizedRecordIsRefused` now covers it, and
fixing it surfaced a detail worth knowing: the cap applies to `Value.Size()`,
which counts the content type and attributes as well as the payload, so it
cannot be evaded by moving content into attributes.

Eleven of twelve are enforced by **absence**: a missing edge in the lifecycle
table, a missing field on `Event`, a missing constructor for a long-term
scratchpad. Enforcement by absence cannot be forgotten, misconfigured or
disabled in production.

---

## 7 · Test quality

| Property | Assessment |
|---|---|
| Behaviour vs implementation | Tests assert observable outcomes; none reaches into unexported state except the lifecycle-table well-formedness test, which is *about* that table |
| Failure injection | Summariser failure, encryptor failure, publisher failure, auditor absence, CAS conflict, budget exhaustion |
| Determinism asserted | Sweep ordering, retrieval ordering, split part ordering, and 25 end-to-end runs fingerprinted in `eval_test.go` |
| Outcome measurement | `eval_test.go` measures recall, scope leakage, promotion justification, erasure completeness, retention accuracy and context priority — and asserts each, so MEMORY_EVALUATION.md cannot drift from the code |
| Concurrency | Concurrent readers, writers, sweep-during-read, erasure-during-write |
| Clock | Every time-dependent test uses `FakeClock`; **no test sleeps** |
| Flakiness | None observed at `-count=5 -shuffle=on` |

**Gap:** no property-based or fuzz testing. The lifecycle table and
`matchesMeta` are both good fuzz targets — the table because reachability
properties are exactly what a fuzzer checks cheaply, `matchesMeta` because it is
a pure predicate over a wide input space. Recommended for 10D, not blocking.

**Gap:** no test asserts the *absence* of a subject index, so a future
contributor could add one and quietly make every write more expensive without a
test objecting. A benchmark regression gate would catch it; there is no CI
benchmark gate.

---

## 8 · Verdict

**APPROVE WITH ONE BLOCKING FINDING.**

The engine meets the brief: from scratch, provider agnostic, no memory
framework, no embeddings, no vector store, no LLM summarisation, serving five
consumers through isolated namespaces with a coordinator that makes erasure
whole. Determinism, consent-at-write and clone-under-lock are enforced
structurally rather than by discipline. Performance is three orders of magnitude
inside the frozen latency budget.

**A2 blocks production, not approval of the phase.** Three concurrent modules
have now been built without ever running the race detector. That must be a CI
job before any of this handles a real call — and it is the one finding here
that cannot be closed by reasoning about the code.

Second priority: **A1**, because it is now the third occurrence of the same
frozen-interface problem, and each phase that ships around it makes the eventual
correction larger.

### Handover to Phase 10D

| Item | Action |
|---|---|
| A2 | CI job, Linux, cgo, `-race -count=5` across all three modules |
| A1 | Superseding ADR to export `runtime` metric constructors; delete two duplicate sets |
| A3 | Kafka producer, Aurora `ColdStore`, Redis tier, `.proto`, Python bindings |
| A5 | Shard-at-a-time sweep snapshot if a namespace passes ~500,000 records |
| §7 gaps | Fuzz the lifecycle table and `matchesMeta`; add a benchmark regression gate |
