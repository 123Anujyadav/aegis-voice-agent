# ADR-0026: Distributed tracing: W3C Trace Context and a stdlib span model, backend deferred

- **Status:** **Proposed, not accepted.** This ADR records a recommendation for
  the owner to decide. Nothing in it is in force, and no code implements it.
- **Date:** 2026-10-23
- **Deciders:** the repository owner (architecture authority)
- **Consulted:** _none yet_
- **Informed:** SRE (owns `/infra/`); anyone wiring a service seam or a
  latency alert
- **Depends on:** ADR-0011, ADR-0012, ADR-0013, ADR-0014

**Numbering.** This is the first number that nothing uses or claims. On `main`,
ADRs 0000–0017 exist. Unpublished Phase 16 and Phase 18 ADRs use 0018–0025 in
the owner's working tree; their index lists 0024 as Accepted. Open PR #44 also
proposes a 0024 ("one Kubernetes manifest tree"), so that number is claimed
twice. No file, branch, commit or open PR uses 0026 or any higher number. The
owner's own closure audit records "no ADR-0026". The owner may renumber this
proposal when accepting it, as PR #44 also allows.

**What this ADR does not claim.** Tracing does not exist on the platform today.
- **Instrumentation:** no tracer implementation exists. No production code path
  emits a span. No `traceparent` is parsed or propagated.
- **Backend:** none is chosen, configured or credentialed.
- **Status:** this ADR is a proposal and a plan. It changes no code, and merging
  it does not accept it.

---

## Context

Tracing has been deferred twice, each time for a stated reason, and both
reasons are now resolved or narrowed.

- **ADR-0013** chose metrics exposition and deferred tracing: *"This ADR does
  not deliver tracing. Observability remains half-solved, and that must not be
  reported as complete."* It blocked trace correlation (T6) on a single
  correlation identity, which required editing frozen `runtime`.
- **ADR-0014** then settled that: `runtime.CorrelationID` is the one type, and
  `runtime.NewCorrelationID` is the one minter. It stated *"Correlation identity
  is the prerequisite for tracing, not tracing itself"*, and listed *"A tracing
  backend is adopted"* and *"a structured W3C `traceparent`"* as revisit
  triggers.
- **ADR-0011 §9**, Accepted, already commits the platform to tracing: *"Every
  turn is traced with a span per hop, named for the hop numbers in §5.2. A p95
  breach must be attributable within minutes."* Nothing implements it today, so
  §5.2's per-hop budget can be checked only end to end.

What exists today:

| Piece | State on `main` |
|---|---|
| `runtime.Tracer` / `runtime.Span` (frozen `runtime`, `kernel.go`) | **The platform's tracing port.** Documented as *"the subset of OpenTelemetry the runtime actually uses. An adapter in a sibling module implements it over the real SDK."* `Span` has `SetAttribute`, `RecordError` and `End`, and its doc forbids `PERSONAL`/`SENSITIVE` attributes. The kernel opens one span, `runtime.generate`, with `request_id`, `session_id`, `tier`, `class` and `model` attributes. The only implementation is `NoopTracer`, and `runtime.WithTracer` has **0 callers**. No code outside `runtime`'s own tests constructs a `Kernel` (`runtime.New`), so that span runs in **no production path** today. |
| `packages/go/telemetry` | A stub with **zero importers**. `InitializeOpenTelemetry` prints a line and returns no-ops; `Tracer.StartSpan(ctx, name) (ctx, func())` carries no attributes, status or parent. |
| `platform.WithTraceID` / `TraceIDFrom`, `AttrTraceID = "trace_id"` | Logging plumbing that adds `trace_id` to every log record when the context carries one. **0 callers** set it. |
| `middleware.WriteProblemDetails(…, traceID)` | Echoes a caller-supplied string into problem details; no trace exists to supply. |
| Third-party tracing dependencies | None. ADR-0015 (pgx) ended the zero-dependency property deliberately, for one driver, behind a port. |
| Tracing backend, collector, credential | **None chosen.** ADR-0008 names no observability backend. |

Two constraints carry over unchanged:

1. **The freeze.** 13 modules are frozen, including `speech`, `conversation`,
   `media`, `telephony`, `runtime` and `metrics`. A span *inside* a frozen
   module is a frozen-module change. Spans can be opened without one only at
   seams owned by non-frozen code: `platform`, `voice`, `voiceintel`,
   `middleware` and the services.
2. **ADR-0012 privacy.** Traces are classified `INTERNAL` with a fixed 30-day
   retention. Log redaction is enforced in the handler, and there is no path
   from a log call around it. Span attributes are a new path to the same data,
   so they need the same guarantee.

## Decision Drivers

- **D1:** Make ADR-0011 §9 implementable: per-hop timing for every turn, joined
  across processes.
- **D2:** Do not choose a backend, collector or vendor without the owner or SRE.
  Do not require a credential.
