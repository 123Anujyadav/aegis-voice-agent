# Enterprise Memory Engine — Architecture

**Phase 10C** · `packages/go/memory` · Status: **PROPOSED — awaiting approval**

The permanent memory layer for the platform, built from scratch on the Go
standard library and the frozen Phase 10A runtime.

---

## 1 · What this is

| It is | It is not |
|---|---|
| A typed, indexed, policy-governed store with an explicit lifecycle | A vector database |
| Provider-agnostic by construction | Built on LangChain Memory, LlamaIndex, Mem0, Zep, Chroma, Pinecone, Weaviate or Milvus |
| Serving assistant, receptionist, fraud, telephony and future agents | A conversation feature |

**No embeddings, no semantic search, no LLM summarisation, no prompt
templates.** Where such a thing is needed the engine defines an interface it
does not implement — `Summarizer` and `Encryptor` are the clear cases, and
neither has an implementation anywhere in the module.

---

## 2 · Position in the platform

```
   ┌──────────────────┐   ┌──────────────────┐   ┌──────────────────┐
   │  conversation    │   │  fraud           │   │  telephony       │
   │  (10B, frozen)   │   │  intelligence    │   │  intelligence    │
   └────────┬─────────┘   └────────┬─────────┘   └────────┬─────────┘
            │                      │                      │
            └──────────┬───────────┴──────────────────────┘
                       │ orchestration wires them
            ┌──────────▼───────────┐
            │  memory   (10C)      │   PEER of conversation, not a child
            └──────────┬───────────┘
                       │ imports
            ┌──────────▼───────────┐
            │  runtime  (10A)      │   Clock · FSM · identifiers
            └──────────┬───────────┘
                       │
                ┌──────▼───────┐
                │  Go stdlib   │
                └──────────────┘
```

**Memory does not import the conversation engine, deliberately.** Fraud,
telephony and a future multi-agent runtime all need memory and none of them
needs dialogue machinery. A dependency the other way would make "conversation
memory" the privileged case rather than one kind among eight.

**One dependency, first-party.** `go list -deps` reports zero external packages.

---

## 3 · The central modelling decision: Kind × Tier

The brief lists eleven "memory types". Read carefully, they are **two orthogonal
ideas wearing one name**:

| Named type | Actually a… |
|---|---|
| WorkingMemory, ShortTermMemory, LongTermMemory | **Tier** — lifetime |
| Conversation, Session, User, Business, Preference, Contact, Scratchpad, Policy | **Kind** — subject matter |

Modelling them as one enum makes "a long-lived user preference" and "a scratchpad
note" inexpressible as different things, and forces every promotion rule to
special-case eight values.

Here they are a **grid: 8 kinds × 3 tiers.** The eleven named types are
constructors over it (`NewPreference`, `NewScratchpad`, …), and `KindOf` maps
the brief's vocabulary onto the model.

```
              TierWorking      TierShortTerm     TierLongTerm
Conversation      ·                 ·                 ·
Session           ·                 ·                 ·
User              ·                 ·                 ·      ← NewUser
Business          ·                 ·                 ·
Preference        ·                 ·                 ·      ← NewPreference (pinned)
Contact           ·                 ·                 ·
Scratchpad     NewScratchpad      (refused)         (refused)
Policy            ·                 ·                 ·      ← NewPolicy (pinned, legal hold)
```

Two cells are structurally refused: a scratchpad above the working tier is a
contradiction, and `Record.validate` rejects it.

---

## 4 · Subsystems

| Brief section | Type | File |
|---|---|---|
| 1 · Memory Runtime | `Runtime`, `Coordinator`, `Registry`, `Dispatcher` | `runtime.go`, `events.go` |
| 2 · Memory Types | `Kind`, `Tier`, `Record`, eleven constructors | `record.go` |
| 3 · Memory Operations | `Store` (13 operations) | `store.go` |
| 4 · Memory Lifecycle | `State`, `lifecycleTable`, `PromotionPolicy` | `lifecycle.go` |
| 5 · Memory Index | `Index` (7 lookups) | `index.go` |
| 6 · Memory Retrieval | `Retriever`, `Query`, `Result` | `retrieval.go` |
| 7 · Memory Compression | `Compressor`, `Summarizer`, `TokenBudget` | `compression.go` |
| 8 · Memory Policies | `Policy`, `RetentionPolicy`, `ConsentChecker`, `Auditor`, `Encryptor` | `policy.go` |
| 9 · Memory Consistency | `Version`, `Update` CAS, `Snapshot`, `Rollback` | `store.go` |
| 10 · Context Builder | `ContextBuilder`, `ContextScope` | `contextbuilder.go` |
| 11 · Memory Events | `Event`, `EventType`, `Publisher` | `events.go` |
| 12 · Memory Metrics | `Metrics` | `metrics.go` |
| 13 · Testing | `Harness` and doubles | `harness.go` |

