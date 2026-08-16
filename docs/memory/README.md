# Enterprise Memory Engine — Documentation

**Phase 10C** · `packages/go/memory` · Status: **PROPOSED — awaiting approval**

The permanent memory layer for the platform. Built from scratch on the Go
standard library and the frozen Phase 10A runtime — **no memory framework, no
vector database, no embeddings, no LLM summarisation.**

---

## Documents

| # | Document | What it answers |
|---|---|---|
| 1 | [MEMORY_ARCHITECTURE.md](MEMORY_ARCHITECTURE.md) | What the engine is, how it is shaped, and why — Kind × Tier, thirteen operations, seven indexes, twelve invariants |
| 2 | [MEMORY_LIFECYCLE.md](MEMORY_LIFECYCLE.md) | How a memory is admitted, promoted, compressed and removed |
| 3 | [MEMORY_STATE_DIAGRAM.md](MEMORY_STATE_DIAGRAM.md) | The seven states and every transition — including the two that deliberately do not exist |
| 4 | [MEMORY_RETRIEVAL_FLOW.md](MEMORY_RETRIEVAL_FLOW.md) | The three read paths and how context is assembled under budget |
| 5 | [ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md) | Compliance with the brief, five defects found and fixed, five open findings |
| 6 | [PERFORMANCE.md](PERFORMANCE.md) | 21 benchmarks, the one optimisation made, and five refused |
| 7 | [SECURITY_REVIEW.md](SECURITY_REVIEW.md) | Threat model, fourteen controls, eight findings, DPDP alignment |
| 8 | [MEMORY_EVALUATION.md](MEMORY_EVALUATION.md) | Does it remember and forget the right things — measured, not asserted |

---

## The short version

**One dependency**, first-party (`packages/go/runtime`). Zero external packages.

**Eight kinds × three tiers**, not eleven flat types — the brief's eleven
"memory types" are two orthogonal ideas wearing one name.

**Four isolated namespaces** (assistant, receptionist, fraud, telephony) plus
on-demand ones, with a coordinator that exists so **erasure cannot miss a
namespace**.

**Consent refused at write**, Secret refused outright, events that carry
identifiers and have no field capable of holding content.

**Deterministic**: 25 identical runs produce 25 identical memories.

**~65 µs of memory work per conversational turn** — 0.007% of the frozen 900 ms
p50 budget.

---

## Verification

```
cd packages/go/memory
go vet .                              # clean
gofmt -l .                            # clean
go test -count=5 -shuffle=on .        # 77 tests
go test -run TestEvaluation -v .      # the numbers in MEMORY_EVALUATION.md
go test -bench=. -benchmem .          # the numbers in PERFORMANCE.md
```

**Not verified: `-race`** — requires cgo, and there is no C toolchain on the
development machine. This is the one blocking finding
([ENGINEERING_AUDIT §A2](ENGINEERING_AUDIT.md)) and now applies to three
concurrent modules.

---

## Before production

| | Finding |
|---|---|
| 1 | `-race` in CI across runtime, conversation and memory — **A2** |
| 2 | Enforce that indexed attributes carry no Personal content — **R1** |
| 3 | A real KMS-backed `Encryptor` — **R2** |
| 4 | Durable `ColdStore`, Kafka producer, Redis tier — **A3** |
| 5 | Rollback barrier across a completed erasure — **R4** |

---

## Frozen artifacts

Phase 10A (`packages/go/runtime`) and Phase 10B (`packages/go/conversation`) are
**unmodified**. `go.work` gained one line for this module; all three build.
