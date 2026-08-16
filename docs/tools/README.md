# Enterprise Tool Calling Runtime — Documentation

**Phase 10D** · `packages/go/toolruntime` · Status: **PROPOSED — awaiting approval**

The component that turns a stated intent into a bounded, permitted, audited,
compensable execution. Built from scratch — **no orchestration framework, no
vendor tool protocol, no real adapters.**

---

## Documents

| # | Document | What it answers |
|---|---|---|
| 1 | [TOOL_RUNTIME.md](TOOL_RUNTIME.md) | What the runtime is and why it is shaped this way — capabilities over tools, inert plans, effect classification, twelve invariants |
| 2 | [EXECUTION_FLOW.md](EXECUTION_FLOW.md) | Intent to outcome, the five plan shapes, load shedding, cancellation, streaming |
| 3 | [TOOL_LIFECYCLE.md](TOOL_LIFECYCLE.md) | The five registration stages, health, selection order, breakers |
| 4 | [SEQUENCE_DIAGRAMS.md](SEQUENCE_DIAGRAMS.md) | Nine sequences, each one a case that shaped a decision |
| 5 | [ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md) | Compliance, five defects found and fixed, six open findings |
| 6 | [PERFORMANCE.md](PERFORMANCE.md) | 22 benchmarks, three defects found by benchmarking, six optimisations refused |
| 7 | [SECURITY_REVIEW.md](SECURITY_REVIEW.md) | Threat model, fourteen controls, eight findings, twenty attack scenarios |
| 8 | [EXECUTION_EVALUATION.md](EXECUTION_EVALUATION.md) | Does it execute, refuse and record the right things — measured |

---

## The short version

**Intents name capabilities, not tools.** That single decision is what makes
provider agnosticism structural: a capability request is not expressible in any
vendor's function-calling wire format, so translating one is an adapter's job
and this module has never heard of a provider.

**One dependency**, first-party (`packages/go/runtime`). Zero external packages.

**It cannot write memory**, because it does not import it. The rule is a fact
about the build.

**Plans are inert.** Building one executes nothing, so a plan can be reviewed
before it runs and compared after.

**Irreversible effects are never retried**, never fallen back over, and cannot
be declared compensable.

**Idempotency keys are derived, never supplied.** 64 concurrent identical
requests produce 1 tool invocation and 64 answers.

**~20 µs of runtime overhead per execution** — 0.0023% of the frozen 900 ms p50
turn budget.

---

## Verification

```
cd packages/go/toolruntime
go vet .                              # clean
gofmt -l .                            # clean
go test -count=5 -shuffle=on .        # 87 tests
go test -run TestEvaluation -v .      # the numbers in EXECUTION_EVALUATION.md
go test -run XXX -bench=. -benchmem . # the numbers in PERFORMANCE.md
```

**Not verified: `-race`** — requires cgo, and there is no C toolchain on the
development machine. This is the one blocking finding
([ENGINEERING_AUDIT §A2](ENGINEERING_AUDIT.md)) and now applies to four
concurrent modules.

---

## Before production

| | Finding |
|---|---|
| 1 | `-race` in CI across runtime, conversation, memory and toolruntime — **A2** |
| 2 | Out-of-process sandbox; today every tool must be trusted code — **R1** |
| 3 | Shared idempotency ledger for multi-replica writes — **R2** |
| 4 | Durable, append-only audit store — **R5** |
| 5 | Confirmation policy for irreversible plans — **R4** |

---

## Frozen artifacts

Phase 10A (`runtime`), 10B (`conversation`) and 10C (`memory`) are
**unmodified** and their suites pass. `go.work` gained one line for this module.