---

## 5 · The thirteen operations

| Operation | Notes |
|---|---|
| **Store** | Sets timestamps from the engine's clock, never the record's. Assigns version 1 or increments |
| **Retrieve** | Distinguishes **four** negative outcomes — see below |
| **Update** | Compare-and-swap on version; returns a `*ConflictError` carrying both |
| **Delete** | Leaves a tombstone so a key is never silently reused |
| **Forget** | Subject erasure honouring legal hold, reporting exactly what survived |
| **Expire** | Marks expired without reclaiming, so a raised retention preference can revive |
| **Merge** | All-or-nothing; inherits the **strictest** classification of its inputs |
| **Split** | Deterministic part ordering; source removed only on success |
| **Compress** | Framework only — selection, budget, preservation. No summarisation |
| **Snapshot** | Subject-scoped, not global |
| **Restore** | Rollback that never reverts a legal hold |
| **Archive** | Models the state; `ColdStore` is the unimplemented destination |
| **Redact** | Destroys payload **and indexed attributes**, retains existence |

### Retrieve distinguishes four failures

`ErrNotFound` · `ErrExpired` · `ErrArchived` · `ErrRedacted`

"Never existed", "aged out", "in cold storage" and "destroyed on purpose" are
four different facts. Collapsing them into one nil is the most common way a
memory layer becomes impossible to reason about — a caller cannot tell a bug
from a policy outcome.

---

## 6 · Index layer — seven lookups, three structures

**No vector index. No embedding.** The access patterns this platform has are
exact match, reference lookup and time range; none is a nearest-neighbour
problem.

| Logical index | Physical structure | Cost |
|---|---|---|
| Primary | Sharded map, 16 stripes | 287 ns |
| Secondary | attribute → value → keys | 682 ns |
| Conversation, Session, Contact, Business | **The same secondary index**, over four reserved attribute names | 682 ns |
| Time | Bucketed by minute | 7.0 µs |
| Subject / Kind | **Unindexed full scan** | 29.8 µs / 1,000 records |

**The four "dedicated" indexes are one index with four reserved keys.** Rather
than four bespoke structures with four sets of maintenance bugs, a record joins
an index by carrying an attribute — and nothing is inferred, because the engine
never parses a payload to discover a reference.

### Why the time index is bucketed

A sorted structure gives O(log n) lookup and O(n) insert. At this engine's write
rate the insert dominates. Bucketing gives O(1) insert and a range query that
scans only overlapping buckets — which is exactly the query it serves.

### Why subject lookup is deliberately unindexed

A subject index would be maintained on **every write** to serve a query used
only by erasure and diagnostics, neither of which is latency-sensitive. Paying
on every write for a query that runs on erasure is the wrong trade. The cost is
measured and reported in PERFORMANCE §3.

---

## 7 · Lifecycle

Seven states, every transition declared in one table. Full diagram:
[MEMORY_STATE_DIAGRAM.md](MEMORY_STATE_DIAGRAM.md).

Three properties **asserted by test**, not inspection:
every state reachable from Active · every non-terminal state can reach Deleted ·
Deleted has no outgoing edges.

Two absences carry weight:

| Absent edge | Why |
|---|---|
| `Redacted → Active` | Reviving a redacted record would recover content destroyed on purpose |
| `Corrupt → Active` | Silently repairing a record that failed an integrity check makes corruption indistinguishable from truth |

And one presence: `Expired → Active` exists, because a subscriber raising their
retention preference should not have destroyed data that has not yet been
reclaimed.

---

## 8 · Promotion — explicit, never heuristic

A memory layer whose promotion rules are implicit produces a system nobody can
explain. **"Why does it remember that about me" must be answerable with a rule.**

| Threshold | Default | Derivation |
|---|---|---|
| `AccessesToPromote` | 3 | A record read three times inside one 20–40 s screening is load-bearing |
| `MinAgeToPromote` | 5 s | Stops a burst of reads promoting something never read again |
| `IdleToDemote` | 10 min | |
| `IdleToExpire` | 30 s | Spans a whole conversation |
| `PromoteDerived` | **false** | See below |

