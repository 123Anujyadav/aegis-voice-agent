# Engineering Audit — Phase 11A

**Scope:** `packages/go/telephony` — 15 source files, 4,588 lines, 2,654 lines
of tests, 78 tests, 24 benchmarks, 76.6% coverage.

---

## 1. Brief compliance

| Requirement | Status | Evidence |
|---|---|---|
| No telephony SDK or framework | ✅ | `go list -deps` shows two first-party requires and no third party |
| Built from scratch in Go 1.25 | ✅ | 4,588 production lines, zero external modules |
| `TelephonyRuntime`, `CallCoordinator`, `CallScheduler`, `CallRegistry`, `CallLifecycle`, `CallDispatcher`, `RuntimeMetrics` | ✅ | all seven implemented |
| Fifteen explicit states | ✅ | `TestState_FifteenStatesExist` |
| No implicit transitions | ✅ | one declared table, FSM-enforced, `TestState_TransitionTableIsComplete` |
| Session create/update/snapshot/restore/resume/terminate | ✅ | `session.go`, `recovery.go` |
| Correlation, session and call identifiers | ✅ | `ids.go`, and see A1 |
| Call context: caller, callee, direction, channel, provider, metadata, capabilities, tags | ✅ | `context.go` |
| Lifecycle: incoming, outgoing, connect, disconnect, timeout, failure, transfer, recovery | ✅ | `lifecycle.go` |
| Eight required events | ✅ | plus six more; `events.go` |
| Session recovery, reconnect, resume, graceful shutdown | ✅ | `recovery.go`, `runtime.go` |
| Thousands of concurrent sessions | ✅ | 2,000 verified; 64-shard registry |
| No global mutable state | ✅ | nothing package-level and mutable |
| Metrics: duration, session count, failures, recovery, lifecycle latency | ✅ | `metrics.go`, 26 instruments |
| Unit, integration, lifecycle, stress, concurrency, failure-injection, recovery tests | ✅ | 78 tests |
| Provider agnostic, deterministic, thread safe, streaming ready | ✅ | see §5 |
| No SIP, RTP, WebRTC, audio, STT, TTS, LLM, fraud, emergency | ✅ | absent |

---

## 2. Defects found and fixed

Four. Every one was found by running something — three by the test suite and
benchmarks, one by `-count=3 -shuffle=on`.

### F1 — Graceful shutdown never terminated *(blocking, fixed)*

`drain` measured its deadline against the **injected** clock while polling with
a **real** ticker:

```go
deadline := r.clock.Now().Add(r.cfg.DrainTimeout)   // fake clock
ticker := time.NewTicker(pollInterval)              // real time
```

Under a `FakeClock` that nobody advances — every test, and any deployment that
injects a controlled clock — `clock.Now()` never moves, the deadline never
arrives, and `Stop` spins forever holding the process open.

The whole suite hung at 120 seconds. Any test that left a live call and then
stopped the runtime never returned.

**Why it matters beyond tests.** A graceful shutdown that never terminates is
worse than an abrupt one: the orchestrator waits out its entire grace period and
sends `SIGKILL` anyway, having wasted it — and the snapshot that would have let
those calls recover is never written, because the code that writes it sits after
the drain.

**Fix.** The drain budget is real time. That is also right on its own terms:
this is an operational shutdown allowance — a Kubernetes
`terminationGracePeriod` — not call-lifecycle semantics. Call timeouts belong to
the injected clock; wall-clock patience during shutdown belongs to the wall
clock. `TestShutdown_TerminatesWithLiveCalls` is the regression test.

**A second-order finding.** The fix made `DrainTimeout` real, so a test passing
its own `Config` built from `DefaultConfig` silently reintroduced a 30-second
shutdown — the suite went from hanging to taking 30.5 s. Exported `TestConfig()`
so the fast path is the obvious one; the suite now runs in 0.63 s.

### F2 — A refused transfer still recorded a leg *(fixed)*

`Transfer` minted and recorded the leg **before** attempting the transition:

```go
leg := NewLegID()
sess.AddLeg(leg)                                    // ← unconditional
if err := l.transition(ctx, sess, StateTransferred, reason); err != nil {
    return "", err                                  // leg survives the failure
}
```

A transfer refused by the table — from `muted`, which is refused by design —
left a leg on the session anyway. The call then looked transferred to anything
counting legs while its state said otherwise, and the two disagreed permanently.

Found by `TestLifecycle_HoldMuteAndTransfer`, which attempts the refused
transfer first and then a legal one, and saw two legs where one was expected.

**Fix.** Transition first; mint the leg only on success.

### F3 — Identifiers did not sort *(fixed)*