- **D3:** Edit no frozen module.
- **D4:** Route span attributes through the same redaction as logs (ADR-0012).
- **D5:** Add a third-party dependency only for a reason its own ADR records.
- **D6:** Keep correlation identity (ADR-0014) and trace identity distinct, and
  both visible.

## Considered Options

1. **Keep deferring.**
2. **A W3C Trace Context span model in the standard library, exporter behind
   an interface, log exporter first; backend adapter later.**
3. **OpenTelemetry Go SDK with an OTLP exporter, now.**
4. **A vendor SDK (AWS X-Ray, Datadog).**

## Decision Outcome

**Recommended: Option 2.**

- **Wire format: W3C Trace Context.** `traceparent` is version `00`, a 16-byte
  trace-id, an 8-byte parent-id and flags, all lowercase hex. It is the format
  every candidate backend accepts, including OTLP, X-Ray via its collector, and
  Datadog, so choosing it now forecloses no backend. Inbound values are
  validated strictly and replaced when invalid. This is the same rule the
  correlation-ID middleware applies.
- **The port is `runtime.Tracer`. The implementation lives in
  `packages/go/telemetry`, which is not frozen.** Frozen `runtime` already
  defines the port and expects "an adapter in a sibling module". Reusing it
  needs no frozen change, for three reasons:
  - `Tracer` and `Span` are exported interfaces. A Go type satisfies an
    interface structurally, so a type in `telemetry` implements them without
    any declaration in `runtime` changing.
  - `runtime.WithTracer` is an existing exported `Option` of `runtime.New`.
    Supplying the implementation uses the frozen API as it stands.
  - `runtime`'s own span sites, such as `runtime.generate` (ADR-0011 hop 6),
    already call the port. They record as soon as a kernel is built with a real
    tracer, without editing them.

  Today nothing outside `runtime`'s tests builds a kernel, so this turns on no
  production span by itself. It makes such spans possible the day a service
  constructs one.
  - Each span records a name, trace-id, span-id, parent, start and end times,
    a status, and an allowlisted set of attributes. IDs come from
    `crypto/rand`.
  - The stub's own `Tracer` interface, a second and incompatible port,
    together with `NoopMetricsExporter` and `InitializeOpenTelemetry`, has zero
    importers. These are replaced rather than extended. ADR-0013 left the
    stub's fate to this decision, and `metricsexport` already supersedes its
    metrics half.
  - `telemetry` will import `runtime`. That cannot form a cycle, because
    `runtime` depends only on `metrics`.
- **One exporter interface, one implementation first: structured log
  records** through `platform`'s logger. Every span then passes the existing
  redaction handler (D4) and lands wherever logs already go, with no new
  backend (D2). A real backend becomes one adapter behind the interface later.
  This mirrors how `metricsexport` adapts `metrics`.
- **Trace identity is not correlation identity.** The correlation ID stays the
  business key for a call or request (ADR-0014). The trace-id is the
  distributed-timing key. Each span carries the correlation ID as an attribute,
  and `platform.WithTraceID` populates the existing `trace_id` log attribute.
  Neither is derived from the other (D6).
- **Spans at seams only.** Hops whose work happens inside frozen modules are
  timed at the non-frozen seam around them. A hop that has no such seam is
  reported as uninstrumented. It is never faked, and is never instrumented
  through a frozen edit without a freeze exception (D3).

Option 1 leaves an Accepted ADR (0011 §9) unimplementable indefinitely. Option 3
brings the largest dependency tree in the repository (D5) and an exporter with
nowhere to send to (D2). Its value is almost entirely the backend integration,
which nobody has chosen. Option 4 needs a credential and selects a vendor, so
it fails D2 on both counts.

### Consequences

**Positive**

- ADR-0011 §9 becomes implementable, and per-hop latency becomes attributable.
- No dependency, no credential and no backend are introduced. No frozen module
  changes.
- Any kernel built with the tracer records its existing `runtime.generate`
  span, with no change to `runtime`. No production code builds a kernel yet.
- The misleading `InitializeOpenTelemetry` stub, which reports configuring
  OpenTelemetry and configures nothing, goes away. With it goes the second,
  incompatible `Tracer` port.
- Every span is redacted by construction, through the same handler as logs.

**Negative**

- The span model and the `traceparent` codec are hand-written and must stay
  correct. This is mitigated by the W3C specification's test vectors and by a
  small surface.
- Spans exported as log records are queryable only as well as the log store
  allows. There is no waterfall view until a backend adapter exists.
- Seam-level spans are coarser than in-module spans. Some ADR-0011 hops may
  remain uninstrumented until the freeze lifts or an exception is granted.

**Neutral**

- Choosing a backend remains a separate owner and SRE decision. OTLP via the
  OpenTelemetry SDK remains the expected adapter, with its own ADR when chosen.
