# Enterprise Telephony Runtime & Call Lifecycle — Documentation

**Phase 11A** · `packages/go/telephony` · Status: **PROPOSED — awaiting approval**

The runtime that manages every phone call lifecycle inside Aegis AI. Built from
scratch — **no Twilio, Exotel, Plivo, Vonage, Asterisk, FreeSWITCH or Linphone
SDK, and no telephony framework of any kind.**

---

## Documents

| # | Document | What it answers |
|---|---|---|
| 1 | [TELEPHONY_ARCHITECTURE.md](TELEPHONY_ARCHITECTURE.md) | What the runtime is, its subsystems, its ordering rules, ten invariants |
| 2 | [STATE_TRANSITIONS.md](STATE_TRANSITIONS.md) | The fifteen states and the complete declared transition table |
| 3 | [CALL_LIFECYCLE.md](CALL_LIFECYCLE.md) | The eight lifecycle paths, end to end |
| 4 | [SEQUENCE_DIAGRAMS.md](SEQUENCE_DIAGRAMS.md) | Seven sequences, each drawn from a named test |
| 5 | [ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md) | Brief compliance, four defects found and fixed, six open findings |
| 6 | [PERFORMANCE.md](PERFORMANCE.md) | 24 benchmarks, the 5.2× fix, and what is not measured |
| 7 | [SECURITY_REVIEW.md](SECURITY_REVIEW.md) | Why the number is structurally absent; DoS, untrusted carrier input |
| 8 | [EVALUATION_REPORT.md](EVALUATION_REPORT.md) | 78 tests measured — and what they do **not** establish |

---

## The short version

**No implicit transitions.** A call is in one of fifteen states, and every legal
move is declared in one table. A transition not in the table is refused at run
time; a malformed table is refused at construction. There is no code path that
assigns a state directly.

**Provider agnostic, checkably.** `Provider` has four methods and no telephony
vocabulary beyond dial, answer, reject and hangup — no SDP, no codec, no media
description. A carrier that cannot transfer omits `CapTransfer`, and no code
anywhere names that carrier.

**The caller's number is structurally absent.** `Endpoint` holds an opaque
`Ref`. The runtime does not need a number to manage a lifecycle, and every place
one could sit is a place it would reach a snapshot, a Kafka event, a metric
label and a log line. Frozen invariant I7, applied where it matters most.

**Telemetry is never load-bearing.** A broker outage loses events; it does not
stop calls from connecting, transferring or **ending**. The alternative would
turn an observability outage into a phone-system outage.

**Recovery does not assume a call survived.** A restored session starts in
`recovery`, not its snapshotted state — the process that believed it was
connected is gone, and nothing has asked the carrier. The default concludes the
call, because resurrecting a dead one is the worse error.

**Shed, never queue.** A caller waiting in a queue is listening to silence. A
refusal costs 76 ns.

---

## Numbers

| | |
|---|---:|
| Production lines | 4,588 |
| Tests | 78 |
| Benchmarks | 24 |
| Coverage | 76.6% |
| Third-party dependencies | **0** |
| Full call lifecycle | 30.4 µs |
| Registry lookup (parallel) | 10.25 ns |
| Admission refusal | 76.1 ns |
| Concurrent calls verified | 2,000 |

---

## Four defects found

| # | Defect | Found by |
|---|---|---|
| F1 | Graceful shutdown never terminated under an injected clock | the suite hanging at 120 s |
| F2 | A refused transfer still recorded a leg | a lifecycle test |
| F3 | Identifiers did not sort — a documented claim was false | `-count=3 -shuffle=on` |
| F4 | The sequencer copied 12.8 KB per event (5.2× fix) | a benchmark ratio that made no sense |

---

## Running it

```console
cd packages/go/telephony
go test ./...
go test -count=3 -shuffle=on ./...
go test -run XXX -bench=. -benchmem -benchtime=300ms .

# the boundary claim — two first-party requires, no third party
go list -deps ./... | grep callscreen
```

---

## Scope

**Implemented:** telephony runtime, call lifecycle, state machine, session,
context, events, recovery, scheduling, metrics.

**Deliberately not implemented:** carrier APIs, SIP, RTP, WebRTC, audio,
microphone, speaker, STT, TTS, LLM, fraud detection, emergency detection. Their
absence is the design, not a gap.

---

## Related

- [../hardening/](../hardening/) — Phase 10.5
- [../evaluation/](../evaluation/) — Phase 10F
- [../governance/](../governance/) — Phase 10E
- [../tools/](../tools/) — Phase 10D
- [../memory/](../memory/) — Phase 10C
- [../conversation/](../conversation/) — Phase 10B
- [../runtime/](../runtime/) — Phase 10A
