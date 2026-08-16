# Observability Audit — Phase 10.5

**Scope:** the eight AI-plane modules.
**Verdict:** metrics are now production-grade. Logging is sound. The tracing
port exists but **one span is emitted across the whole AI plane**, and
correlation is carried by two of six subsystems using two unrelated types.

---

## 1. Metrics — **resolved this phase**

Was the largest gap and is now the strongest area.

| | Before | After |
|---|---|---|
| Implementations | 6 | 1 |
| `Sample` shapes | 3 | 1 |
| Subsystems that can export a histogram | **3 of 6** | 6 of 6 |
| Instrument name collisions across a merged scrape | unknown | 0 of 183 |

Half the platform emitted histograms as a synthetic `name_count` counter with no
bounds, no cumulative buckets and no sum. A scraper could not recover a
percentile or an average from governance, tool runtime or evaluation. Full
detail in [METRICS_MIGRATION_REPORT.md](METRICS_MIGRATION_REPORT.md).

Enforced by `TestObservability_*` in `packages/go/evalsubjects` — the only module
importing all five engines, and therefore the only place a cross-subsystem
property can be checked by a compiler instead of asserted in a document.

```
runtime      54 instruments,  8 histogram series exported in full
conversation 61 instruments, 11 histogram series exported in full
memory       59 instruments,  5 histogram series exported in full
toolruntime  67 instruments,  5 histogram series exported in full
governance   53 instruments,  3 histogram series exported in full
evaluation   37 instruments,  3 histogram series exported in full
```

**Not yet done:** nothing exports a `Snapshot` to Prometheus or OTLP. The data
is now reachable and uniform; the adapter that publishes it does not exist. That
is deliberate — an instrument package that acquires a network dependency would
forfeit the zero-dependency property the whole AI plane rests on — but it means
**no metric currently leaves the process**. See O4.

---

## 2. Logging — sound

`log/slog` in all six engines, injected rather than global, with scoped
loggers carrying domain identifiers:

```go
logger: e.logger.With(slog.String("conversation_id", string(id)))
```

Structured, level-aware, and a nil logger is handled rather than panicking.
Consistent with the platform's rule that nothing reaches for a package-level
singleton.

No finding.

---

## 3. Tracing — the port exists; almost nothing uses it

The runtime defines a proper tracing port, and it is well shaped:

```go
type Tracer interface {
    Start(ctx context.Context, name string) (context.Context, Span)
}
type Span interface {
    SetAttribute(string, any); RecordError(error); End()
}
```

with `NoopTracer` as the default and `WithTracer` to inject a real one — so the
runtime works with no collector configured, which is what keeps it unit-testable
offline. This is the same port-and-adapter shape the metrics package took, and
it is correct.

**The problem is adoption, not design.** Exactly one span is created in the
entire AI plane:

```console
$ grep -rn 'tracer.Start\|Tracer().Start' --include=*.go \
    runtime conversation memory toolruntime governance evaluation | grep -v _test
runtime/kernel.go:537:  ctx, span := k.tracer.Start(ctx, "runtime.generate")
```

| Subsystem | Spans emitted |
|---|---:|
| runtime | 1 (`runtime.generate`) |
| conversation | 0 |
| memory | 0 |
| toolruntime | 0 |
| governance | 0 |
| evaluation | 0 |

No OpenTelemetry adapter exists either, so even that one span goes to a no-op.

Note also that "trace" means three different things in this codebase and only
one of them is a span: `conversation.Trace()` is a state-transition history and
`governance` produces a decision trace explaining a policy outcome. Both are
valuable audit artifacts; neither is telemetry.

`packages/go/telemetry` declares a **second, rival** `Tracer` interface with a
different signature, a no-op implementation and an `InitializeOpenTelemetry`
that returns it. It is referenced by zero modules and is 31 lines. It should be
deleted or turned into the adapter for `runtime.Tracer` — see
[REPOSITORY_AUDIT.md](REPOSITORY_AUDIT.md) F1.

### O1 — One span across five engines *(high, open)*

A production call traverses conversation → governance → tool runtime → memory.
Four of those emit nothing, so nothing links the operations into one trace. An
incident investigation reconstructs the path by correlating timestamps across
four log streams.

The remaining work is smaller than it first appears: the port is defined, the
injection point exists, and each engine already takes a runtime kernel. What is
missing is span creation at each engine's entry point, plus one adapter.

**Why this is not fixed here:** adding spans to five frozen engines is a change
to their call paths, not a hardening fix, and the exporter that would make the
spans visible needs `go.opentelemetry.io/otel` — which cannot go in the AI
plane without forfeiting the zero-dependency property Phase 10A argued for at
length. The adapter belongs in a module outside the AI plane, exactly as the
metrics exporter does.

