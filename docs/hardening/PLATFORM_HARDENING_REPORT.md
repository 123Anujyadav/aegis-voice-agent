# Platform Hardening Report — Phase 10.5

**Objective:** convert the platform from *engineering complete* to *production
verified*.
**Result:** substantially hardened, and **not production verified** — for one
reason, stated in §6.

---

## 1. What this phase did

No new capabilities. No architecture changes. No redesign of any frozen phase.

| Section | Deliverable | Outcome |
|---|---|---|
| 1 | Repository audit | 7 findings; 2 resolved, 5 documented |
| 2 | Metrics framework refactor | **A1 resolved** after four phases |
| 3 | Race verification | Reframed; CI fixed; **still never run** |
| 4 | Durable evaluation storage | `Repository` port + reference implementation |
| 5 | Evaluation retention | 30/90/180/custom, archive, legal hold, audit |
| 6 | Observability hardening | Metrics fixed; 5 findings on tracing and correlation |
| 7 | Security hardening | 1 fixed (a false security claim), 5 open |
| 8 | Dependency audit | **Zero third-party dependencies** verified |
| 9 | Performance verification | 1 broken benchmark fixed; no regression |
| 10 | CI/CD readiness | 3 blocking defects fixed; 1 workflow added |
| 11 | Production readiness | See [FINAL_PRODUCTION_READINESS_REPORT.md](FINAL_PRODUCTION_READINESS_REPORT.md) |

---

## 2. The headline result

**Finding A1 is resolved.** Six copies of the metric instruments, first recorded
in Phase 10B and the first handover item in four consecutive audits, are now one
implementation.

Investigating it changed what the finding was. Every prior audit stated the risk
as "six histogram implementations will disagree on bucket boundaries". Measured,
that was wrong twice over: the `Observe` and `Quantile` bodies were semantically
identical, and the bucket boundaries differ **correctly** — governance decides in
hundreds of nanoseconds, a conversation turn takes seconds, and a shared bucket
set would have been the bug.

The real defect was worse and had gone unrecorded for five phases:

| Modules | Histogram in `Snapshot()` |
|---|---|
| runtime | complete: bounds, cumulative buckets, count, sum |
| conversation, memory | count + sum, **no buckets** |
| toolruntime, governance, evaluation | **`name_count` only** |

Half the platform could not export a histogram at all. Governance computed
latency percentiles in-process that no scraper could reach. The cross-subsystem
latency dashboard the SLOs depend on was **not buildable** — not "inconsistent",
not buildable.

That is the difference between a finding carried forward and a finding
investigated, and it is the main methodological lesson of this phase.

---

## 3. Changes made

### New

- **`packages/go/metrics`** — 686 lines, 26 tests, 7 benchmarks, zero
  dependencies. Exported constructors, so nothing has to fork it again.
- **`packages/go/evaluation/persistence.go`** — `Repository` port, six record
  kinds, schema versioning, migration chains, snapshot/restore, legal hold,
  content-free audit trail.
- **`packages/go/evaluation/retention.go`** — policies, schedules, archival,
  enforcement.
- **`packages/go/evalsubjects/observability_test.go`** — cross-subsystem checks
  that only compile where all five engines are imported.
- **`.github/workflows/hardening.yml`** — race artifacts, coverage floor,
  benchmarks, boundary check, release gate.

### Modified

- Six `metrics.go` files: 3,028 lines → 1,310, plus type aliases so no call site
  changed.
- Six doc comments that explained why each module had forked — now false, and
  leaving them would have been the most misleading possible outcome of a
  deduplication.
- `runtime/doc.go`: three capability-table entries corrected, one of them a
  **false security claim** (§4).
- `memory/bench_test.go`: two benchmarks that could not run to convergence.
- `ci.yml`, `pr-go.yml`: three blocking defects.

### Deliberately not done

- **Unifying the three canonical value encoders (D2).** Verified this is a
  migration, not a refactor: the encoders write `'0' + kind` as their first byte
  and the kind enums differ, so a string is `'1'` in toolruntime and evaluation
  but `'0'` in governance. Unifying changes fingerprints in at least two
  packages, invalidating every approved golden, every tool idempotency key and
  every governance decision fingerprint. Deferred with the cost now quantified
  rather than deferred vaguely.
- **Deleting `packages/go/telemetry`.** A 31-line dead stub declaring a rival
  metrics interface with no histogram. Removal is a decision, not hardening.
- **Adding tracing spans to five engines.** The port exists; adoption is a
  change to frozen call paths.

---

## 4. Defects found and fixed

