# ADR-0014: Correlation identity — one type, owned by packages/go/runtime

- **Status:** Accepted
- **Date:** 2026-08-16
- **Deciders:** repository owner
- **Consulted:** Phase 10.5 observability audit (finding O2); the frozen
  declarations in `telephony/ids.go` and `media/errors.go`, which name the fix
- **Informed:** anyone assembling a trace, or adding a subsystem

---

## Context

Production blocker **B3** is that nothing observable leaves the process.
[ADR-0013](0013-metrics-exposition-format.md) closed the metrics half. This ADR
closes the correlation half, and it is the half that required changing a frozen
module.

**Four subsystems each declared their own `CorrelationID`:**

| Module | Declaration | Minted |
|---|---|---|
| `toolruntime` | `ids.go` — `CorrelationID string` | `cor_…` |
| `governance` | `ids.go` — `CorrelationID string` | `cor_…` |
| `media` | `errors.go` — `type CorrelationID string` | `corr_…` |
| `telephony` | `ids.go` — `type CorrelationID string` | `corr_…` |

Four declarations of one concept are four unrelated Go types. Assigning one to
another requires a string conversion, and **a string conversion is exactly where
a correlation identity gets silently replaced by a different one** — to the
compiler they are just strings, so nothing objects. An end-to-end trace was
therefore not assemblable, which is the concrete cost B3 names.

This was not newly discovered. Phase 10.5 recorded it as finding **O2**, and
each subsystem that was forced to re-declare the type recorded the same
conclusion in the file where it did so. `media/errors.go`:

> *"The correct home remains packages/go/runtime, which is frozen. Recorded
> rather than repeated silently, and the recommendation is now four phases old."*

`telephony/ids.go` says the same thing and adds why it could not fix it itself:
telephony sits **above** conversation, governance and tool runtime, so importing
either existing definition would invert the dependency direction.

**Five phases recorded the problem and none could fix it, because the fix
required editing a frozen module.** That is the specific reason this ADR exists.

## Decision Drivers

- **One canonical identity**, not several string conventions.
- **Smallest possible change to frozen code.** Freezing exists to stop drift,
  not to make a recorded defect permanent.
- **No existing API, signature, field or call site may change.**
- **No dependency inversion and no cycle.**
- **No new third-party dependency.**

## Considered Options

1. **`runtime.CorrelationID`, with the four declarations becoming type aliases.**
2. Leave the duplication; correlate on raw strings at the boundary.
3. A new shared module owning the type, imported by the four.
4. Pick one existing declaration as canonical and have the others import it.

## Decision Outcome

**Chosen: Option 1.** `packages/go/runtime` gains `CorrelationID` and
`NewCorrelationID`. The four previous declarations become **type aliases**:

```go
type CorrelationID = runtime.CorrelationID
```

An alias is not a new type — it is another name for the same one. So
`telephony.CorrelationID`, `media.CorrelationID`, `governance.CorrelationID`,
`toolruntime.CorrelationID` and `runtime.CorrelationID` are now *literally the
same type*, and the compiler enforces it. Every existing field, signature and
call site keeps working **unchanged**, because an alias is transparent.

Each module's `NewCorrelationID` now delegates to `runtime.NewCorrelationID`, so
one function mints every correlation identity. Two minting functions is two
conventions, which is the problem restated.

**Why `runtime` is the correct home** — the reason the frozen modules gave
themselves: it sits at the bottom, every subsystem already depends on it
*directly*, and it depends on nothing but `packages/go/metrics`. No cycle is
possible.

Option 2 keeps the defect. Option 3 makes every subsystem take a new dependency
to solve a problem `runtime` already sits in the right place to solve. Option 4
inverts a dependency — telephony would import toolruntime, coupling a call
lifecycle to a tool executor.

### Exact scope of the frozen change

Deliberately minimal, and stated precisely so a reviewer can check it:

| Module | Change |
|---|---|
| `runtime` | **+1 type, +1 constructor, +1 method** — additive only |
| `telephony` | declaration → alias; constructor delegates; `import runtime` |
| `media` | same |
| `governance` | same |
| `toolruntime` | same |

Plus one consequence: each module's `func (c CorrelationID) String()` was
**removed**, because Go does not permit declaring a method on an alias to
another package's type. `String()` is inherited from `runtime.CorrelationID` and
behaves identically, so no caller changes.

**No signature changed. No field changed. No call site changed. No behaviour
changed** — every pre-existing test in all 41 modules passes unmodified.

### Consequences

**Positive**

- A trace assembles across subsystems: one subsystem's identity indexes
  another's audit trail directly, with no conversion.
- The compiler now enforces the identity. Reverting any module to its own named
  type fails to compile in `packages/go/correlation`, at the assertion, rather
  than at some distant call site that happens to bridge two subsystems.
- One minting convention and one prefix (`corr_`) platform-wide.
- A five-phase-old recorded defect is closed rather than re-recorded.

**Negative**

- **A frozen module changed.** That is a real cost and the precedent should be
  narrow: this was approved explicitly, is additive to `runtime`, and was the
  fix the frozen code itself named. It is not licence to edit frozen modules
  when something is merely inconvenient.
- The four aliases are now coupled to `runtime`'s definition. That is the point,
  but it does mean a change to the type is a platform-wide change.
- `governance` and `toolruntime` identifiers change prefix from `cor_` to
  `corr_`. No test asserted the old prefix, and identifiers are opaque, but a
  stored historical identifier keeps its original form.

**Neutral**

- `packages/go/correlation` is a new module holding the cross-subsystem
  conformance suite. It defines no mechanism — anything there would be the
  second convention this ADR removes.

### Confidence

**High.** The change is mechanical, the compiler proves the central property,
and every pre-existing test passes untouched. The design was specified by the
frozen code's own commentary rather than invented here.

### Revisit Trigger

Revisit when **any** of the following is first observed:

- A subsystem needs a correlation identity that is *not* a string — a structured
  W3C `traceparent`, for example. The alias would then have to become a real
  type with fields, which is a genuine migration.
- Correlation must cross a process boundary in a format this platform does not
  control, making the wire representation rather than the Go type the binding
  constraint.
- A tracing backend is adopted; its identity model may subsume this one.

## Scope explicitly NOT taken

**This ADR does not adopt tracing.** No OpenTelemetry, no tracing SDK, no
third-party dependency. `packages/go/telemetry` remains the untouched stub it
was — not implemented, not deleted. Correlation identity is the prerequisite for
tracing, not tracing itself, and conflating them is how a small correct change
becomes a large speculative one.

`middleware`, `outbox` and `persistence` carry a plain `string` correlation at
the HTTP and messaging layer. They were **not** changed: they sit outside the AI
plane, do not import `runtime`, and unifying them would be a second, larger
decision about the transport boundary.

## References

- `packages/go/runtime/ids.go` — the canonical declaration and its rationale
- `packages/go/telephony/ids.go`, `packages/go/media/errors.go` — the frozen
  commentary that specified this fix, preserved in place
- `packages/go/correlation` — compile-time identity proof and the
  four-subsystem propagation suite
- Phase 10.5 observability audit, finding O2; `ENGINEERING_AUDIT` §A1
- [ADR-0013](0013-metrics-exposition-format.md) — the metrics half of B3
- Supersedes: none. Superseded by: none.