**Recommendation:** instrument each engine entry point against the existing
`runtime.Tracer`, and write one OTel adapter outside the AI plane. No new port
is needed — that work was already done.

---

## 4. Correlation identifiers — partial, and inconsistent

| Subsystem | Session ID | Correlation ID |
|---|---|---|
| runtime | `SessionID`, `RequestID`, `StreamID` | **none** |
| conversation | conversation ID | **none** |
| memory | — | **none** |
| toolruntime | `SessionID` | `CorrelationID` |
| governance | `SessionID` | `CorrelationID` |
| evaluation | — | n/a (offline) |

Both `CorrelationID` types are documented identically —

> ties every execution arising from one conversation turn

— and both are declared independently, in `toolruntime/ids.go` and
`governance/ids.go`. **`packages/go/runtime` defines no `CorrelationID` at
all**, so there is no shared ancestor.

### O2 — Two unrelated `CorrelationID` types *(medium, open)*

They are distinct Go types in distinct packages. Passing a tool runtime
correlation into a governance request requires
`governance.CorrelationID(string(tc))`. The conversion is legal, silent, and
exactly where a mismatch will one day be introduced — the type system cannot
tell you that the two IDs mean the same thing, because as far as it knows they
do not.

The natural home is `packages/go/runtime`, alongside `SessionID` and
`RequestID`, which every AI module already imports.

### O3 — Three subsystems drop correlation entirely *(high, open)*

Conversation, memory and the runtime core carry no correlation ID. So even once
a tracing backend exists, **an end-to-end trace cannot be assembled**: the chain
breaks at the first hop, because the conversation engine has nothing to
propagate into governance.

The governance field comment says the ID is "carried for audit and trace
assembly". Nothing assembles a trace, and half the participants do not carry the
identifier that would let it.

**Recommendation, in order:** (1) move `CorrelationID` into `runtime`; (2) thread
it through conversation and memory; (3) then add tracing. Doing (3) first
produces traces with three of six subsystems missing.

---

## 5. Cross-module observability

### O4 — No exporter exists *(high, open)*

Every subsystem can now produce a complete, uniform `[]Sample`. Nothing
converts one into a scrape endpoint or an OTLP push.

This is a small piece of work with a large payoff and it is genuinely blocked on
nothing: `Sample` carries `Kind`, `Name`, `Labels`, `Value`, `Bounds`,
`Buckets`, `Count` and `Sum`, which is precisely the Prometheus exposition
model. One adapter in a module outside the AI plane, written once, now handles
all six subsystems — which is the concrete dividend of the migration, and was
impossible a week ago because a single adapter would have needed a branch per
subsystem and three of the branches would have had no data to read.

### O5 — No health or readiness surface in the AI plane *(low)*

`packages/go/platform` has `health.go`, used by services. The AI engines expose
no equivalent — no "is this engine's policy registry loaded", "is the golden
store reachable". For in-process libraries that is defensible; it becomes a gap
when they are hosted.

---

## 6. Audit trails — strong

Distinct from telemetry and worth separating, because these are correctness
evidence rather than diagnostics:

- **Governance** produces a decision trace per decision: which policies matched,
  in which scope, why the outcome was reached.
- **Tool runtime** keeps an execution ledger.
- **Evaluation** now writes a content-free retention audit trail — deletions,
  sweeps, legal holds, migrations, restores — retained indefinitely and exempt
  from its own sweep, because deleting the record of what was deleted destroys
  the evidence that retention was honoured.

All three are content-free by construction, consistent with frozen invariant I7.

---

## 7. Findings

| ID | Finding | Severity | Status |
|---|---|---|---|
| — | Metric duplication and unexportable histograms | high | **resolved** |
| O1 | One span across five engines; no OTel adapter | high | open |
| O2 | Two unrelated `CorrelationID` types, none in `runtime` | medium | open |
| O3 | Conversation, memory and runtime carry no correlation ID | high | open |
| O4 | No metrics exporter — nothing leaves the process | high | open, unblocked |
| O5 | No health surface in the AI plane | low | open |

**The single most valuable next step is O4.** It is the smallest of the five and
it converts the metrics work from "correct in principle" into "visible in
production".

---

## 8. Related

- [METRICS_MIGRATION_REPORT.md](METRICS_MIGRATION_REPORT.md)
- [SECURITY_HARDENING_REPORT.md](SECURITY_HARDENING_REPORT.md)
