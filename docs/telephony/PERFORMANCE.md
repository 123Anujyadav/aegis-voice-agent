# Performance Report — Phase 11A

**Machine:** 11th Gen Intel Core i7-11800H @ 2.30 GHz, 16 logical CPUs,
windows/amd64, Go 1.26.5.
**Command:** `go test -run XXX -bench=. -benchmem -benchtime=300ms`
**Measured clock resolution:** ~520 µs (Windows `time.Now()` granularity).

---

## 1. Results

```
BenchmarkFullCallLifecycle-16             12555     30423 ns/op    24858 B/op    199 allocs/op
BenchmarkFullCallLifecycleParallel-16     20629     16376 ns/op    24886 B/op    199 allocs/op
BenchmarkTransition-16                   109809      3390 ns/op     2218 B/op     20 allocs/op
BenchmarkTransitionRefused-16           1000000       367.3 ns/op     208 B/op      5 allocs/op
BenchmarkDispatchNotApplicable-16        230308      1765 ns/op     2368 B/op     18 allocs/op
BenchmarkRegistryRegister-16              33885     10496 ns/op    10102 B/op     75 allocs/op
BenchmarkRegistryGet-16                 7475160        44.18 ns/op      0 B/op      0 allocs/op
BenchmarkRegistryGetParallel-16        36114844        10.25 ns/op      0 B/op      0 allocs/op
BenchmarkRegistryLen-16               937768630         0.3769 ns/op    0 B/op      0 allocs/op
BenchmarkRegistryByState-16                1668    214925 ns/op    43192 B/op     68 allocs/op
BenchmarkAdmitAndRelease-16             2616730       117.5 ns/op       0 B/op      0 allocs/op
BenchmarkAdmitAndReleaseParallel-16     1812514       190.7 ns/op       0 B/op      0 allocs/op
BenchmarkAdmitShedPath-16               4779885        76.06 ns/op     32 B/op      1 allocs/op
BenchmarkSnapshot-16                     487203       691.6 ns/op     904 B/op      8 allocs/op
BenchmarkRestore-16                       44736      8328 ns/op    10288 B/op     72 allocs/op
BenchmarkContextClone-16                 535761       668.2 ns/op     776 B/op      8 allocs/op
BenchmarkContextValidate-16            16061246        21.75 ns/op      0 B/op      0 allocs/op
BenchmarkNewCallID-16                   2130025       168.5 ns/op     64 B/op      2 allocs/op
BenchmarkEventPublish-16                1000000       613.5 ns/op   1000 B/op      0 allocs/op
BenchmarkSweep-16                          1986    197159 ns/op    50568 B/op    196 allocs/op
BenchmarkSnapshotAll-16                     280   1304902 ns/op  1348910 B/op   8071 allocs/op
BenchmarkMetricsSnapshot-16               13036     25978 ns/op    28008 B/op    280 allocs/op
BenchmarkClockResolution-16            93158058         3.925 ns/op      0 B/op      0 allocs/op
BenchmarkHistoryAppendAtCap-16           117756      3343 ns/op     2212 B/op     20 allocs/op
```

---

## 2. Headline: a call costs 30 µs of runtime

`FullCallLifecycle` is admission, session creation, five state transitions,
five metric updates, four published events, teardown and slot release: **30.4 µs
and 199 allocations**.

At a sustained 1,000 calls per second — a large Indian carrier deployment — that
is **3% of one core**. The runtime is not what will limit call throughput; the
carrier, the network and the model will.

**Parallel is 1.86× faster than serial** (16.4 µs vs 30.4 µs) at identical
allocation counts. The sharded registry and sharded scheduler are why: 16
goroutines mostly touch different shards. This is the opposite of the Phase 10F
evaluation runtime, where parallel was *slower* because per-item work was too
small to amortise coordination — here there is real work to spread.

Measured end-to-end in the stress test: **20 rounds × 250 calls through the full
lifecycle** with a sweep between rounds, zero leaked slots.

---

## 3. The registry does what it was designed to do

| Operation | ns/op | allocs |
|---|---:|---:|
| `Get` serial | 44.18 | 0 |
| `Get` parallel | **10.25** | 0 |
| `Len` | **0.3769** | 0 |
| `ByState` (5,000 calls) | 214,925 | 68 |

**`Get` is 4.3× faster under parallel load than serial.** Not a measurement
error — 64 shards mean 16 goroutines rarely collide, while the serial benchmark
hammers one core's cache. This is the sharding claim, measured.

**`Len` at 0.38 ns** is a single atomic load. This is read on every admission
decision, and deriving it by locking 64 shards would be the shape of defect
Phase 10F paid 45× for. Guarded by `BenchmarkRegistryLen`.

`ByState` walks every session and is 215 µs for 5,000 calls — 43 ns per call.
It runs **once per sweep interval**, never on the call path. One pass, not
fifteen: fifteen passes over thousands of sessions would be the same mistake in
a different disguise.

---

## 4. Admission is essentially free

| Path | ns/op | allocs |
|---|---:|---:|
| Admit + Release | 117.5 | **0** |
| Admit + Release parallel | 190.7 | **0** |
| **Shed** | **76.06** | 1 |

