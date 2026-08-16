# Telephony Runtime Evaluation Report — Phase 11A

**What was evaluated:** `packages/go/telephony`, 78 tests and 24 benchmarks.
**Method:** every figure below is output from a command, not a restatement.

---

## 1. Headline

```
ok  github.com/callscreen/callscreen-platform/packages/go/telephony  0.895s
```

| | |
|---|---:|
| Source files | 15 |
| Production lines | 4,588 |
| Test lines | 2,654 |
| Tests | 78 |
| Benchmarks | 24 |
| Coverage | **76.6%** |
| Third-party dependencies | **0** |
| `gofmt` / `go vet` | clean |
| `-count=3 -shuffle=on` | passing |

Coverage sits mid-range against the platform (71.2%–84.9%) and above the 60%
CI floor.

---

## 2. Behavioural coverage

| Area | Tests | What is verified |
|---|---:|---|
| State machine | 9 | 15 states, table completeness, reachability, terminality, refused transitions |
| Identifiers | 4 | uniqueness over 2,000 mints, sortability over 40 pairs, alphabet ordering, label validity |
| Call context | 4 | validation reports every problem, bounds, deep clone, non-disclosing render |
| Session | 7 | history, sequencing, reason codes, duration vs talk duration, bounded history and attributes |
| Registry | 6 | duplicate refusal, O(1) length, shard spread, deadlock-free iteration, one-pass census |
| Config | 4 | default validity, unbounded refusal, sweep-interval coherence, capacity |
| Scheduler | 5 | shedding, per-provider ceiling, slot pairing, failed-start release |
| Events | 6 | topic shape, no content, partition key, deliberate silence, sequencing, bounded recorder |
| Lifecycle | 6 | inbound path, outbound path, rejection, hold/mute/transfer, escalation, capability refusal |
| Timeouts | 3 | sweep, no deadline when connected, reaper |
| Failure injection | 5 | publisher outage, provider answer/hangup failure, provider hang, store outage |
| Dispatcher | 1 | three non-error outcomes |
| Snapshot & recovery | 7 | round trip, recovery-state rule, refusals, conclude, resume, abandon, determinism |
| Shutdown | 5 | immediate refusal, snapshot, idempotence, termination, provider registration order |
| Concurrency & stress | 4 | 2,000 concurrent calls, 32-way race on one session, sweep under churn, sustained churn |
| Metrics | 3 | full histogram export, zero-reporting gauges, rates |

---

## 3. The properties that matter most

### No implicit transitions

Three layers of enforcement, verified:

- **Construction** — `runtime.NewFSM` refuses a self-transition, an edge out of
  a terminal state, or a transition into an undeclared state.
- **Run time** — `FSM.To` refuses anything absent from the table.
  `TestState_FSMRefusesUndeclaredTransition` attempts `idle → connected` and
  confirms the state does not move.
- **Test** — `TestState_TransitionTableIsComplete` walks all fifteen states and
  fails on a missing entry, an undeclared destination, a self-transition, or a
  non-terminal state with no way out.

`TestState_CallMayOnlyBeginIdleOrRecovery` confirms a caller cannot fabricate a
connected call that never rang.

### A broker outage does not stop calls

`TestFailure_PublisherOutageDoesNotStopCalls` drives a complete call — begin,
ring, screen, accept, connect, **end** — with every publish failing:

- every transition succeeds
- the call ends and the registry drains to zero
- publishes were attempted (`Attempts() > 0`) and drops were counted

The critical assertion is the *end*. If a publisher failure blocked termination,
every in-flight call would stay Connected, capacity would exhaust, and an
observability outage would become a phone-system outage.

### Recovery does not assume a call survived

`TestRestore_StartsInRecoveryNotTheSnapshottedState` — a snapshot taken at
`connected` restores to `recovery`, keeps its `CallID`, gets a **new**
`SessionID`, and increments `ResumeCount`.

`TestRecovery_ConcludesCallsThatAreNoLongerLive` — three snapshots, default
`AssumeDead`, all three concluded with `RecoveryAbandoned` events so downstream
consumers see a terminal event rather than a call that simply stopped producing
them.

`TestRecovery_ResumesCallsThatAreStillLive` — with `AlwaysLive`, the call
resumes to `connected` **and holds a scheduler slot**, so the runtime does not
under-count its own load.

`TestRecovery_IsDeterministic` — two recoveries of the same store produce the
same event sequence, so an incident can be replayed.

### Thousands of concurrent sessions

`TestConcurrency_ThousandsOfSimultaneousCalls` — 50 goroutines × 40 calls =
**2,000 full lifecycles**, zero failures, zero live sessions afterwards, zero
leaked scheduler slots.

