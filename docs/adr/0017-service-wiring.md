# ADR-0017 — Wiring the voice intelligence layer into a running service

- **Status:** Accepted (Phase 14 T1)
- **Date:** 2026-08-17
- **Supersedes:** nothing
- **Related:** [ADR-0016](0016-intent-classification.md),
  [Phase 14 dependency map](../phase14/DEPENDENCY_MAP.md)

## Context

Phase 13 delivered `packages/go/intent` and `packages/go/voiceintel`, both
CI-verified. They are **imported by nothing**. `voiceintel` is a leaf module, so
no caller experiences the intelligence it provides.

Inspection established (see the dependency map for evidence):

- All 16 services are lifecycle scaffolds. **None calls `svc.Register(...)`;
  none implements a `Runner`; none imports voice, conversation or speech.**
- `voice.NewPipeline` and `voice.NewSessionFSM` are constructed **nowhere in
  production**.
- `conversation.NewEngine` appears in production exactly once, in
  `voiceintel/bridge.go` — Phase 13's own code.
- `platform.Service.Register(r Runner)` already exists as the sanctioned
  component seam, with `platform.Service` owning start/stop ordering.
- Every shipped voice provider shells out to an external binary or model.

## Decision

### 1. Where `voiceintel` is instantiated

In **`services/go/voice/cmd/server/main.go`**, inside `run()`, after config and
logger are built and before `svc.Run(ctx)`. Constructed once per process.

Rejected: package-level singleton, `init()`, or lazy global. All are forbidden
by Phase 14 and would defeat `TestT10_BridgeHoldsNoCrossSessionState`.

### 2. Who owns its lifecycle

A **`Runner`** registered via `platform.Service.Register`. The Runner receives
the Bridge by **constructor injection** and implements `Name`/`Run`/`Shutdown`.
`platform.Service` owns startup order, failure propagation and drain — the
existing contract, unchanged.

The Bridge itself owns no goroutine, so `Shutdown` has nothing to tear down
beyond releasing its reference; the per-conversation engines are owned by
`conversation.Engine` and die with it.

### 3. How session IDs are passed

As `conversation.ConversationID` (a string), supplied by the caller to
`Bridge.Planner(id, persona)`. Phase 14 introduces **no session registry and no
ID generator of its own** — `voice.NewSessionID()` already exists for callers
that need one.

### 4. How the Planner is injected

It is not injected — it is **produced**. `Bridge.Planner(id, persona)` returns a
`voice.Planner`, which is precisely the interface `voice.PipelineConfig.Planner`
expects and which `voice` already asserts `*conversation.Conversation`
satisfies (pipeline.go:123). No adapter, no shim.

### 5. How errors propagate

- Construction failure in `run()` → returned error → `main` prints and exits
  non-zero. A service that cannot build its intelligence must not start
  pretending it has one.
- Per-session failures stay **session-scoped**: typed errors from
  `conversation` (`ErrTerminal`, `ErrInvalidTransition`, …) returned to the
  caller of `Handle`. They must **not** be promoted to service-wide failure.
- A `Runner.Run` error triggers service shutdown, so the Runner returns an error
  only for genuinely unrecoverable conditions — never for one bad turn.

### 6. Whether one classifier/config is shared safely

**Yes, and it is the intended shape.** One `Bridge`, one immutable
`intent.Classifier`, one config, shared across all sessions.

Evidence, not assumption: the classifier holds no mutable state (AST-guarded, no
`*Classifier` method assigns to a receiver field); 64 goroutines against a
shared classifier match a serial baseline; 16 concurrent sessions stay isolated
under classification, context churn, cancellation, interruption and termination.
CI-verified under single and repeated shuffled `-race`, with no data race
reported.

### 7. How per-session `ContextEngine` stays isolated

Untouched. Every `conversation.Conversation` constructs its own `ContextEngine`
(engine.go:250); the Bridge holds exactly one field, `*conversation.Engine`.
Phase 14 adds **no** context or memory system, and the existing structural guard
already fails the build's tests if the Bridge grows a map/slice/sync field.

### 8. Scope boundary — the pipeline's provider half is deferred

A full `voice.Pipeline` needs a `ProviderRegistry`, and every shipped provider
requires an external binary or model. Under Phase 14's policy (no model
download, no external service, no API key) that is not constructible in a
running service.

**Phase 14 therefore wires the intelligence path**:

```
service run() → voiceintel.Bridge → voice.Planner → conversation.Engine
              → intent.Classifier → deterministic intent → Plan
```

and **defers the audio-bearing stages** (`audio → STT → … → TTS → audio`) until
providers are provisioned. This is recorded as a limitation, not disguised as
completeness.

## Consequences

**Positive.** The intelligence path becomes reachable from a real service
entrypoint through the existing seam. One file changes. No frozen module, no CI
change for T2, no new dependency, no new listener, no new state machine, no
second context system. `services/go/voice` becomes the first service in the
repository to register a `Runner` — establishing the pattern the other fifteen
scaffolds will need.

**Negative / accepted.** The service will start, construct and expose the
intelligence path but will not process audio, because no provider can be
constructed. A reader could mistake "wired" for "handling calls"; the
documentation states the boundary explicitly. `services/go/voice`'s dependency
graph widens to include voice/conversation/intent and their transitive set —
intended, and still zero third-party.

**Deferred.** Provider provisioning; adding a `services/` entry to hardening's
`AI_MODULES` (T13 will stop for approval rather than edit CI).

## Alternatives considered

Full pipeline construction (needs providers); an in-process fake provider in
production code (test doubles in production); a Bridge singleton (forbidden, and
defeats an existing guard); a new HTTP endpoint or listener (no requirement,
explicitly forbidden); wiring a different service (all equally bare — `voice` is
the name-matched owner). Each is recorded with its rejection reason in the
dependency map.
