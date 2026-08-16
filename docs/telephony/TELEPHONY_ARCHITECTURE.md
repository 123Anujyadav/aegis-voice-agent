# Telephony Runtime Architecture

**Phase 11A** · `packages/go/telephony` · 15 files, 4,588 lines, 78 tests,
24 benchmarks, 76.6% coverage.

---

## 1. What this is

Every phone call the platform handles is a `CallSession` owned by a
`TelephonyRuntime`. This module owns the call's state machine, its identity, its
context, its lifecycle, the events it publishes, and its recovery after a crash.

It sits between the carrier and the AI:

```
Carrier
  ↓
Provider Adapter          ← a sibling module, not this one
  ↓
Telephony Runtime         ← this phase
  ↓
Conversation Engine (10B) · Tool Runtime (10D) · Governance (10E) · Memory (10C)
```

## 2. What this is not

There is no SIP, no RTP, no WebRTC, no audio, no codec, no speech, no model.
Not "not yet" — those belong to other layers and their absence here is the
design.

The distinction that matters: **this module knows a call is connected. It does
not know what is being said, how the audio is carried, or which codec
negotiated.** A provider adapter reports that media is up; the runtime records
that the call reached `StateConnected` and tells everyone who needs to know. If
this package ever needs to parse an SDP body, something has gone wrong upstream
of it.

**No telephony library is used or required.** No Twilio, Exotel, Plivo, Vonage,
Asterisk, FreeSWITCH or Linphone SDK. The module's `go.mod` requires exactly two
things, both first-party and both dependency-free:

```console
$ cd packages/go/telephony && go list -deps ./... | grep callscreen
.../packages/go/metrics
.../packages/go/runtime
.../packages/go/telephony
```

The transitive closure is the Go standard library. A carrier SDK cannot leak in,
because the manifest does not admit one.

---

## 3. Subsystems

| Component | File | Owns |
|---|---|---|
| `TelephonyRuntime` | `runtime.go` | Start/Stop ordering, background loops, wiring |
| `CallCoordinator` | `runtime.go` | The front door — pairs admission with lifecycle |
| `CallDispatcher` | `runtime.go` | Provider signals, classified rather than errored |
| `CallScheduler` | `scheduler.go` | Admission, shedding, per-provider ceilings |
| `CallRegistry` | `registry.go` | Sharded session storage, O(1) census |
| `CallLifecycle` | `lifecycle.go` | Every state change, its metric and its event |
| `CallSession` | `session.go` | One call's mutable state, history, snapshot |
| `CallContext` | `context.go` | The immutable description of a call |
| State machine | `state.go` | Fifteen states, one declared transition table |
| Event runtime | `events.go` | Fourteen event types, the `Publisher` port |
| Recovery | `recovery.go` | `SessionStore` port, restore, liveness, sweep |
| `RuntimeMetrics` | `metrics.go` | Instruments, on the shared Phase 10.5 package |
| Config | `config.go` | Validated tuning, no environment access |
| Harness | `harness.go` | Exported fakes, per platform convention |

---

## 4. The central decision: no implicit transitions

The brief's hardest requirement, and it shapes everything else.

A call is in exactly one of fifteen states, and **every legal move is declared
in one table** (`state.go`, `transitionSpec`). A transition not in the table is
refused at run time; a terminal state with an outgoing edge, or a self
transition, is refused at **construction** by `runtime.NewFSM`.

There is no code path that assigns a state directly. `CallSession.Transition` is
the only mutator and it delegates to the frozen runtime's FSM.

**Why the FSM rather than a switch.** A switch encodes transitions in the places
that perform them, so "can a held call be transferred" is answered by reading
every call site. A declared table answers it in one place, and the answer is
testable — `TestState_TransitionTableIsComplete` walks every state and fails on
a missing entry, an undeclared destination, a self-transition, or a
non-terminal state with no way out.

---

## 5. Provider agnosticism, made checkable

`Provider` has four methods and no telephony vocabulary beyond dial, answer,
reject and hangup. There is deliberately no SDP, no codec negotiation and no
media description in those signatures: the moment one appears, this module has
acquired an opinion about how calls are carried.

Capability differences are handled generically. A carrier that cannot transfer
omits `CapTransfer`; the runtime returns `ErrCapabilityUnsupported` and **no
code anywhere names that carrier**. `TestLifecycle_CapabilityIsEnforcedGenerically`
builds a provider without hold or transfer and checks both are refused.

Capabilities are carried **per call**, not read from the registry at use time,
so a provider reconfigured mid-call cannot change what an in-flight call may do.

---

## 6. Concurrency

**Sharded registry.** 64 shards, selected by FNV-1a over the whole call
identifier. Hashing the whole string matters: identifiers begin with a
millisecond timestamp, so a prefix hash would put every call created in the same
millisecond on one shard — exactly the burst a call storm produces.
`TestRegistry_ShardsSpreadLoad` fails on an empty shard or one above 3× the mean.

Measured: `RegistryGet` is 44.18 ns serial and **10.25 ns under parallel load** —
faster in parallel, because the shards spread.

**`Len()` is O(1)** — an atomic counter, not a walk over 64 shards. Measured at
0.38 ns. This is read on every admission decision, and deriving it by locking
every shard would be the same shape of defect Phase 10F paid 45× for.

**`Each` never holds a shard lock while the callback runs.** Sessions are
collected under the lock, the lock is released, then the callback is invoked. A
callback that transitions a call — which is what the timeout sweep does — would
otherwise deadlock against registration, and only under load.
`TestRegistry_EachDoesNotHoldAShardLock` covers it.

**No package-level mutable state.** Two runtimes in one process share nothing.
That is what makes the suite parallel-safe and horizontal scaling a deployment
decision rather than a code change.

