# Security Review — Phase 11A

**Scope:** `packages/go/telephony`

---

## 1. Why this module's threat model is different

Every other module in the AI plane handles data that arrived from somewhere.
This one sits at the **boundary with the public telephone network** — the only
place in the platform where an untrusted stranger can initiate work by dialling
a number.

That produces three risks the other phases do not have:

1. **The caller's number is the most sensitive identifier the platform
   touches.** Personal data under the DPDP Act, and the primary key of a
   person's identity to a telecom.
2. **An attacker controls the arrival rate.** Anyone can dial repeatedly.
3. **Carrier input is untrusted.** Callbacks arrive out of order, duplicated,
   and for calls that ended — and a provider adapter is written by whoever
   integrates the carrier.

Against that, the module has an unusually small surface: **it performs no I/O**.
No network client, no database driver, no filesystem access, no carrier SDK.
Everything outside is an interface a service opts into.

---

## 2. The number is structurally absent

`Endpoint` holds an **opaque `Ref`**, not an E.164 number:

```go
type Endpoint struct {
    Ref     string   // opaque upstream reference
    Display string   // "unknown", "contact", "blocked" — never a name
    Country string   // ISO-3166 alpha-2 — two characters cannot identify a person
}
```

**This is a structural control, not a convention.** The runtime does not need
the number to manage a lifecycle, and every place one could sit is a place it
would reach a snapshot, a Kafka event, a metric label and a log line.

Frozen invariant **I7**: events carry identifiers, never content. Kafka cannot
delete an individual record, so a number placed in a topic is retained for as
long as the topic is — regardless of what an erasure request later says.

The test applied to `Event` and `Snapshot` during design: *if this topic were
retained forever and could never be deleted, would that be a compliance
failure?* It must be no.

Resolution from `Ref` to a number belongs to the identity service, behind an
access check, on an audited path.

`TestEvents_CarryNoContent` renders every event from a full call and fails on
any endpoint reference. `Endpoint.String()` deliberately omits `Ref` when a
`Display` is present, because the rendered form reaches log lines.

---

## 3. Bounded inputs

Everything a provider adapter can supply is bounded, because a provider adapter
is the least trusted code that will call this module.

| Input | Bound | Why |
|---|---|---|
| Metadata entries | 32 | reaches snapshots and events |
| Metadata key / value | 64 / 256 chars | ditto |
| Tags | 16, 48 chars each | ditto |
| Session attributes | 32 entries | reaches the snapshot |
| Transition reason | 64 chars, `[a-z0-9_.]` | reaches Kafka |
| Provider identifier | 64 chars, `[a-z0-9_-]` | becomes a metric label and topic segment |
| Transition history | 128 records | a flapping call would grow without limit |

**Reason codes deserve their own note.** They are lowercase alphanumerics,
underscore and dot — no spaces, no punctuation, no free text. The obvious thing
for an adapter author to put in a hangup reason is whatever the carrier
returned, **and carriers return strings containing phone numbers**.
`TestSession_ReasonCodeIsBounded` rejects `"caller +91 98765 43210 hung up"`
explicitly.

This is the control Phase 10E added after its audit found reason codes were
unbounded free text on a path into an event stream. Here the risk is higher and
the control is applied from the start.

Provider identifier validation prevents a subtler problem: a character legal in
a Prometheus label but not a Kafka topic segment produces a failure at the far
end of the pipeline from the configuration that caused it.

---

## 4. Denial of service

The attacker controls the arrival rate. Four controls:

**Global capacity with a high-water mark.** `AdmissionHighWater` (0.95 by
default) keeps headroom so calls already in progress can still transition — a
runtime at exactly 100% cannot accept the transfer leg of a call it is already
carrying.

**Per-provider ceilings.** One carrier's outage-driven retry storm cannot
consume the whole runtime and take down calls on every other carrier.

**Shed, never queue.** A queue converts an overload into a latency problem that
spreads to calls that would have been served. Refusal costs **76 ns**, so the
runtime can refuse ~13 million calls per second — the storm is bounded by the
carrier's ability to send, not ours to refuse.

**Atomic check-and-reserve.** Checking capacity then reserving in two steps lets
N goroutines observe capacity for one slot and all take it — precisely under the
storm where it matters. The lock spans both.

Unbounded configuration is refused at construction:
`MaxConcurrentCalls = 0` produces *"an unbounded telephony runtime discovers its
limit during a call storm"*.

**Residual:** there is no per-caller rate limit. A single `Ref` dialling
repeatedly consumes capacity up to the global ceiling. That is deliberate — the
identity needed to rate-limit per caller is exactly the number this module
refuses to hold — and it belongs upstream, where the number is resolved. Worth
stating so it is a decision rather than an omission.

---

## 5. Untrusted carrier input

`CallDispatcher` classifies rather than errors:

| Outcome | Meaning | Is it an error? |
|---|---|---|
| `SignalApplied` | the transition happened | no |
| `SignalUnknownCall` | callback for a call already removed | **no** |
| `SignalNotApplicable` | not legal from the current state | **no** |
| `SignalRejected` | should have worked, did not | yes |

