// Package runtime is the Aegis AI Runtime Core: the execution substrate every
// AI interaction in the platform runs on.
//
// It is built from scratch on the Go standard library. There is no agent
// framework beneath it, and there is no framework to be locked into.
//
// # What this package is
//
// A runtime, in the sense an operating system kernel is a runtime. It owns
// admission, scheduling, session lifetime, context assembly, provider
// selection, streaming, cancellation, and observability. It knows how to run an
// AI interaction. It knows nothing about what any interaction means.
//
// # What this package is deliberately not
//
// It contains no conversation logic, no prompt text, no telephony, no fraud
// scoring, no memory reasoning, no tool calling, and no agent behaviour. Those
// belong to the AI Orchestration bounded context (docs/domain/14) and sit ABOVE
// this package. The separation is enforced by the type system: nothing in this
// package refers to a caller, a subscriber, a screening, or a verdict.
//
// The one apparent exception is [ModelTier], which names the four-tier ladder
// from ADR-0006. It is here because tier is a routing input the runtime must
// act on, not because the runtime understands why a tier was chosen.
//
// # The seventeen subsystems
//
//	Kernel          [Kernel]              wiring, ownership, no globals
//	Scheduler       [Scheduler]           admission, priority, backpressure
//	Session manager [SessionManager]      lifetime, TTL, sharded lookup
//	Context manager [ContextManager]      token budget, assembly, eviction
//	Provider layer  [Provider]            vendor-agnostic generation
//	Model registry  [ModelRegistry]       tier ladder, capabilities, routing
//	Prompt registry [PromptRegistry]      versioned storage and rollout gating
//	State machine   [FSM]                 typed, guarded transitions
//	Streaming       [Dispatcher]          fan-out, backpressure, barge-in
//	Token dispatch  [Dispatcher.Run]      the hot loop
//	Metrics         [Metrics]             counters, gauges, histograms
//	Logging         [Kernel.Logger]       structured, contextual — NOT redacting
//	Tracing         [Tracer]              span port, no-op by default
//	Health          [HealthState]         liveness vs readiness
//	Configuration   [Config]              typed, validated, fail-fast
//	Lifecycle       [Kernel.Start/Stop]   ordered start, reverse drain
//	Test harness    [Harness]             fake clock, fake provider, assertions
//
// Three entries in that table were corrected in Phase 10.5 because they claimed
// more than the code delivers, and one of them claimed it about a security
// control:
//
//   - Logging said "redacting". It does not redact — [Kernel.Logger] states so
//     explicitly, and an engineer who read the table instead of the method could
//     reasonably have concluded it was safe to log message content.
//   - Tracing said "spans, propagation, sampling". [Tracer] is a port with a
//     no-op default; the kernel opens one span and nothing implements sampling.
//   - Health referenced a HealthReporter, which does not exist. The type is
//     [HealthState].
//
// # Design commitments
//
// No global mutable state. Every subsystem is a struct with explicit
// dependencies, constructed by [New] and owned by a [Kernel]. There is no
// package-level registry, no init() side effect, and no singleton. Two kernels
// can run in one process with no interference, which is what makes the test
// suite parallel-safe and what makes multi-tenancy possible later.
//
// Streaming first. [TokenStream] is the primary result type, not an
// optimisation over a batch API. A non-streaming call is a stream collected to
// completion, never the reverse.
//
// Cancellation is a first-class latency budget. ADR-0011 fixes barge-in at one
// frame interval, 20 ms. [Dispatcher] is built so an abort signal preempts an
// in-flight provider read rather than waiting for it, and the bound is asserted
// by test, not assumed.
//
// Fail closed under load. ADR-0006 invariant I11 permits shedding at admission
// and downgrading a tier; it forbids skipping the safety layer. [Scheduler]
// encodes that as a [Class] whose shed policy cannot be configured away.
//
// # Concurrency contract
//
// Every exported type in this package is safe for concurrent use by multiple
// goroutines unless its documentation says otherwise. The exceptions are
// [Harness] and its fakes, which are single-test-scoped by design.
//
// # Reading order
//
// Start at [Kernel] for wiring, [Provider] for the vendor boundary, and
// [Dispatcher] for the hot path. Everything else is reachable from those three.
package runtime