`ids.go` used the alphabet `"abcdefghijklmnopqrstuvwxyz234567"`. That places
digits **after** letters by index but **before** them in ASCII, so base32 value
order and byte order disagree — and the documented claim was:

> The timestamp prefix makes identifiers roughly sortable by creation, which is
> what makes them usable as a database primary key without a secondary index on
> created_at.

That claim was simply false. Two identifiers a millisecond apart could compare in
either direction, depending on whether the tenth character — which straddles the
last three timestamp bits and the first two random bits — landed on a digit or a
letter.

**Found by `-count=3 -shuffle=on`, not by the ordinary suite.** The original
test compared a **single** pair, which passes about half the time. One sample
cannot distinguish "ordered" from "ordered by luck".

**Fix.** Lowercase Crockford base32: `"0123456789abcdefghjkmnpqrstvwxyz"`.
Digits first (so byte order matches value order), lowercase (no case-sensitivity
bugs in metric labels and Kafka keys), and Crockford's letter set (i, l, o, u
omitted, so an identifier read aloud to a support agent cannot be transcribed as
a different one).

`packages/go/runtime` had this right all along with uppercase Crockford. This is
the same alphabet, lowercased — the mistake was inventing a new one.

The test was replaced with one that checks 40 consecutive pairs, plus
`TestIDs_AlphabetIsAsciiSortable`, which checks the property directly and cannot
pass by luck.

### F4 — The event sequencer copied the whole history *(fixed)*

`sequencer.next` obtained a per-call sequence number as `len(sess.History())` —
and `History()` deep-copies every retained transition. At the 128-record cap
that is **12.8 KB allocated per published event**.

Measured, before and after:

| Benchmark | Before | After | Factor |
|---|---:|---:|---:|
| `BenchmarkTransition` | 17,511 ns/op | 3,390 ns/op | **5.2×** |
| | 13,087 B/op | 2,218 B/op | **5.9×** |

The tell was a benchmark that made no sense: `BenchmarkHistoryAppendAtCap` was
**three times faster** than `BenchmarkTransition` while doing strictly more
work. A history at its cap stops growing, so the copy stopped growing with it.

**This is the same shape as Phase 10F's `PendingApprovals` defect** — a
cheap-looking accessor that copies a collection, called on a hot path. That one
cost 45×. Recognising the shape is what turned an odd benchmark ratio into a
diagnosis.

**Fix.** `HistoryLen()`, O(1) under an RLock. `History()` now documents that it
copies and points at the alternative.

---

## 3. Open findings

### A1 — A third `CorrelationID` *(medium, knowingly incurred)*

Phase 10.5's observability audit recorded (O2) that `CorrelationID` is declared
independently in `toolruntime` and `governance`, that neither is in
`packages/go/runtime`, and that the two are unrelated Go types requiring a
string conversion to bridge. **This is the third declaration.**

It is not avoidable in this phase. Telephony sits **above** conversation,
governance and tool runtime, so importing either existing definition would
invert the dependency direction and couple the call lifecycle to the tool
executor. The correct home is `packages/go/runtime`, which every module already
imports — and that module is frozen, and this phase's brief forbids modifying it.

**Recommendation, unchanged from Phase 10.5 and now stronger:** move
`CorrelationID` into `packages/go/runtime` as an additive change, and collapse
all three. Each phase that passes makes the migration wider.

### A2 — Correlation still cannot span the platform *(high)*

Phase 10.5 found conversation, memory and the runtime core carry no correlation
identifier, so an end-to-end trace cannot be assembled. Telephony now mints one
at the very top of the stack — the natural origin — which makes the gap more
visible, not less: the identifier exists at the entry point and is dropped at
the first hop.

### A3 — `-race` has never been run *(BLOCKING, inherited)*

Now **nine** modules. This one is heavily concurrent: a 64-shard registry, a
sharded scheduler, per-session locks, background sweep and snapshot loops.
`TestConcurrency_ThousandsOfSimultaneousCalls` drives 2,000 calls across 50
goroutines and
`TestConcurrency_ConcurrentTransitionsOnOneCall` races 32 goroutines onto one
session — both pass, and both are weaker evidence than they appear without the
detector.

The CI configuration exists (`hardening.yml`, added in Phase 10.5) and the
repository is still not under version control, so it has never run.

### A4 — No provider adapter exists *(expected, by design)*

`Provider` has one implementation: `FakeProvider`, for tests. Nothing here talks
to a carrier, which is the phase boundary. Worth stating plainly so a green test
suite is not read as "telephony works" — it means the lifecycle works against a
provider that always behaves.

