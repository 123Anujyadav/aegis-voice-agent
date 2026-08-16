# ADR-0013: Metrics exposition — Prometheus text format, stdlib adapter

- **Status:** Accepted
- **Date:** 2026-08-16
- **Deciders:** repository owner
- **Consulted:** Phase 12 discovery brief; `packages/go/metrics` design record
- **Informed:** anyone wiring a service listener or building a dashboard

---

## Context

Phase 10.5 resolved finding A1 by replacing six divergent metric implementations
with one: `packages/go/metrics`. Ten modules now expose
`Registry() *metrics.Registry` — `runtime`, `conversation`, `memory`,
`toolruntime`, `governance`, `evaluation`, `audiointel`, `media`, `telephony`
and `voice`.

**Nothing leaves the process.** That is production blocker **B3**: every
instrument is recorded, and none is observable from outside. A dashboard cannot
be built, an alert cannot fire, and a latency regression is invisible until it
becomes an incident.

Three facts constrain the answer, and all three predate this ADR:

1. **`packages/go/metrics` is frozen and deliberately network-free.** Its own
   module header states the rule: *"It does not export to Prometheus,
   OpenTelemetry, or anything else. `Snapshot()` returns plain values and an
   adapter converts them. Instrument code must not acquire a network dependency;
   that is the whole reason the AI phases were able to keep their supply chain at
   zero."* The exporter must therefore live outside `metrics`.

2. **`metrics.Sample` is already shaped for Prometheus.** It carries
   `Kind/Name/Labels/Value/Bounds/Buckets/Count/Sum`, and `Buckets` is documented
   as cumulative-at-or-below — *"the convention Prometheus expects and the one
   that makes a quantile computable by a consumer without the raw observations."*

3. **A merged scrape was designed for.**
   `evalsubjects/observability_test.go` asserts that instrument names do not
   collide across subsystems, existing specifically because *"two subsystems can
   now genuinely register the same name into a combined exporter."*

Instrument names are already valid Prometheus names
(`governance_decisions_total`, `voice_sessions_opened_total`), and label names
are a closed, bounded vocabulary (`reason`, `outcome`, `from`, `to`, `kind`,
`provider`, `stage`, `action`, `basis`).

ADR-0008 puts the workload on **EKS**, where Prometheus scraping is the default
idiom. ADR-0012 (DPDP) forbids content and per-subject identifiers leaving the
process, which binds what may appear as a label value.

The platform currently has **zero third-party Go dependencies**. That is not an
accident; it is why `govulncheck` reports only standard-library advisories.

## Decision Drivers

Most important first:

- **Do not modify a frozen phase.** 10A–10F, 10.5 and 11A–11E are closed.
- **Do not end the zero-dependency property.** It is the platform's cheapest
  security property and the hardest to recover once lost.
- **Do not leak content or per-call identifiers.** ADR-0012 is a legal
  obligation, not a preference.
- **Bounded label cardinality.** An unbounded label set is how a metrics
  backend becomes an outage.
- **Metrics exposition must not be able to fail a call.** Observability is not
  on the call path and must never become a dependency of one.
- **Fit the deployment already chosen** (EKS, ADR-0008).

## Considered Options

1. **Prometheus text format v0.0.4, stdlib-only adapter** — a new small module
   converts `[]metrics.Sample` to text; served on the existing health mux.
2. **Prometheus via `prometheus/client_golang`** — adopt the official library.
3. **OpenTelemetry SDK, OTLP push, metrics and traces together.**
4. **Hosted SaaS backend** (Datadog / New Relic / managed OTel).

## Decision Outcome

**Chosen: Option 1 — Prometheus text exposition format v0.0.4, produced by a
standard-library-only adapter in a new module, served at `GET /metrics` on the
existing `HealthPort` mux.**

**This ADR is scoped to metrics exposition only. Tracing is explicitly deferred.**

It is the only option satisfying every driver at once. It touches no frozen
module — the adapter reads `Registry.Snapshot()` from outside, which is
precisely the arrangement `metrics` was designed for. It adds no third-party
dependency: emitting the text format is string formatting, and the library's
real value is its *instruments*, which this platform already replaced. It reuses
a listener that already exists rather than opening a second one.

Option 2 would end the zero-dependency property to avoid writing a formatter.
Option 3 bundles a deferrable decision with a blocking one and immediately meets
the frozen-module problem below. Option 4 is the only option requiring a
credential, and is rejected on that basis alone for a decision that needs none.

### Tracing is deferred, and why

`packages/go/telemetry` is a stub with **zero importers**:
`InitializeOpenTelemetry` prints a line and returns no-ops, and its
`MetricsExporter` has no histogram method, so it structurally cannot carry a
`metrics.Sample`. It is left **entirely unchanged** by this decision — not
implemented, not deleted.

**T6 (trace correlation) is blocked by a frozen-module constraint, and this ADR
does not unblock it.** Correlation identity is currently six declarations across
five modules (`audiointel.CallID`, `media.CorrelationID`, `runtime.RequestID`,
`telephony.CallID`, `telephony.CorrelationID`, `voice.CallID`). The frozen code
states the diagnosis itself, in both `telephony/ids.go` and `media/errors.go`:

