# Repository Audit — Phase 10.5

**Scope:** the full monorepo, with depth on the eight AI-plane Go modules.
**Method:** every finding below was produced by running a command, and the
command is given so it can be re-run.

---

## 1. Module inventory

`go.work` declares **18 first-party packages** and **16 services**. The AI plane
is eight of the packages:

| Module | Lines | Tests | Benchmarks | Coverage |
|---|---:|---:|---:|---:|
| `packages/go/metrics` | 686 | 26 | 7 | 84.9% |
| `packages/go/runtime` | 6,047 | 53 | 21 | 72.8% |
| `packages/go/conversation` | 5,205 | 70 | 18 | 76.1% |
| `packages/go/memory` | 5,258 | 77 | 21 | 80.7% |
| `packages/go/toolruntime` | 8,512 | 87 | 22 | 76.2% |
| `packages/go/governance` | 7,063 | 82 | 20 | 77.1% |
| `packages/go/evaluation` | 7,691 | 94 | 22 | 75.9% |
| `packages/go/evalsubjects` | 1,458 | 23 | — | 71.2% |
| **Total** | **41,920** | **512** | **131** | 71.2–84.9% |

---

## 2. Duplicate code

### D1 — Metric instruments, six copies *(RESOLVED this phase)*

The A1 finding, outstanding since Phase 10B and the first handover item in four
consecutive audits.

```console
$ for m in runtime conversation memory toolruntime governance evaluation; do
    wc -l < packages/go/$m/metrics.go; done
524 492 487 529 508 488     # 3,028 lines
```

Resolved by `packages/go/metrics`. See
[METRICS_MIGRATION_REPORT.md](METRICS_MIGRATION_REPORT.md).

**A correction to the earlier audits.** They stated the risk as "six independent
histogram implementations will not agree on bucket boundaries". Measured, the
`Observe` and `Quantile` bodies were semantically identical, and the bucket
boundaries differ **correctly** — governance decides in hundreds of nanoseconds,
a conversation turn takes seconds, and a shared bucket set would have been
actively wrong. The real defect was elsewhere and worse; §2.1 below.

### D1.1 — The scrape surface actually diverged

Three different `Sample` shapes:

| Modules | `Sample` fields | Histogram in `Snapshot()` |
|---|---|---|
| runtime | Name, Labels, Value, **Buckets, Count, Sum** | complete |
| conversation, memory | Name, Labels, Value, Count, Sum | count + sum, **no buckets** |
| toolruntime, governance, evaluation | Name, Labels, Value | **`name_count` only** |

Half the platform could not export a histogram at all. Governance computed
latency percentiles in-process that no scraper could reach, and the
cross-subsystem latency dashboard the SLOs depend on was **not buildable** —
not "inconsistent", not buildable.

Now uniform, and checked by `TestObservability_EverySubsystemExportsHistogramData`
in `evalsubjects`, the only module that imports all five engines and can
therefore check the property with a compiler rather than a document.

### D2 — Three canonical value encoders *(open, and costlier than it looks)*

`toolruntime/value.go`, `governance/action.go` and `evaluation/value.go` each
implement canonical encoding with sorted map keys.

Earlier audits recommended extracting `packages/go/canonical`. **That
recommendation is not free, and the cost was not previously stated.** The
encoders write `'0' + kind` as their first byte, and the kind enums differ:

| Logical value | toolruntime | governance | evaluation |
|---|---|---|---|
| string | `'1'` | `'0'` | `'1'` |
| number | `'2'` (int) / `'3'` (float) | `'1'` | `'2'` |
| bool | `'4'` | `'2'` | `'3'` |

The same logical value therefore fingerprints differently in each package.
Unifying them **changes fingerprints in at least two of the three**, which
invalidates:

- every approved golden in the evaluation platform (keyed on behaviour prints)
- every idempotency key in the tool runtime (`Arguments.Fingerprint`)
- every governance decision fingerprint

**Revised recommendation:** unification must be paired with either a
fingerprint-version tag on stored records or a full golden re-approval cycle. It
is a migration, not a refactor, and it should not be attempted in a hardening
phase whose rule is "no breaking changes". Deferred with the cost now known.

---

## 3. Dead code and unused packages

Five first-party packages are referenced by **zero** other modules:

```console
$ for d in $(grep -oE '\./packages/go/[a-z0-9/_-]+' go.work | sed 's|^\./||'); do
    name=$(basename $d)
    echo "$name $(grep -rl "packages/go/$name" --include=go.mod . | grep -v "$d/go.mod" | wc -l)"
  done
```