`TestConcurrency_ConcurrentTransitionsOnOneCall` — 32 goroutines race to end one
session; **exactly one wins**, the FSM refuses the other 31, and no goroutine
observes a torn state.

`TestConcurrency_SweepRunsAlongsideCallChurn` — 200 sweeps concurrent with 16
goroutines creating and ending calls. This is the path that deadlocks if `Each`
holds a shard lock while its callback transitions a call.

### Privacy

`TestEvents_CarryNoContent` renders every event from a full call and fails on
any endpoint reference. `TestSnapshot_RoundTripsWithoutContent` does the same
for snapshots. `TestSession_ReasonCodeIsBounded` rejects five malformed reasons
including one containing a phone number.

---

## 4. What the tests found

Four defects, all by execution:

| # | Defect | Found by |
|---|---|---|
| F1 | Shutdown never terminated under an injected clock | the whole suite hanging at 120 s |
| F2 | A refused transfer still recorded a leg | `TestLifecycle_HoldMuteAndTransfer` |
| F3 | Identifiers did not sort — a documented claim was false | `-count=3 -shuffle=on` |
| F4 | The event sequencer copied 12.8 KB per event | a benchmark ratio that made no sense |

F3 and F4 are the instructive ones.

**F3 passed the ordinary suite.** The original test compared a single pair of
identifiers, which passes about half the time against an alphabet whose byte
order does not match its value order. It took repeated runs with shuffling to
fail it. One sample cannot distinguish "ordered" from "ordered by luck", and the
replacement checks 40 consecutive pairs plus the alphabet property directly.

**F4 was diagnosed from an inconsistency, not a failure.** Nothing failed —
`BenchmarkHistoryAppendAtCap` was simply three times *faster* than
`BenchmarkTransition` while doing strictly more work. That ratio is impossible
unless something scales with history length, which pointed straight at the
copying accessor. 5.2× throughput and 5.9× allocation once fixed.

---

## 5. Performance summary

| Operation | Cost |
|---|---:|
| Full call lifecycle | 30.4 µs, 199 allocs |
| Full lifecycle, parallel | 16.4 µs (1.86× faster) |
| One transition | 3.39 µs |
| Registry `Get`, parallel | **10.25 ns**, 0 allocs |
| Registry `Len` | **0.38 ns**, 0 allocs |
| Admission (accept) | 117.5 ns, **0 allocs** |
| Admission (shed) | **76.1 ns** |
| Sweep, 2,000 calls | 197 µs |

At 1,000 calls per second the runtime consumes **3% of one core**. It will not
be what limits call throughput.

Full detail and caveats in [PERFORMANCE.md](PERFORMANCE.md).

---

## 6. What this evaluation does NOT establish

Stated plainly, because a page of green is easy to over-read.

**No carrier has been contacted.** `Provider` has exactly one implementation:
`FakeProvider`, which returns immediately and never misbehaves except when a
test tells it to. A green suite means *the lifecycle works against a provider
that behaves*. Real carriers send duplicate callbacks, reorder them, invent
transitions that make no sense, and time out — the dispatcher is designed for that and has not
met one.

**No media exists.** This runtime records that a provider reported media
established. It has never carried a packet, and cannot.

**Concurrency evidence is behavioural, not analytical.** 2,000 concurrent calls
and a 32-way race pass — which shows no race was severe enough to corrupt a
result *in those runs*. **`-race` has never been run**, against this or any of
the nine modules. That remains the platform's blocking item.

**76.6% coverage means 23.4% is unexecuted.** Largely error branches on paths
that need a misbehaving store or provider in combinations the fakes do not
produce.

**Single machine, single session.** Phase 10.5 measured ~40% between-session
wall-clock variance on this hardware. Allocation counts are the durable
measurement.

---

## 7. Verdict

The telephony runtime meets the brief. Fifteen states with one declared
transition table and no code path that assigns a state directly; a lifecycle
whose three critical orderings are each the opposite of the obvious choice and
each defended where the decision lives; recovery that refuses to assume a call
survived a crash; and a privacy model in which the caller's number is
structurally absent rather than carefully avoided.

Four defects were found and fixed. The blocking item is inherited and
unchanged: the race detector has never run.

---

## 8. Related

- [TELEPHONY_ARCHITECTURE.md](TELEPHONY_ARCHITECTURE.md)
- [ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md)
- [PERFORMANCE.md](PERFORMANCE.md)
- [SECURITY_REVIEW.md](SECURITY_REVIEW.md)