> *"The correct home is `packages/go/runtime`, which every module already
> imports — and that module is frozen. Recorded rather than repeated silently…
> the recommendation is now four phases old."*

A single correlation identity therefore requires editing frozen `runtime`, or
creating a shared module **and** editing frozen modules to import it. Both are
outside current authority. A future ADR must settle it; **metrics exposition
does not wait on it.**

### Where the adapter lives

**A new module, `packages/go/metricsexport`**, rather than inside
`packages/go/platform`.

`platform` is the service host and is imported by every service, including
services outside the AI plane. Putting the exporter there would force any
`platform` consumer to link it. A separate module keeps `platform`'s dependency
set unchanged and lets a non-service caller — a test, a CLI, a future admin tool
— render an exposition without importing a service runtime. This mirrors the
reasoning `metrics` itself records for not living in `runtime`.

The module depends on `packages/go/metrics` and the standard library. Nothing
else.

### Consequences

**Positive**

- B3's metrics half closes: instruments become observable outside the process.
- Zero third-party dependencies preserved; vulnerability surface unchanged.
- No frozen module touched; `metrics` stays network-free as designed.
- One listener, not two — probes and metrics share a port already designed to
  keep answering when the application listener is saturated.
- The exporter is a pure function of `[]Sample`, so it is exhaustively testable
  without a network, a server, or a backend.

**Negative**

- The Prometheus text format must be implemented and kept correct by hand.
  Mitigated by it being a small, stable, frozen specification, and by the output
  being directly assertable in tests.
- Prometheus pull semantics assume a scrapeable endpoint; a push-only
  environment would need a gateway.
- Exposition cost is paid on the scrape request, proportional to series count.
- **This ADR does not deliver tracing.** Observability remains half-solved, and
  that must not be reported as complete.

**Neutral**

- `packages/go/telemetry` becomes visibly redundant. Whether to delete,
  implement, or leave it is deferred with the tracing decision.
- Dashboards and alert rules are operator artefacts and are not in this repo.

### Confidence

**High** for the format and placement: three independent pieces of frozen
evidence (the `Buckets` comment, the no-collision test, the module header)
indicate the architecture already expected exactly this adapter, and the choice
is reversible — the exporter is one module behind one function.

**Not applicable** to tracing, which this ADR deliberately does not decide.

### Revisit Trigger

Revisit when **any** of the following is first observed:

- A metrics backend is chosen that cannot scrape a Prometheus text endpoint.
- Exported series exceed **10,000**, at which point a streaming or filtered
  exposition, or the protobuf format, becomes worth its complexity.
- A scrape's p99 exceeds **100 ms**, meaning exposition has become a cost rather
  than a readout.
- The zero-dependency property is deliberately ended by another ADR, which
  removes this ADR's main argument against `client_golang`.

## Options in Detail

### Option 1: Prometheus text v0.0.4, stdlib adapter *(chosen)*

New module converts `[]metrics.Sample` to text; `platform` mounts it at
`GET /metrics` on `HealthPort`.

- **Good:** no dependency; no frozen change; no credential; reuses an existing
  listener; testable as a pure function; matches EKS.
- **Bad:** the format is hand-written and must be kept correct.

### Option 2: `prometheus/client_golang`

- **Good:** canonical implementation; format correctness is someone else's job.
- **Bad:** ends the zero-dependency property for a formatter; the library's
  instruments duplicate `metrics`, and its registry would have to be bridged to
  `Snapshot()` anyway.

### Option 3: OpenTelemetry SDK + OTLP

- **Good:** metrics and traces in one model; vendor-neutral.
- **Bad:** the largest dependency tree of any option; pushes rather than
  scrapes; bundles the deferred tracing decision; does not solve the frozen
  correlation-identity blocker regardless.

### Option 4: Hosted SaaS backend

- **Good:** no infrastructure to operate.
- **Bad:** **requires an ingest credential**; egress of operational data to a
  third party interacts with ADR-0012; vendor SDK dependency.

## References

- `packages/go/metrics/go.mod` — module header: adapter-based exposition, no
  network dependency
- `packages/go/metrics/metrics.go` — `Sample`, and `Buckets` as the cumulative
  convention "Prometheus expects"
- `packages/go/evalsubjects/observability_test.go` — no-collision guarantee for a
  combined exporter
- `packages/go/platform/server.go` — existing `/healthz` and `/readyz` mux on
  `HealthPort`
- `packages/go/telephony/ids.go`, `packages/go/media/errors.go` — the frozen
  correlation-identity finding blocking T6
- ADR-0008 — AWS EKS, `ap-south-1`, Graviton
- ADR-0009 — data and event backbone
- ADR-0012 — DPDP consent and retention; bounds what may be a label value
- Prometheus text exposition format v0.0.4 —
  <https://prometheus.io/docs/instrumenting/exposition_formats/>
- Supersedes: none. Superseded by: none.