Zero allocations on the accept path. **The shed path is the important number**:
under overload the runtime spends its time refusing, and a refusal that cost
more than an admission would make the overload worse. At 76 ns a runtime can
refuse 13 million calls per second — the storm is bounded by the carrier's
ability to send, not ours to refuse.

Parallel admission is 1.6× *slower* than serial, correctly: admission takes a
single global lock, because check-and-reserve must be atomic or N goroutines all
take the last slot. That contention is the price of not over-admitting, and at
191 ns it is not worth a lock-free redesign.

---

## 5. What F4 cost, and how it was found

`BenchmarkTransition` before and after the sequencer fix:

| | Before | After | Factor |
|---|---:|---:|---:|
| ns/op | 17,511 | 3,390 | **5.2×** |
| B/op | 13,087 | 2,218 | **5.9×** |

The diagnosis came from a benchmark ratio that made no sense:
`BenchmarkHistoryAppendAtCap` was **three times faster** than
`BenchmarkTransition` while doing strictly more work — appending to a full
history that has to slide, rather than one that can simply grow.

A history at its cap stops growing. So did the copy that
`len(sess.History())` was making on every event. 128 records × ~100 bytes =
12.8 KB, matching the 13,087 B/op exactly.

The two benchmarks are now within 1.5% of each other (3,390 vs 3,343 ns), which
is what they should always have been: appending at the cap and below it are the
same work.

**The general lesson, third time in this platform:** an accessor that returns a
copy is a cost, and a caller that only wants its length pays that cost for
nothing. Phase 10F's `PendingApprovals` gauge (45×), Phase 10.5's audit of
`missesFor`, and now this.

---

## 6. Session and snapshot

| Operation | ns/op | B/op |
|---|---:|---:|
| `Snapshot` | 691.6 | 904 |
| `Restore` | 8,328 | 10,288 |
| `Context.Clone` | 668.2 | 776 |
| `Context.Validate` | 21.75 | **0** |
| `NewCallID` | 168.5 | 64 |

`Validate` at 21.75 ns with zero allocations runs on every inbound call and is
free.

`NewCallID` at 168 ns is the `crypto/rand` cost, paid once per call. Worth it:
a call identifier appears in URLs, webhooks and support tickets, and a guessable
one lets somebody enumerate calls that are not theirs. 168 ns against a 30 µs
lifecycle is 0.5%.

`Restore` at 8.3 µs is 12× `Snapshot`, because it rebuilds an FSM, clones the
context and copies the history. It runs once per recovered call at start-up, so
a runtime recovering 1,000 calls spends 8 ms doing it.

---

## 7. Maintenance paths

| Operation | Cost | Frequency |
|---|---:|---|
| `Sweep` (2,000 calls) | 197 µs | every `SweepInterval` (1 s) |
| `SnapshotAll` (1,000 calls) | 1.30 ms, 1.35 MB | every `SnapshotInterval` (10 s) and at shutdown |
| `MetricsSnapshot` | 26.0 µs | per scrape |

**Sweep at 197 µs per second is 0.02% of a core.** It bounds how often the
runtime can afford to enforce a deadline, and the answer is "far more often than
it needs to".

**`SnapshotAll` is the expensive one**: 1.3 ms and 1.35 MB for 1,000 calls —
1.35 KB per session, dominated by cloning history and context. At 10,000 live
calls a periodic snapshot would be 13 ms and 13.5 MB of garbage every 10
seconds. That is affordable but not free, and it is the number to watch as
concurrency grows. See §9.

---

## 8. Allocation profile

Zero-allocation paths — where the design intent shows:

| Path | allocs/op |
|---|---:|
| `Registry.Get` | 0 |
| `Registry.Len` | 0 |
| `Scheduler.Admit`/`Release` | 0 |
| `CallContext.Validate` | 0 |

The 199 allocations in a full lifecycle are legible: five transition records,
four events with cloned tag slices, a session, an FSM, a context clone, and the
metric label keys. None is a leak and all scale linearly with transitions.

---

## 9. Known limits and what is not measured

**`SnapshotAll` scales linearly and is the first thing to bind.** At 10,000 live
calls it is 13 ms and 13.5 MB per pass. If periodic snapshots become a problem,
the fix is incremental snapshotting — only sessions whose `UpdatedAt` moved
since the last pass — not a faster clone. Not implemented, because at present
concurrency it would be optimising an invisible cost.

**No provider latency is modelled.** `FakeProvider` returns immediately. A real
carrier adds tens to hundreds of milliseconds to `Accept`, which will dominate
`FullCallLifecycle` entirely — the 30 µs measured here will be under 0.1% of a
real call setup.

**No durable store is modelled.** `MemorySessionStore` is a map. Redis adds a
round trip per snapshot; Aurora adds more.

**Wall-clock figures are not comparable across sessions.** Phase 10.5 measured
~40% between-session variance on this machine against ±3% within a session.
Allocation counts are the durable measurement; nanoseconds on a laptop are not.
Every figure here comes from one session.

**Windows clock resolution is ~520 µs.** Every per-operation figure above is a
Go benchmark amortised mean over many iterations, so it is trustworthy — but any
*single* operation timed by the runtime's own histograms will quantise. The
`LifecycleLatency` bucket set deliberately extends below the resolution so the
overflow bucket is where the truth lives, and it is visible.

---

## 10. Related

- [ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md)
- [EVALUATION_REPORT.md](EVALUATION_REPORT.md)
