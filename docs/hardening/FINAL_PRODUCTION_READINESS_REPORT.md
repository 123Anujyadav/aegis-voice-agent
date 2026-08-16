# Final Production Readiness Report

**Phases 10A – 10.5** · Runtime Core, Conversation, Memory, Tool Runtime,
Governance, Evaluation, Hardening.

---

## 1. Verdict by dimension

| Dimension | Status | Blocking gap |
|---|---|---|
| **Architecture** | ✅ Ready | none |
| **Engineering** | ✅ Ready | none |
| **Security** | 🟡 Conditional | retention rule exists; PII-in-scenarios is a process control only |
| **Performance** | ✅ Ready for the workload measured | nothing reflects durable storage |
| **Operational** | ❌ **Not ready** | no CI has ever run; no metrics leave the process; no durable store |

**Overall: not production ready, and every remaining gap is operational.** None
is an architecture or correctness defect.

---

## 2. Architecture readiness — ✅

The boundary claims are checkable rather than asserted, and were re-checked this
phase by command:

```console
$ cd packages/go/evaluation && go list -deps ./... | grep callscreen
.../packages/go/metrics
.../packages/go/runtime
.../packages/go/evaluation
```

- The evaluation core imports nothing it evaluates.
- Governance imports nothing it governs.
- No import cycles.
- Zero third-party dependencies across the entire Go plane — the transitive
  closure of every AI module is the Go standard library plus first-party code.

Both boundary checks now run in CI instead of living in a document.

The layering is acyclic by construction: `metrics` → nothing; `runtime` →
`metrics`; the five engines → `runtime`, `metrics`; `evalsubjects` → all.

**One change to a frozen phase**, made under the Section 2 mandate:
`packages/go/runtime` previously had no `require` at all and now requires
`packages/go/metrics`. Its "no external dependencies" claim survives — `metrics`
is first-party and dependency-free — but the module is no longer require-free,
and that is recorded rather than glossed.

---

## 3. Engineering readiness — ✅

| | |
|---|---:|
| Production lines (Go, AI plane) | 41,920 |
| Tests | 512 |
| Benchmarks | 131 |
| Coverage | 71.2% – 84.9% |
| `gofmt` / `go vet` | clean |
| `go test -count=2 -shuffle=on` | passing |

Across six phases, **26 defects** were found and fixed by running the code
rather than reading it. The pattern that produced them is consistent: build the
measurement first, then write the claim. Phase 10D's 120-second hang, Phase
10E's fail-closed handler that killed the process at the moment it was supposed
to fail closed, Phase 10F's clock resolution making every latency zero, and this
phase's six all surfaced that way.

Two findings from this phase are worth carrying as a lesson rather than an item:

- **A1 was misdescribed for five phases.** Recorded four times as a bucket-
  boundary risk; measured, the boundaries were correct and the actual defect —
  half the platform unable to export a histogram — had never been written down.
  A finding carried forward is not a finding investigated.
- **Two benchmarks had never run to convergence.** They failed the moment CI ran
  them at a fixed `-benchtime`, and one had been passing at 47,667 iterations
  against a 50,000 cap **by luck**.

---

## 4. Security readiness — 🟡

**Strong by construction.** The AI plane performs no I/O — no network client, no
database driver, no filesystem access. There is no connection string to leak, no
request to forge, no TLS to misconfigure. No secrets, credentials or keys exist
anywhere in it, and `crypto/rand` and `crypto/sha256` are used correctly where
they appear.

**Fixed this phase (S1):** `runtime/doc.go` advertised the logger as
"redacting". `Kernel.Logger` states in capitals that it redacts nothing. The
capability table is the first thing a reader of the package sees; an engineer
who read it and not the method could reasonably have concluded it was safe to
log message content. A false claim about a security control is worse than an
absent one.

**New this phase:** legal hold enforced at four independent layers, each with a
test named for the failure it prevents — `Record.Expired` returns false under
hold so no backend has to restate the rule, `Delete` refuses, `Put` preserves a
hold across replacement, `Restore` preserves holds placed after the snapshot.

**Open:**

| ID | Finding | Severity |
|---|---|---|
| S2 | No redaction mechanism; "never log content" is convention | medium |
| S4 | Nothing prevents a scenario using production data | medium |
| S5 | Observations unbounded in size | medium |
| S6 | Approval attribution is not authentication | low |
| D3 | GitHub Actions pinned by mutable tag | medium |

S4 is the one to watch. The platform now bounds **how long** a recording lives —
90 days for observations, mirroring ADR-0012, which had no counterpart in the
evaluation platform until this phase. Nothing bounds **what** the recording
contains. That is a process control and this report says so rather than
implying a technical one exists.

---

## 5. Performance readiness — ✅ for what was measured

Zero-allocation hot paths where it matters: every metric instrument,
`conversation.Planner`, `evaluation.GoldenBaselineLookup`. Expensive paths are
expensive for legible reasons and scale linearly —
`governance.DecideLarge` is 11.7× `DecideSmall` on a proportionally larger
policy set, with no super-linear term.

**No regression from the metrics migration**, evidenced by byte-identical
allocation counts on every path measured.