`PromotionPolicy.Evaluate` is a **pure function** of record, policy and instant —
which is what makes the ladder exhaustively table-testable and a memory's
history reproducible.

### Derived memories do not reach long term by default

An inferred memory promoted to permanence is the platform deciding, on its own,
to permanently remember something a person never told it. That is a deliberate
deployment choice, not a default — and the guard sits on **both** the promotion
path and the write path, because a caller could otherwise create a long-term
derived record directly and bypass the ladder entirely.

---

## 9 · Policy and DPDP

### INV-MEM-2 — consent is structural

**A Personal or Sensitive record cannot be stored without a valid consent
reference.** Refused at write, so an unlawful memory is never created and then
detected by an audit.

The check is in `Policy.admit` and *only* there. It was briefly in
`Record.validate` as well, and that duplication meant the structural error
shadowed the typed `ErrConsentRequired` sentinel — see ENGINEERING_AUDIT §F1.

### Classification

| Sensitivity | Requires consent | Requires audit | Storable |
|---|---|---|---|
| Public, Internal | No | No | Yes |
| Personal | **Yes** | No | Yes |
| Sensitive | **Yes** | **Every read** | Yes |
| Secret | — | — | **Never** |

**Secret is refused outright.** A memory engine that accepted credentials would
be one more place a token can leak from; authentication material belongs in
Identity under its own handling.

### Retention

Classes mirror the frozen `annotations.proto`. The standard duration is **90
days, matching ADR-0012's transcript retention** rather than chosen
independently — a memory layer outliving the transcripts it summarises would be
a second, unregulated copy of the same personal data.

`RetentionLegalHold` deliberately has **no duration**. Giving it one is a way to
accidentally delete the records that must be kept, and `validate` refuses a
configuration that tries.

### Audit

Only **Sensitive** reads are audited. Auditing every Internal read would bury
the entries that matter, and an audit log nobody can read is not a control.

`RequireAuditor` defaults **true**: an engine holding Sensitive data with no
audit trail cannot answer "who read this", which is an obligation rather than a
nicety.

---

## 10 · Consistency

**Compare-and-swap, not last-write-wins.** Two subsystems updating one memory —
the assistant and a sync job — would otherwise silently lose one write, and a
lost memory update is invisible until someone notices the system forgot
something.

`ConflictError` carries both versions and a `Stale()` count, so a caller can
choose between retry, merge and abandon rather than being told only that
something changed.

Every read returns a **clone**, produced **under the shard lock**. Returning a
live pointer would let a caller mutate a record without a version bump, defeating
optimistic locking and every audit guarantee that rests on it. That was a real
latent race — see ENGINEERING_AUDIT §F2.

---

## 11 · Namespaces and the coordinator

Four namespaces ship: `assistant`, `receptionist`, `fraud`, `telephony`. More
are created on demand for a multi-agent runtime.

**Isolation is by construction.** A fraud agent cannot reach the assistant's
memory by guessing a key — the same `Key` in two namespaces holds two different
records.

The **Coordinator** exists for one reason that matters: **erasure must span
every namespace.** A subject's memories are spread across four stores, and an
erasure reaching only one would be a compliance failure that looks like a
success. No caller has to remember the full list.

---

## 12 · Context builder

Six scopes, filled in **priority order** under a token budget:

| Priority | Scope | Sourced from |
|---|---|---|
| 0 | Runtime | Policy |
| 1 | Personal | Preference, User |
| 2 | Business | Business |
| 3 | Conversation | Conversation |
| 4 | Shared | Contact |
| 5 | Temporary | Scratchpad |

**Priority is the interesting column.** Under pressure, what survives is what a
caller can least afford to lose: an assistant that forgets an operating limit is
dangerous, one that forgets a stated preference is rude, one that forgets the
last three exchanges is merely forgetful.

Within a scope, records are admitted until one does not fit; the scope then
stops and reports `Truncated` with `Available` stating how many matched. A scope
for which nothing fits is named in `Dropped`. **Truncation is permitted;
silence about it is not** — a caller must be able to tell a short context from a
complete one. Measured behaviour under a starved budget is in
[MEMORY_EVALUATION.md](MEMORY_EVALUATION.md) §E6: at 25% of demand, runtime,
personal and business scopes survived whole and conversation history absorbed
the entire cut.

The builder **writes nothing**. Assembling context must never mutate what it
assembles, or two concurrent builds on one subject would each change what the
other sees.

---

## 13 · Events

Eight types, topic-named per the frozen `packages/go/eventbus` convention:
`memory.record.<event>.v1`.