- Head sampling is always on in process, since ADR-0011 says *every* turn.
  Export-side sampling is a backend-cost decision and is deferred with the
  backend.

### Confidence

**Medium.** The format choice is high-confidence: W3C Trace Context is the
interoperable standard, and every option converges on it. The split between
model and exporter is reversible, because one adapter swaps the exporter. The
open question is coverage: which ADR-0011 hops have non-frozen seams can only
be settled hop by hop in step 3 of the plan.

### Revisit Trigger

- A tracing backend is chosen. Add its adapter, and its dependency ADR if it
  brings one.
- Span volume makes the log store the bottleneck or the dominant cost.
- The freeze lifts, so spans can move inside the modules that do the work.
- A peer the platform does not control requires a propagation format other
  than W3C, for example a carrier's own header.

## Implementation plan (only after acceptance; one small PR per step)

| Step | Scope | Acceptance criteria | Blocked on |
|---|---|---|---|
| 1 | `packages/go/telemetry`: a `runtime.Tracer` and `runtime.Span` implementation, `traceparent` parse and format, `crypto/rand` IDs, an `Exporter` interface and a log exporter. Replace the stub's zero-importer declarations. | A compile-time assertion shows the type satisfies `runtime.Tracer`. W3C test vectors pass. Malformed, all-zero, upper-case and oversized `traceparent` values are rejected. IDs are unique under concurrency (`-race` in CI). Attributes outside the allowlist are dropped. golangci-lint is clean. | Owner acceptance |
| 2 | `platform` and `middleware`: inbound `traceparent` extraction, or a new root span; outbound injection helper; `WithTraceID` set. | Valid inbound context propagates unchanged and invalid context is replaced. `trace_id` appears on log records. The health listener is unaffected. | Step 1 |
| 3 | Inject the tracer with `runtime.WithTracer` wherever a service builds a kernel (none does today). In the `voice` and `voiceintel` seams, add one span per ADR-0011 hop that a non-frozen seam can time, named for the §5.2 hop number. | Wherever a kernel is built, `runtime.generate` spans appear as children of the turn's span. A table maps every §5.2 hop to "instrumented at `<seam>`" or "uninstrumented: no non-frozen seam". Tests assert span names and parentage for a simulated turn. | Step 2; per-hop seam inventory |
| 4 | Backend adapter, for example OTLP via the OpenTelemetry SDK. | The adapter's own ADR, covering dependency, endpoint, credential handling and ADR-0012 egress and retention. | **Owner and SRE backend choice; credential provisioning** |

Out of scope for every step: alert rules and dashboards (operator artefacts,
as in ADR-0013), sampling policy beyond always-on head sampling, and any
change to a frozen module.

## Options in Detail

### Option 1: keep deferring

- **Good:** no work and no risk.
- **Bad:** ADR-0011 §9 stays unimplementable. A p95 breach cannot be
  attributed to a hop. ADR-0014's prerequisite was built for nothing.

### Option 2: W3C Trace Context, stdlib span model, log exporter first *(recommended)*

- **Good:** no dependency and no credential. It is backend-neutral, redacted by
  construction and reversible, and it reuses existing logging plumbing.
- **Bad:** a hand-written codec, no waterfall view until an adapter exists, and
  coarser seam-level spans.

### Option 3: OpenTelemetry SDK and OTLP now

- **Good:** the canonical implementation, with rich tooling once a backend
  exists.
- **Bad:** the largest dependency tree of any option, an exporter with no
  backend to send to, and a decision bundled with backend choice.

### Option 4: vendor SDK

- **Good:** an integrated backend with no infrastructure to run.
- **Bad:** it needs an ingest credential, selects a vendor without the owner,
  and egresses operational data to a third party (ADR-0012).

## Approval required

| Decision | Authority | Status |
|---|---|---|
| Accept, amend or reject this ADR, and its number | repository owner | **pending** |
| Replace the `packages/go/telemetry` stub's declarations | repository owner | pending; part of acceptance |
| Tracing backend and its credential | owner and SRE | **not requested here** |
| Any freeze exception for in-module spans | repository owner | **not requested here** |

## References

- ADR-0011 §5.2 and §9: the per-hop budget and the commitment to trace it
- ADR-0012: `INTERNAL` classification, 30-day retention and handler-level
  redaction for telemetry and traces
- ADR-0013: metrics exposition; tracing deferred; the `telemetry` stub's fate
  left to this decision
- ADR-0014: correlation identity, the stated prerequisite for tracing
- `packages/go/runtime/kernel.go`: the `Tracer` and `Span` port,
  `NoopTracer`, `WithTracer`, and the `runtime.generate` span
- `packages/go/telemetry/telemetry.go`: the stub
- `packages/go/platform/logging.go`: `WithTraceID`, `TraceIDFrom`,
  `AttrTraceID`
- W3C Trace Context Recommendation: the `traceparent` and `tracestate` headers