**A methodological correction that qualifies earlier reports:** wall-clock
figures from this machine vary ~40% *between sessions* while varying ±3% *within*
one. Absolute nanosecond figures published across Phases 10A–10F are therefore
not comparable to each other. Allocation counts are the durable measurement.
This is the same class of error as Phase 10F's clock-resolution defect — a
number that looks like a measurement but is largely an artifact of the machine.

**Not measured:** anything involving durable storage, which will dominate. A
50 ns in-memory golden lookup becomes a network round trip.

---

## 6. Operational readiness — ❌

Three gaps, in priority order.

### B1 — No CI has ever run *(blocking)*

The repository is not under version control. No `.git`, no remote, no commits.
Five workflows exist and none has executed.

This reframes what four audits recorded as "`-race` has never been run": the
race configuration was already there, in `pr-go.yml`, auto-discovering modules
from `go.work`. What is missing is a git repository — and behind that one fact
sit *every* unverified gate: race, lint, vet, format, coverage, vulnerability
scan.

Three blocking CI defects were found and fixed this phase precisely because a
never-executed workflow accumulates errors silently: a Go version too old to
build the platform, a build command that cannot work in a workspace, and no gate
on the default branch.

**Cost to close: minutes.** `git init`, commit, push, read one artifact.

### B2 — No durable evaluation storage *(blocking for "permanent infrastructure")*

The **port** now exists and is complete: `Repository` with schema versioning,
one-step migration chains, snapshot/restore, retention hooks, legal hold and a
content-free audit trail. A conformance suite is written against the interface,
so the Aurora implementation Phase 11 adds is verified by exactly the code that
verifies the reference one — the behaviour a backend must reproduce is pinned
now, while there is only one implementation, rather than inferred later from
whatever the second one happened to do.

What does not exist is a backend. **Goldens still do not survive a restart.**

### B3 — Nothing observable leaves the process *(blocking for operability)*

Every subsystem now produces a complete, uniform `[]Sample`. Nothing converts
one into a scrape endpoint.

This is the smallest of the three and has the highest leverage: `Sample` carries
`Kind`, `Name`, `Labels`, `Value`, `Bounds`, `Buckets`, `Count` and `Sum`, which
is precisely the Prometheus exposition model. One adapter, written once, now
handles all six subsystems — which was impossible before this phase, because it
would have needed a branch per subsystem and three of those branches would have
had no data to read.

Tracing is a related but separate gap: the port exists and is well shaped, and
**one span is emitted across the entire AI plane** (`runtime.generate`).
Correlation IDs are carried by two of six subsystems using two unrelated types,
neither defined in `runtime`, so an end-to-end trace cannot currently be
assembled even with a backend.

---

## 7. Recommendations before Phase 11

**In order. The first is not optional.**

1. **`git init`, commit, push.** Let `hardening.yml` run. Read the
   `race-logs-repeated` artifact. Until it exists, every concurrency claim
   across 41,920 lines rests on the absence of observed symptoms. *(B1)*

2. **Write the metrics exporter.** One adapter outside the AI plane, converting
   `[]Sample` to Prometheus. Smallest item on this list, largest operational
   payoff, and it converts the whole Section 2 refactor from correct-in-
   principle to visible-in-production. *(B3)*

3. **Decide the scenario data rule** and state it in `evalsubjects`. Free, and
   the wrong time to decide it is after a durable store exists. *(S4)*

4. **Implement the durable `Repository`**, with the retention schedule wired in
   from the start. The conformance suite already defines correct. *(B2)*

5. **Move `CorrelationID` into `runtime`** and thread it through conversation
   and memory, **then** instrument spans. Doing spans first produces traces with
   two of six subsystems participating. *(O2, O3, O1)*

6. **Pin GitHub Actions to commit SHAs.** The Go code has no supply-chain
   surface; the machinery that builds it has the usual amount. *(D3)*

7. **Add an observation size cap**, following `INV-MEM-8`'s precedent rather
   than inventing a new one. *(S5)*

8. **Decide on `packages/go/telemetry`** — delete it, or make it the adapter for
   `runtime.Tracer`. It currently declares a rival metrics interface with no
   histogram, and a future engineer will find it before finding the real one.

Items 3, 6 and 8 are decisions rather than implementations and could be closed
this week.

---

## 8. What was explicitly not started

Per the stop condition: no telephony intelligence, no audio, no STT, no TTS, no
SIP, no carrier integrations. Nothing in this phase touches the media plane.

---

## 9. Readiness statement

**The platform is engineering complete and operationally unverified.**

Six phases produced 41,920 lines of Go with zero third-party dependencies, 512
tests, 131 benchmarks, and boundary claims that are checked by command rather
than asserted in prose. Phase 10.5 resolved the longest-standing engineering
finding, gave the evaluation platform the persistence and retention machinery it
needed to be called permanent, and found six further defects — three of them in
CI configuration that had never run, one of them a false security claim in the
most-read documentation in the runtime package.

What stands between here and production is not code. It is a git repository, an
exporter, and a database implementation behind an interface that already exists.
The first costs minutes and gates everything else.

**Recommendation: approve Phase 10.5. Close B1 before beginning Phase 11.**

---

## 10. Related

- [PLATFORM_HARDENING_REPORT.md](PLATFORM_HARDENING_REPORT.md)
- [../evaluation/PLATFORM_READINESS_REPORT.md](../evaluation/PLATFORM_READINESS_REPORT.md)