A dispatcher that errored on every late or duplicate callback would fill the
logs with entries nobody can act on, **and a real fault would be invisible among
them**. Log flooding is a denial-of-service vector against the operators.

The state machine is what makes this safe: a carrier cannot drive a call into an
illegal state, because the table refuses it. A replayed "connected" callback for
an ended call is discarded, not applied.

`TestDispatcher_ClassifiesLateAndDuplicateSignals` covers all three
non-error outcomes.

---

## 6. Identifier unguessability

`crypto/rand`, not `math/rand`. 10 random bytes per identifier, and a
`crypto/rand` failure panics rather than falling back:

```go
if _, err := rand.Read(buf[6:]); err != nil {
    panic("telephony: crypto/rand failed: " + err.Error())
}
```

A silent fallback to `math/rand` would make identifiers guessable, and a call
identifier appears in URLs, webhooks and support tickets — guessable ones let
somebody enumerate calls that are not theirs. Failing loudly is correct.

The 6-byte timestamp prefix is not secret and is not meant to be; it makes
identifiers sortable. The 80 bits of entropy after it are the security property.

**F3 is relevant here.** The original alphabet broke sortability, not
unguessability — the entropy was always 80 bits. The fix changed the encoding,
not the randomness.

---

## 7. Resource exhaustion

| Resource | Bound |
|---|---|
| Live sessions | `MaxConcurrentCalls`, enforced atomically |
| Sessions per provider | `MaxCallsPerProvider` |
| Transition history | 128 records per session, oldest dropped, first kept |
| Recorded events | 4,096, with `Dropped()` reporting truncation |
| Provider goroutines | `ProviderTimeout` bounds every provider call |
| Shutdown | `DrainTimeout` in real time — see F1 |
| Scheduler map | provider entries deleted at zero, so a runtime that served a thousand providers does not hold a thousand entries |

**`ProviderTimeout` is the one that matters most.** A provider adapter that
ignores context cancellation holds one goroutine per call; the deadline is the
last line of defence against a hung carrier SDK, and it is verified by
`TestFailure_ProviderTimeoutIsBounded`.

Stalled calls cannot accumulate: `SweepTimeouts` moves them to `timeout` and
`ReapTerminal` concludes them. Without the second, a call whose teardown never
completed would hold a slot forever.

---

## 8. Failure containment

Three subsystems can fail without taking calls with them:

| Failing | Effect |
|---|---|
| Publisher (Kafka) | events lost and counted; **calls connect, transfer and end normally** |
| Session store (Redis) | recoverability lost; the call proceeds |
| Provider reject/hangup | logged; the call still reaches its terminal state locally |

The publisher case is the important one and is argued at length in
`lifecycle.go`: failing a transition when its publish fails would mean a broker
outage **prevents calls from ending**, filling the registry until capacity is
exhausted. **Telemetry must never be load-bearing for the thing it observes.**

---

## 9. Findings

| ID | Finding | Severity | Status |
|---|---|---|---|
| S1 | No per-caller rate limit | medium | by design — belongs upstream where the number is resolved |
| S2 | Provider adapters are trusted to honour context | medium | mitigated by `ProviderTimeout`; a non-cancelling adapter still holds a goroutine until it fires |
| S3 | `Metadata` is opaque to validation beyond size | low | an adapter *could* put a number in a 256-char value; size-bounded but not content-checked |
| S4 | No encryption of snapshots at rest | low | correct here — belongs to the `SessionStore` implementation; `Snapshot` is a plain struct a backend may encrypt |
| S5 | `LivenessCheck` default loses in-progress calls silently | medium | safe direction; visible in `RecoveryAttempts{outcome}` but not forced |
| S6 | `-race` has never been run | **blocking** | inherited; nine modules, this the most concurrent |
| — | Secrets, credentials, keys | — | **none present** — the module performs no I/O |
| — | Phone numbers in events or snapshots | — | **structurally impossible** |

**S3 is the honest weak point.** `Endpoint.Ref` is opaque by contract, and
`Metadata` is bounded by size but not inspected. A provider adapter that put an
E.164 number in a metadata value would defeat the model. The bound limits blast
radius; it does not prevent the mistake.

**Recommendation:** state in the provider-adapter contract that `Metadata` is
for carrier diagnostics — trunk group, SIP response code — and never for party
identity. Consider a build-time or review-time check on adapters. A runtime
check cannot distinguish a number from any other digit string.

---

## 10. Out of scope

No SIP, RTP, WebRTC, audio, STT, TTS, LLM, fraud detection or emergency
detection — so none of their attack surfaces exist here. SIP in particular is a
large and historically vulnerable parsing surface, and its absence is worth
naming: **this module parses nothing from the network.**

---

## 11. Related

- [ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md)
- [TELEPHONY_ARCHITECTURE.md](TELEPHONY_ARCHITECTURE.md)