### A5 — Liveness is a deployment's responsibility *(medium)*

`LivenessCheck` defaults to `AssumeDead`, which concludes every recovered call.
A deployment that never supplies a real check loses every in-progress call
across a restart — safely, but silently. The metric `RecoveryAttempts{outcome}`
makes it visible; nothing forces the decision.

### A6 — A pre-existing flake in Phase 10F *(reported, NOT fixed)*

Found while re-verifying the frozen phases. `TestVerification_Benchmarks` in
`packages/go/evalsubjects/verification_test.go:457` fails intermittently — about
one run in five under `-shuffle=on`:

```
runtime.clock-advances on runtime: amortised=0s n=40 p50=0s ... [BELOW CLOCK RESOLUTION 518.4µs]
verification_test.go:457: runtime.clock-advances: amortised mean is zero — the benchmark measured nothing
```

**Cause.** The assertion `b.AmortisedMean <= 0` assumes 40 iterations always sum
to something the clock can see. `runtime.clock-advances` merely advances a fake
clock; all 40 iterations can complete inside a single ~518 µs tick, so the total
quantises to zero and the amortised mean with it. Amortisation raises the floor
but does not guarantee measurability.

It is the Phase 10F clock-resolution finding (F1) reappearing one level up, in
the assertion written to guard against it.

**Not fixed.** Phase 11A's brief states "DO NOT MODIFY ANY PREVIOUS PHASE"
without the compile-error exemption earlier phases carried, and this is a test
flake rather than a broken dependency.

**Recommended fix for a future phase:** when `BelowResolution` is true, treat a
zero amortised mean as *unmeasurable* rather than *measured nothing* — or raise
the iteration count until total elapsed exceeds the resolution.

---

## 4. The frozen phases were not modified

Verified — nine modules, `gofmt` clean, `go vet` clean:

```
metrics       ok    runtime      ok    conversation  ok
memory        ok    toolruntime  ok    governance    ok
evaluation    ok    evalsubjects ok*   telephony     ok
```

\* `evalsubjects` carries the pre-existing flake in A6. It passes on the great
majority of runs and its failure is unrelated to this phase — telephony is not
in its dependency graph.

No file outside `packages/go/telephony` was changed, except one line in
`go.work` adding the module.

---

## 5. Quality requirements

| Requirement | How it is met |
|---|---|
| **Production ready** | Every failure mode has a test; every bound is validated at construction |
| **Thread safe** | Sharded registry, per-session locks, atomic counters; 2,000 concurrent calls verified |
| **Deterministic** | Every clock injected; recovery ordered oldest-first; no wall-time dependency in call semantics |
| **Provider agnostic** | Four-method port with no telephony vocabulary; capability refusals are generic |
| **Cloud native** | No local state beyond the registry; `SessionStore` port for Redis/Aurora; graceful drain |
| **Horizontally scalable** | No package-level state; two runtimes share nothing; per-provider ceilings |
| **Streaming ready** | Lifecycle is event-driven; the runtime never blocks on media, which it does not carry |
| **No global mutable state** | Nothing package-level and mutable exists |

---

## 6. Test inventory

| Suite | File | Tests | Covers |
|---|---|---:|---|
| Unit | `telephony_test.go` | 40 | states, table completeness, identifiers, context, session, registry, config, scheduler, events |
| Integration | `integration_test.go` | 38 | lifecycle paths, timeouts, failure injection, dispatcher, snapshot/restore, recovery, shutdown, concurrency, stress, metrics |
| Benchmarks | `bench_test.go` | 24 | lifecycle, transitions, registry, scheduler, session, identifiers, sweep, scrape |

---

## 7. Verdict

The runtime does what the brief asks. Fifteen states with one declared
transition table and no path that assigns a state directly; a lifecycle where
the three critical orderings are each the opposite of the obvious choice and
each defended in a comment; recovery that refuses to assume a call survived a
crash; and a privacy model where the caller's number is structurally absent
rather than carefully avoided.

Four defects were found and fixed. Two of them — F1's frozen-clock hang and F4's
copying accessor — are defects in *judgement about time and cost* rather than in
logic, and both were found by measuring rather than reading. F3 is the one worth
remembering: a documented claim that was false, caught only because the suite
was run repeatedly with shuffling.

**One blocking item remains, inherited: A3.** The race detector has never been
run against any of the nine modules, and this is the most concurrent one yet.

---

## 8. Related

- [PERFORMANCE.md](PERFORMANCE.md)
- [SECURITY_REVIEW.md](SECURITY_REVIEW.md)
- [EVALUATION_REPORT.md](EVALUATION_REPORT.md)