Verified: 2,000 calls through the full lifecycle across 50 goroutines, zero
failures, zero leaked capacity slots.

---

## 7. Ordering rules

Three orderings are load-bearing and each is the opposite of an obvious
alternative.

**Transition, then metric, then event.** The FSM moves first and nothing after
can undo it. If the broker is down, calls continue to connect, transfer and end;
events are lost and counted as lost.

The alternative — failing the transition when the publish fails — sounds safer
and is catastrophic: a broker outage would prevent calls from **ending**, every
call would stay Connected, the registry would fill, capacity would exhaust, and
an observability outage would become a phone-system outage. **Telemetry must
never be load-bearing for the thing it observes.**
`TestFailure_PublisherOutageDoesNotStopCalls` drives a full call to completion
with every publish failing.

**Provider first, then state — but only for answering.** Moving to Accepted
before the carrier confirmed would produce a session that believes it answered a
call the carrier never connected. Rejection and hangup are the opposite: the
platform has already decided, and holding a screening decision hostage to a
carrier's REST availability would be wrong.

**Start: recover, then open admission. Stop: refuse, drain, snapshot.** Recovery
runs before admission so a recovered call cannot lose its slot to a new one. The
snapshot runs after the drain so it captures only what genuinely could not
finish.

---

## 8. Admission: shed, do not queue

A queue is the obvious response to overload and it is wrong for telephony. A
caller waiting in a queue is a caller listening to silence, and by the time the
runtime reaches them the carrier has usually given up — the work is done twice
and satisfies nobody. Worse, a queue converts overload into a latency problem
that spreads to calls that would have been served promptly.

Shedding is honest: refused in ~76 ns, the carrier is told immediately and can
route elsewhere, and calls in progress keep their resources.

**Per-provider ceilings** exist for outage storms. A carrier having a bad day
retries aggressively; without a ceiling, one carrier's storm consumes the whole
runtime and takes down calls on every other carrier.

Admission and reservation are **one atomic step**. Checking then reserving lets
N goroutines observe capacity for one slot and all take it — precisely under the
storm where it matters.

---

## 9. Recovery

A restored session starts in `StateRecovery`, **not** its snapshotted state.

This is the central recovery decision. The obvious implementation puts the
session back where it was, and it is wrong: the snapshot says Connected, the
process that believed that is gone, and nothing has verified with the carrier
that the call is still up. A session restored directly into Connected would
report a live call that may have hung up minutes ago, and would emit metrics and
events for it.

`StateRecovery` says exactly what is true: this call existed, and we do not yet
know whether it still does. A `LivenessCheck` — which a deployment supplies,
because only the carrier knows — moves it to Connected or concludes it.

The default is `AssumeDead`, which loses in-progress calls but never resurrects
a dead one. **Resurrecting is the worse error**: the runtime then holds a
session, a capacity slot and a metric for a call nobody is on.

Recovery is deterministic — snapshots are processed oldest first — so two
recoveries of the same store produce the same event sequence and an incident can
be replayed.

---

## 10. Privacy by construction

`Endpoint` holds an **opaque `Ref`**, not an E.164 number. The runtime does not
need the number to manage a lifecycle, and every place one could sit is a place
it would reach a snapshot, a Kafka event, a metric label and a log line.

Frozen invariant **I7**: events carry identifiers, never content. A caller's
number is the most sensitive identifier the platform touches — personal data
under the DPDP Act, and Kafka cannot delete an individual record.

The test applied to `Event` and `Snapshot` during design: *if this topic were
retained forever and could never be deleted, would that be a compliance
failure?* It must be no. There is no field capable of holding a number, a name,
an audio reference or a transcript.

Transition reasons are **bounded codes**, not free text — lowercase
alphanumerics, underscore and dot, 64 characters. The obvious thing for an
adapter author to put in a hangup reason is whatever the carrier returned, and
carriers return strings containing numbers.

---

## 11. Ports, not implementations

| Port | Implemented here | Production implementation |
|---|---|---|
| `Provider` | `FakeProvider` (tests only) | a carrier adapter module |
| `Publisher` | `RecordingPublisher`, `NopPublisher` | `packages/go/eventbus` → Kafka |
| `SessionStore` | `MemorySessionStore` | Redis (hot) / Aurora (audit) |
| `LivenessCheck` | `AssumeDead`, `AlwaysLive` | a carrier query |

Nothing here reaches a network. The whole lifecycle, including recovery, is
testable with no infrastructure — the property every phase since 10A has
preserved.

---

## 12. Invariants

| ID | Invariant | Enforced by |
|---|---|---|
| INV-TEL-1 | A session is registered exactly once under a well-formed identifier | `CallRegistry.Register` |
| INV-TEL-2 | Direction matches the entry point used | `CallLifecycle.Incoming`/`Outgoing` |
| INV-TEL-3 | A transition reason is a bounded code | `checkReasonCode` |
| INV-TEL-4 | Session attributes are bounded in count and size | `CallSession.SetAttr` |
| INV-TEL-5 | A provider identifier is a valid metric label | `providerRegistry.register` |
| INV-TEL-6 | No transition occurs that the table does not declare | `runtime.FSM` |
| INV-TEL-7 | An admitted call always releases its slot | `CallCoordinator` |
| INV-TEL-8 | A restored session begins in Recovery | `Restore` |
| INV-TEL-9 | Events and snapshots carry no content | `Event`, `Snapshot` shape |
| INV-TEL-10 | Two runtimes share no state | no package-level mutables |

---

## 13. Related

- [CALL_LIFECYCLE.md](CALL_LIFECYCLE.md)
- [STATE_TRANSITIONS.md](STATE_TRANSITIONS.md)
- [SEQUENCE_DIAGRAMS.md](SEQUENCE_DIAGRAMS.md)
- [ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md)