| ID | Defect | Why it mattered |
|---|---|---|
| **A1** | Histogram data unexportable from 3 of 6 subsystems | The latency dashboard could not be built |
| **S1** | `runtime/doc.go` claimed the logger was "redacting"; `Kernel.Logger` says it redacts nothing | The table is the first thing a reader sees. Someone reading it and not the method could reasonably conclude it was safe to log message content — and the failure would be silent, permanent, and in the logs |
| **C1** | CI pinned Go 1.23.5; modules require 1.25.0 | Silent toolchain download or hard failure |
| **C2** | `ci.yml` ran `go build ./...` from a workspace root | Fails immediately; verified locally |
| **C3** | No CI trigger on push to `main` | The default branch had no gate |
| **P1** | Two memory benchmarks died past 50,000 iterations | They had only ever run at counts low enough to hide it; one was passing at 47,667 **by luck** |

S1 and P1 share a shape worth naming: both were claims that had never been
tested against the thing they claimed. The documentation said "redacting"
because nobody re-read it after the code settled; the benchmark passed because
nobody ran it at an iteration count the tooling picks on its own.

---

## 5. Verification

All eight modules, `gofmt` clean, `go vet` clean, `go test -count=2 -shuffle=on`
passing.

| Module | Tests | Benchmarks | Coverage |
|---|---:|---:|---:|
| `metrics` | 26 | 7 | 84.9% |
| `runtime` | 53 | 21 | 72.8% |
| `conversation` | 70 | 18 | 76.1% |
| `memory` | 77 | 21 | 80.7% |
| `toolruntime` | 87 | 22 | 76.2% |
| `governance` | 82 | 20 | 77.1% |
| `evaluation` | 94 | 22 | 75.9% |
| `evalsubjects` | 23 | — | 71.2% |
| **Total** | **512** | **131** | 71.2–84.9% |

Up from 454 tests and 124 benchmarks at the end of Phase 10F: **+58 tests, +7
benchmarks**, no test removed.

Architectural claims re-verified by command, not restated:

```console
$ cd packages/go/evaluation && go list -deps ./... | grep callscreen
.../packages/go/metrics
.../packages/go/runtime
.../packages/go/evaluation
```

The evaluation core still imports nothing it evaluates. Now enforced in CI.

---

## 6. Why this is not "production verified"

One reason, and it is not a code defect.

**The repository is not under version control.**

```console
$ git rev-parse --is-inside-work-tree
fatal: not a git repository (or any of the parent directories): .git
```

No `.git`, no remote, no commits. Five workflows exist and **none has ever
executed** — including the `-race` step in `pr-go.yml` that has been present all
along.

This reframes the platform's top blocker. Four audits recorded "`-race` has
never been run" and recommended writing CI. The CI was already written. What is
missing is a git repository, and behind that single fact sit every unverified
gate: race, lint, vet, format, coverage, vulnerability scan, and now the
hardening workflow.

"Production verified" requires evidence produced by execution. This phase
produced a great deal of evidence by execution — 512 tests, 131 benchmarks, six
defects found by running things — but the one class of verification that
requires infrastructure this machine does not have is still outstanding.

Local race detection was attempted and is genuinely unavailable: `CGO_ENABLED=0`
with no C toolchain on `PATH`. Docker Desktop is installed and would have
provided a Linux container; its daemon was not running, checked three times
across the phase.

---

## 7. Status

| | Before 10.5 | After 10.5 |
|---|---|---|
| Metric implementations | 6, three incompatible scrape shapes | 1, uniform |
| Subsystems exporting histograms | 3 of 6 | 6 of 6 |
| Evaluation persistence | in-memory only | port + reference impl + conformance suite |
| Evaluation retention | **none** | 30/90/180/custom, hold, audit |
| CI correctness | 3 blocking defects, unknown | fixed |
| Race detector runs | 0 | 0 |
| Tests | 454 | 512 |
| Third-party dependencies | 0 | 0 |

**Recommendation: approve Phase 10.5.** The engineering blockers this phase could
close are closed. The one that remains is an infrastructure step measured in
minutes — `git init`, push, read one artifact — and it should be taken before
Phase 11 rather than alongside it.

---

## 8. Related

- [REPOSITORY_AUDIT.md](REPOSITORY_AUDIT.md)
- [METRICS_MIGRATION_REPORT.md](METRICS_MIGRATION_REPORT.md)
- [RACE_VERIFICATION_REPORT.md](RACE_VERIFICATION_REPORT.md)
- [SECURITY_HARDENING_REPORT.md](SECURITY_HARDENING_REPORT.md)
- [PERFORMANCE_VERIFICATION_REPORT.md](PERFORMANCE_VERIFICATION_REPORT.md)
- [DEPENDENCY_AUDIT.md](DEPENDENCY_AUDIT.md)
- [CICD_READINESS_REPORT.md](CICD_READINESS_REPORT.md)
- [OBSERVABILITY_AUDIT.md](OBSERVABILITY_AUDIT.md)
- [FINAL_PRODUCTION_READINESS_REPORT.md](FINAL_PRODUCTION_READINESS_REPORT.md)