| Package | Files | Lines | Assessment |
|---|---:|---:|---|
| `contracts-go` | 3 | 0 | **Not dead.** Generated protobuf/Connect output; empty until `task contracts:generate` runs. Correct as-is. |
| `middleware` | 1 | 130 | Unreferenced. Written ahead of the services that will use it. |
| `telemetry` | 1 | 31 | **Dead, and now misleading** — see F1. |
| `persistence` | 1 | 122 | Unreferenced. |
| `integrations` | 2 | 247 | Unreferenced. |

### F1 — `packages/go/telemetry` is a stub that now contradicts the platform

31 lines: a `Tracer` and a `MetricsExporter` interface, two no-op
implementations, and `InitializeOpenTelemetry` that returns them.

It is unused by anything. More importantly it declares a **second, incompatible
metric abstraction** —

```go
type MetricsExporter interface {
    RecordGauge(name string, value float64, labels map[string]string)
    IncrementCounter(name string, value int64, labels map[string]string)
}
```

— with no histogram, at exactly the moment `packages/go/metrics` became the
platform's answer. A future engineer finding it first would wire a service to an
exporter that cannot express the platform's main instrument.

**Recommendation:** delete it, or reduce it to an adapter *from*
`metrics.Registry` *to* an OpenTelemetry exporter, which is the thing it was
presumably reaching for. Left in place this phase because deleting a package is
outside "no new capabilities, no removals" — flagged for an explicit decision.

**Not recommended for deletion:** `middleware`, `persistence` and `integrations`
are unreferenced but coherent and were plainly written for services not yet
built. Unreferenced is not the same as dead.

---

## 4. Cyclic dependencies

None.

```console
$ for m in metrics runtime conversation memory toolruntime governance evaluation evalsubjects; do
    (cd packages/go/$m && go list ./... >/dev/null) || echo "$m: CYCLE"
  done
# no output
```

Go rejects import cycles at compile time, so this confirms rather than
discovers. The layering is strictly acyclic by construction:

```
metrics ──▶ (nothing)
runtime ──▶ metrics
conversation, memory, toolruntime, governance, evaluation ──▶ runtime, metrics
evalsubjects ──▶ all of the above
```

---

## 5. Architectural violations

**None found.** The two load-bearing boundary claims were re-checked by command:

```console
$ cd packages/go/evaluation && go list -deps ./... | grep callscreen
.../packages/go/metrics
.../packages/go/runtime
.../packages/go/evaluation
```

The evaluation core still imports nothing it evaluates. `metrics` is the one
addition this phase, and it is dependency-free, so the claim is unweakened —
the core's transitive closure remains the Go standard library.

Governance likewise imports nothing it governs. Both checks are now enforced in
`hardening.yml` rather than restated in prose.

**One boundary changed and is worth stating plainly:** `packages/go/runtime`
previously had *zero* requires. It now requires `packages/go/metrics`. Its
`go.mod` claimed "THIS MODULE HAS NO EXTERNAL DEPENDENCIES" — still true, since
`metrics` is first-party and itself dependency-free, and the transitive closure
is unchanged at "the Go standard library". But the module is no longer
`require`-free, and that is a real change to a frozen phase, made under the
Section 2 mandate.

---

## 6. Duplicate metrics

Resolved. Verified across subsystems:

```
183 instrument names across six subsystems, no collisions
conversation counters=24 gauges=1  histograms=11
evaluation   counters=14 gauges=3  histograms=3
governance   counters=20 gauges=5  histograms=3
memory       counters=24 gauges=3  histograms=5
runtime      counters=20 gauges=3  histograms=8
toolruntime  counters=26 gauges=5  histograms=5
```

A merged scrape across all six is now possible and collision-free.
`Registry.KindConflicts()` exists so a name registered under two instrument
kinds is caught by a start-up check rather than silently merged by a
name-keyed exporter.

---

## 7. Summary

| # | Finding | Severity | Status |
|---|---|---|---|
| D1 | Six copies of the metric instruments | high | **resolved** |
| D1.1 | Histogram data unexportable from 3 of 6 subsystems | high | **resolved** |
| D2 | Three canonical encoders with incompatible kind tags | medium | open, cost now known |
| F1 | `telemetry` is a dead stub declaring a rival metric API | medium | open, needs a decision |
| — | Unreferenced but coherent packages (`middleware`, `persistence`, `integrations`) | low | no action |
| — | Cyclic dependencies | — | none |
| — | Architectural violations | — | none |

---

## 8. Related

- [METRICS_MIGRATION_REPORT.md](METRICS_MIGRATION_REPORT.md)
- [DEPENDENCY_AUDIT.md](DEPENDENCY_AUDIT.md)
- [PLATFORM_HARDENING_REPORT.md](PLATFORM_HARDENING_REPORT.md)