**Events carry identifiers, not content** (frozen invariant I7). There is
deliberately no field that could hold a payload. The test applied to `Event`:
*if this topic were retained forever and could never be deleted, would that be a
compliance failure?* It must be no — and a test asserts no payload string
appears in any published event.

A failing publisher is **counted and skipped**. One broken subscriber must not
fail a memory write for everyone else.

---

## 14 · Invariants

| # | Invariant | Enforced by |
|---|---|---|
| **INV-MEM-1** | Every read returns a clone produced under the shard lock | `Index.Get`, `Index.Touch` |
| **INV-MEM-2** | Personal and Sensitive records require a valid consent reference | `Policy.admit` |
| **INV-MEM-3** | Secret material is never stored | `Record.validate` |
| **INV-MEM-4** | Redaction destroys payload **and** indexed attributes | `Store.Redact` |
| **INV-MEM-5** | A query must be scoped; an unscoped query is refused | `Search`, `ContextBuilder.Build` |
| **INV-MEM-6** | Derived memories do not reach the long-term tier by default | `Policy.admit`, `Store.moveTier` |
| **INV-MEM-7** | Indexed attributes carry no Personal content | Convention — see SECURITY_REVIEW R1 |
| **INV-MEM-8** | A record over `MaxRecordBytes` is refused | `Policy.admit` |
| **INV-MEM-9** | Merge and split are all-or-nothing | `Store.Merge`, `Store.Split` |
| **INV-MEM-10** | A runtime starts once | `Runtime.Start` |
| **INV-MEM-11** | Rollback never reverts a legal hold | `Store.Rollback` |
| **INV-MEM-12** | Erasure spans every namespace and reports what survived | `Coordinator.Forget` |

Most are enforced by **absence** — a missing edge, a missing field, a missing
constructor. Enforcement by absence cannot be forgotten or misconfigured.

---

## 15 · Determinism

| Source of nondeterminism | Removed by |
|---|---|
| Wall clock | Everything goes through `rt.Clock` |
| Map iteration | Sorted: attribute names, subject scans, split parts, sweep actions, namespaces |
| Result ordering | Every comparator falls back to the key |
| Posting-list order | `removeKey` preserves order rather than swap-removing |

Asserted by `TestRuntime_SweepIsDeterministic` and
`TestRetrieval_OrderIsDeterministic`.

---

## 16 · Concurrency

| Structure | Strategy |
|---|---|
| Primary index | 16 sharded RWMutex stripes |
| Secondary + time | One RWMutex — written and read together, so separate locks would mean ordered acquisition everywhere and a deadlock class that does not exist today |
| Read path | Shard **write** lock, because a read updates access statistics — measured cost in PERFORMANCE §3 |
| `Registry` | Copy-on-write |
| `Metrics` | RWMutex on labels, atomics per series |

No global mutable state. Two runtimes in one process share nothing.

---

## 17 · Testing

| Suite | Count |
|---|---|
| Unit (`memory_test.go`) | **39** |
| Integration, stress, recovery, failure injection, load (`integration_test.go`) | **30** |
| Evaluation (`eval_test.go`) — measures recall, scope leakage, promotion justification, erasure completeness, retention accuracy, context priority, determinism | **8** |
| Benchmarks (`bench_test.go`) | **21** |

**77 tests, 8,434 lines** (5,524 source, 2,910 test). `go vet` clean · `gofmt`
clean · passes `-count=5 -shuffle=on` · workspace builds with 10A and 10B
intact.

The evaluation suite is the one that produces
[MEMORY_EVALUATION.md](MEMORY_EVALUATION.md)'s numbers: it asserts as well as
measures, so a change that degrades recall or leaks a scope fails a test rather
than quietly invalidating a document.

**Not verified: `-race`** — no C toolchain locally. Tracked as
ENGINEERING_AUDIT §A2 and blocking.

---

## 18 · Deliberate omissions

Per the brief's DO NOT IMPLEMENT list, verified absent:

| Excluded | Verified |
|---|---|
| LLM memory / summarisation | `Summarizer` has no implementation; `ConcatSummarizer` is a test double that concatenates |
| Embeddings, vector DB, semantic search | No vector type, no distance function, no similarity anywhere |
| Prompt templates | None |
| Conversation behaviour | No import of 10B; `KindConversation` is a label |
| Telephony logic | No import, no type |
| Fraud logic | `NamespaceFraud` is a namespace name |
| Business workflows | `KindBusiness` is a label |

Also absent by design: config hot-reload, a global registry, and any persistence
adapter — `ColdStore` is an interface this module does not implement.
