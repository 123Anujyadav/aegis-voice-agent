# Evaluation Report — Phase 10F

**What was evaluated:** Runtime Core (10A), Conversation Engine (10B), Memory
Engine (10C), Tool Runtime (10D), Governance Engine (10E).
**How:** 18 scenarios, 8 kinds, 3 suites, executed against the **real** frozen
engines through `packages/go/evalsubjects`.
**Source:** `evalsubjects/verification_test.go`, 19 tests. Every figure below is
logged output, not a restatement.

---

## 1. Headline

```
run run_ddffa5gm5chiyaaaadvykeykwa (phase-10f-verification)
digest=ca40a3aae34aa10c  releasable=true

conversation    2 scenarios:  2 pass  0 drift  0 fail  0 skip  0 new | pass=100% coverage=100%
governance      5 scenarios:  5 pass  0 drift  0 fail  0 skip  0 new | pass=100% coverage=100%
memory          4 scenarios:  4 pass  0 drift  0 fail  0 skip  0 new | pass=100% coverage=100%
runtime         3 scenarios:  3 pass  0 drift  0 fail  0 skip  0 new | pass=100% coverage=100%
toolruntime     4 scenarios:  4 pass  0 drift  0 fail  0 skip  0 new | pass=100% coverage=100%
```

18 of 18 scenarios reproduced their approved baseline. No drift, no failure, no
skip.

**What that sentence means, precisely:** the 18 questions the library asks were
answered exactly as they were answered when a human approved them. It does not
mean the five engines are correct. See §7.

---

## 2. Coverage

```
scenarios=18  subjects=5  kinds=8  suites=3  registry-digest=ca40a3aae34aa10c
```

| Subject | Scenarios |
|---|---:|
| governance | 5 |
| memory | 4 |
| toolruntime | 4 |
| runtime | 3 |
| conversation | 2 |

| Kind | Scenarios |
|---|---:|
| runtime | 3 |
| memory | 3 |
| tool | 3 |
| governance | 3 |
| failure | 3 |
| conversation | 1 |
| emergency | 1 |
| recovery | 1 |

**No subject is unevaluated.** `PlatformReadiness.UnevaluatedSubjects` is empty,
and would force `Ready` false if it were not. **No kind is uncovered** — all
eight have at least one scenario.

---

## 3. Gating suites

| Suite | Kind | Gating | Scenarios | Blockers | Wall time |
|---|---|---|---:|---:|---:|
| `acceptance` | acceptance | **yes** | 15 | 0 | 32.4 ms |
| `compliance` | compliance | **yes** | 4 | 0 | 8.4 ms |
| `benchmark` | benchmark | no | 6 | 0 | 8.3 ms |

Both gating suites are clean.

---

## 4. Determinism

```
all 18 scenarios deterministic across 3 runs against the real engines
```

Per-subject, from the readiness pass:

```
conversation.escalation on conversation:       deterministic across 3 runs (890289e4d7eed883)
governance.baseline-decides on governance:     deterministic across 3 runs (308ffc4fcde44b13)
memory.forget-is-complete on memory:           deterministic across 3 runs (a42c5863a1e7dd7b)
runtime.breaker-opens on runtime:              deterministic across 3 runs (8a6de857d2c25471)
tool.execute on toolruntime:                   deterministic across 3 runs (8bf7fc0af6b4d34a)
```

This is the precondition for everything else in this document. A subsystem that
did not reproduce would make drift meaningless — the platform could not
distinguish "the engine changed" from "the engine is noisy".

---

## 5. Replay

```
all 18 approved goldens replayed against the real engines
```

And the negative control, which matters more:

```
runtime.breaker-opens on runtime: NOT REPRODUCED
  (47fbca2b51f3745c → 8a6de857d2c25471, 9 differences, behaviour_differs)
```

A recording from a different scenario, replayed, diverges with nine named
differences. A replay engine that reproduced everything would be
indistinguishable from one that compared nothing.

---

## 6. Drift detection

The platform's purpose, verified end to end on a real engine:

```
drift detected and filed as candidate gld_ddffa5gljk4fyaaaaa6x3ohnfm:
  runtime.breaker-opens on runtime: drift (behaviour_changed)
  8a6de857d2c25471 → 32c24a2eb59dde70
    step_count: 6 → 7
```

Three properties in one result:

1. behaviour change detected against an approved baseline
2. the change **named** (`step_count: 6 → 7`), not just a hash difference
3. a golden candidate **filed**, so the reviewer has something concrete to
   promote at the moment their decision is required

That third property was absent until ENGINEERING_AUDIT F2 was fixed.

---

## 7. Failure injection

Three scenarios drive declared failure modes through the real adapters:

```
governance.malformed-refused    [decide=deny]
memory.oversized-refused        [store=refused]
tool.failure-is-classified      [register=ok execute=tool_error health=ok]
```

The property checked is **not** that the engine fails. It is that an injected
failure is recorded as an **outcome** rather than as a platform error. A
governance engine denying a malformed request is the engine working correctly;
a platform that recorded that as its own failure would make every safety
scenario unreadable.

Each of these confirms a frozen phase's central claim:

- `governance.malformed-refused` — **default deny** holds on input nobody
  designed for
- `memory.oversized-refused` — INV-MEM-8's size cap refuses rather than
  truncating
- `tool.failure-is-classified` — a tool error is classified, and the runtime
  stays healthy afterwards

---

## 8. What this does not establish

Stated plainly, because a page of green is easy to over-read.

**Coverage is thin.** 18 scenarios against five substantial subsystems.
Governance has 5 scenarios against 9 scopes and 10 outcomes; conversation has 2.
A green result means the 18 questions asked were answered as approved — not that
the engines are correct. See ENGINEERING_AUDIT A6.

**The baselines are one session old.** Every golden in this verification was
approved during the verification run itself, from the engines' current
behaviour. That is the honest starting state for a platform being stood up: it
proves the mechanism works, not that the engines have been stable over time. The
value compounds only once these baselines are months old.

**Adapters observe through public API only, and sometimes that is the limit.**
Several adapters can see a subsystem only through the test harness that phase
exported. Where a property is not observable through exported API, no scenario
can check it — and that is a fact about the phase, recorded rather than worked
around.

**No `-race`.** See ENGINEERING_AUDIT A2. Blocking.

---

## 9. Related

- [SCENARIO_ARCHITECTURE.md](SCENARIO_ARCHITECTURE.md)
- [BENCHMARK_REPORT.md](BENCHMARK_REPORT.md)
- [REGRESSION_REPORT.md](REGRESSION_REPORT.md)
- [PLATFORM_READINESS_REPORT.md](PLATFORM_READINESS_REPORT.md)
