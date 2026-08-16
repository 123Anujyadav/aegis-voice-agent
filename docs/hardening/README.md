# Platform Hardening & Production Verification — Documentation

**Phase 10.5** · Status: **PROPOSED — awaiting approval**

Production hardening of Phases 10A–10F. No new capabilities, no architecture
changes, no redesign.

---

## Documents

| # | Document | What it answers |
|---|---|---|
| 1 | [PLATFORM_HARDENING_REPORT.md](PLATFORM_HARDENING_REPORT.md) | What this phase changed, and the six defects it found |
| 2 | [REPOSITORY_AUDIT.md](REPOSITORY_AUDIT.md) | Duplication, dead code, cycles, architectural violations |
| 3 | [METRICS_MIGRATION_REPORT.md](METRICS_MIGRATION_REPORT.md) | Finding A1 — what it actually was, and how it was resolved |
| 4 | [RACE_VERIFICATION_REPORT.md](RACE_VERIFICATION_REPORT.md) | Why the top blocker was misdescribed for four phases |
| 5 | [SECURITY_HARDENING_REPORT.md](SECURITY_HARDENING_REPORT.md) | Secrets, crypto, PII, legal hold, and one false security claim |
| 6 | [PERFORMANCE_VERIFICATION_REPORT.md](PERFORMANCE_VERIFICATION_REPORT.md) | 131 benchmarks, one that had never converged, and why timings are not comparable |
| 7 | [DEPENDENCY_AUDIT.md](DEPENDENCY_AUDIT.md) | Zero third-party dependencies, and where the real supply-chain risk is |
| 8 | [OBSERVABILITY_AUDIT.md](OBSERVABILITY_AUDIT.md) | Metrics, logging, tracing, correlation IDs |
| 9 | [CICD_READINESS_REPORT.md](CICD_READINESS_REPORT.md) | Three blocking CI defects, and the workflow that has never run |
| 10 | [FINAL_PRODUCTION_READINESS_REPORT.md](FINAL_PRODUCTION_READINESS_REPORT.md) | **Readiness across all five dimensions, and what to do next** |

---

## The short version

**Finding A1 is resolved, and it was not what five audits said it was.** Six
copies of the metric instruments became one. The recorded risk — "histograms
will disagree on bucket boundaries" — was wrong: the boundaries differ correctly,
because governance decides in hundreds of nanoseconds and a conversation turn
takes seconds. The real defect was that **three of six subsystems could not
export a histogram at all**, so the cross-subsystem latency dashboard was not
buildable. That had never been written down.

**The evaluation platform can now be called permanent.** A `Repository` port with
schema versioning, one-step migration chains, snapshots, legal hold and a
content-free audit trail — plus a retention schedule covering every record kind,
with construction refusing any schedule that leaves one uncovered.

**The top blocker was misdescribed.** Four audits said "`-race` has never been
run; create the CI configuration". The configuration existed all along in
`pr-go.yml`. What is missing is a **git repository** — there is no `.git`, so no
workflow has ever executed, and behind that one fact sit every unverified gate.

**Six defects found, all by running something:**

| | |
|---|---|
| A1 | Histogram data unexportable from half the platform |
| S1 | `runtime/doc.go` claimed the logger was "redacting"; it is not |
| C1 | CI pinned Go 1.23.5 against modules requiring 1.25.0 |
| C2 | `ci.yml` ran `go build ./...` from a workspace root — cannot work |
| C3 | No CI gate on pushes to `main` |
| P1 | Two benchmarks died past 50,000 iterations; one passed at 47,667 by luck |

---

## Numbers

| | Before | After |
|---|---:|---:|
| Metric implementations | 6 | 1 |
| Subsystems exporting histograms | 3 of 6 | **6 of 6** |
| `metrics.go` lines across six modules | 3,028 | 1,310 (+686 shared) |
| Tests | 454 | **512** |
| Benchmarks | 124 | **131** |
| Third-party dependencies | 0 | **0** |
| Race detector runs | 0 | **0** |

---

## Verification

```console
# all eight modules: gofmt clean, vet clean, tests pass
cd packages/go/<module> && go vet ./... && go test -count=2 -shuffle=on ./...

# the boundary claim
cd packages/go/evaluation && go list -deps ./... | grep callscreen

# cross-subsystem observability
cd packages/go/evalsubjects && go test -run TestObservability -v ./...

# retention and persistence
cd packages/go/evaluation && go test -run 'TestRetention|TestRepository|TestSweep' -v ./...
```

---

## Status

**Not production verified**, and every remaining gap is operational rather than
architectural. The first step is `git init` — it costs minutes and gates
everything else. See
[FINAL_PRODUCTION_READINESS_REPORT.md](FINAL_PRODUCTION_READINESS_REPORT.md) §7.

---

## Related

- [../evaluation/](../evaluation/) — Phase 10F
- [../governance/](../governance/) — Phase 10E
- [../tools/](../tools/) — Phase 10D
- [../memory/](../memory/) — Phase 10C
- [../conversation/](../conversation/) — Phase 10B
- [../runtime/](../runtime/) — Phase 10A
