# ADR-0022 — The internal call driver: session ownership without a registry

**Status:** Accepted (implemented, Phase 16 T7)
**Date:** 2026-08-19
**Extends:** ADR-0017 (service wiring). **Depends on:** ADR-0021 (the plan-input seam).

---

## Context

Phase 16 T2 audited who owns each step of a call's life and found the same
answer nine times: nobody. Nobody minted a `ConversationID`, constructed a
`Pipeline`, sent `EventStart` or terminated a conversation. T6 gave the greeting
a route into the speaking path; nothing called it. Every conversation in the
repository was opened by a test.

## Problem statement

Where does a call's identity, lifetime and teardown live, in a service that must
not grow a session registry, a second dialogue engine, or global state?

## Constraints

- No global session registry, package-level map, or mutable global state.
- No second `Engine`, `Planner` or `Classifier`; the Bridge is the only route to
  a conversation.
- The component must live under the existing `platform.Runner` contract — no
  second goroutine supervisor.
- No API key, model, network dependency or external binary.
- Frozen modules unchanged.

## Decision

**1. `CallDriver` is a `platform.Runner`; `Call` is the session.**

`Place` returns a `*Call`, and that value *is* the call. There is no lookup, no
identifier-to-session map and no registry anywhere in the service, so a stale
identifier cannot resolve to a live session and two calls cannot reach each
other's state.

**2. Lifetime is carried by context, not by a list.**

`Run` publishes the service's context; every call's pipeline is started under a
child of it. Shutdown therefore reaches every live call without the driver
remembering any of them — and there is no list to prune correctly on each exit
path, which is where registry bugs live.

**3. One identifier per call, minted by the driver, shared by both layers.**

`voice.NewSessionID()` — the existing production minter — and the conversation
takes the same value. Two identifiers for one call is two things to get out of
step.

**4. Per-call ports are produced per call.**

`NewCallPorts` supplies the audio analyser, the media sink and the observer for
each call. The registry, generator and governor stay process-wide. Sharing the
first three would put two callers' audio in one sink and one caller's speech
onset in another's turn.

**5. The driver sends `EventStart` and `EventHangup`, and nothing else.**

Completion events mean "the audio finished". `Pipeline.Speak` owns them
(ADR-0021). `Greet` blocks until the greeting has actually been delivered, which
is what makes "then accept caller utterances" safe.

**6. The production build registers no driver.**

A call needs providers, and every shipped provider shells out to an external
binary or a model while this phase remains no-model/no-network (ADR-0017).
`withCallDriver` is the seam a deployment with providers uses. It is production
code, not a test helper, so the wiring a real deployment performs is the wiring
that is exercised.

## Alternatives rejected

- **A session registry keyed by `ConversationID`.** It would be a second place
  session identity lives, would need correct pruning on every terminal path, and
  would let any code holding an identifier reach a live session.
- **A driver-owned supervisor goroutine.** `platform.Service` already supervises
  runners; a second one would make shutdown ordering ambiguous.
- **Constructing a `conversation.Engine` in the driver.** Two engines in one
  process, dialogue configuration in a service, and — as the mutation campaign
  showed — an engine with no classifier, so every intent falls back.

## Trade-offs

- **The driver is not reachable in the shipped binary.** Honest, and asserted by
  a test rather than left as a comment; it becomes reachable the moment a
  deployment supplies providers.
- **`Greet` blocks.** A caller wanting concurrency runs it in their own
  goroutine; the alternative — returning early — is the M3c defect.
- **Calls are not enumerable.** Nothing can ask "what calls are live". That is
  the price of having no registry, and shutdown does not need the answer.

## Operational impact

Two runners instead of one where a driver is registered, stopped in reverse
order: calls end before the bridge that produced their conversations.

## Security impact

No transcript, no PCM, no credential, no persistence, no unbounded queue, no
global mutable state. The driver logs identifiers only.

## Migration strategy

Purely additive. `buildService` takes variadic options; `run()` passes none, so
the production path is byte-for-byte the behaviour ADR-0017 described.

## Risks

A deployment that supplies providers but no `GreetingSystem` will generate its
opening turn from the session instruction. Documented, not defaulted.

## Future review trigger

Review when a real inbound transport is integrated — at that point something
must map an incoming call to a `Place` call, and whether *that* needs a registry
is a question this ADR deliberately does not pre-answer.

## References

- `services/go/voice/cmd/server/driver.go`
- `docs/phase16/CALL_DRIVER.md`
- `docs/phase16/LIFECYCLE_OWNERSHIP_AUDIT.md` — the T2 table this closes
- ADR-0017, ADR-0021
