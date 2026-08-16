package conversation

import (
	metrics "github.com/callscreen/callscreen-platform/packages/go/metrics"
)

// Metrics is the conversation engine's instrument set.
//
// It uses the shared instruments in packages/go/metrics. Until Phase 10.5 it
// carried a private ~490-line copy of counter/gauge/histogram plumbing, because
// runtime.Metrics kept its constructors unexported and Phase 10A was frozen —
// the "A1" finding, first handover item for four consecutive phase audits. The
// shared package exports its constructors, so this file is now the thin binding
// those audits predicted it would collapse to.
//
// Engine-scoped rather than global: owned by an [Engine], two engines in one
// process share nothing, and the test suite is parallel-safe as a result.
type Metrics struct {
	// State machine
	StateEntered    *Counter // by state
	StateDuration   *Histogram
	Transitions     *Counter // by from,to,trigger
	InvalidAttempts *Counter // by from,to

	// Turns
	TurnsStarted   *Counter // by party
	TurnsYielded   *Counter // by party,kind
	TurnDuration   *Histogram
	FloorDecisions *Counter // by party,decision
	Backchannels   *Counter

	// Interruptions
	Interruptions   *Counter // by kind
	ResumeOutcomes  *Counter // by policy
	InterruptToStop *Histogram

	// Intent
	IntentsProposed  *Counter // by name
	IntentsAccepted  *Counter // by name
	IntentsRejected  *Counter // by reason
	IntentConfidence *Histogram
	IntentFallbacks  *Counter

	// Planning
	PlansProduced *Counter // by action
	PlanLatency   *Histogram
	PolicyDenials *Counter // by rule

	// Clarification
	Clarifications      *Counter // by kind
	ClarificationRounds *Histogram
	ClarificationGaveUp *Counter

	// Context
	ContextWrites  *Counter // by scope
	ContextExpired *Counter // by scope
	ContextSize    *Histogram

	// Persona
	PersonaSwitches *Counter // by from,to
	PersonaDenied   *Counter // by from,to

	// Latency
	StageLatency   *Histogram // by stage
	BudgetExceeded *Counter   // by stage
	BudgetRemain   *Histogram

	// Conversation outcomes
	Started      *Counter
	Completed    *Counter // by outcome
	Duration     *Histogram
	TurnsPerConv *Histogram
	Active       *Gauge

	// reg owns every instrument above. Shared implementation:
	// packages/go/metrics.
	reg *metrics.Registry
}

// NewMetrics constructs an instrument set.
func NewMetrics() *Metrics {
	m := &Metrics{reg: metrics.NewRegistry()}

	// Stage buckets are fine-grained and top out well below one second. The
	// whole conversation-decision budget is ~150 ms (see latency.go); a bucket
	// at 2 s would tell us nothing about a stage that is supposed to take 5.
	stage := []float64{0.0005, 0.001, 0.002, 0.005, 0.01, 0.02, 0.035, 0.05,
		0.075, 0.1, 0.15, 0.25, 0.5, 1.0}

	// Turn and conversation buckets are seconds-scale: a screening runs 20–40 s
	// (ADR-0002 §11) and turn length is the number that decides cost.
	secs := []float64{0.25, 0.5, 1, 2, 3, 5, 8, 12, 20, 30, 45, 60, 120, 300}

	counts := []float64{1, 2, 3, 4, 5, 6, 8, 10, 15, 20, 30, 50}
	sizes := []float64{1, 4, 16, 64, 256, 1024, 4096}
	conf := []float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 0.95, 0.99}

	m.StateEntered = m.counter("conv_state_entered_total", "state")
	m.StateDuration = m.histogram("conv_state_duration_seconds", secs, "state")
	m.Transitions = m.counter("conv_transitions_total", "from", "to", "trigger")
	m.InvalidAttempts = m.counter("conv_invalid_transition_total", "from", "to")

	m.TurnsStarted = m.counter("conv_turns_started_total", "party")
	m.TurnsYielded = m.counter("conv_turns_yielded_total", "party", "kind")
	m.TurnDuration = m.histogram("conv_turn_duration_seconds", secs, "party")
	m.FloorDecisions = m.counter("conv_floor_decisions_total", "party", "decision")
	m.Backchannels = m.counter("conv_backchannels_total")

	m.Interruptions = m.counter("conv_interruptions_total", "kind")
	m.ResumeOutcomes = m.counter("conv_resume_total", "policy")
	m.InterruptToStop = m.histogram("conv_interrupt_to_stop_seconds", stage)

	m.IntentsProposed = m.counter("conv_intents_proposed_total", "name")
	m.IntentsAccepted = m.counter("conv_intents_accepted_total", "name")
	m.IntentsRejected = m.counter("conv_intents_rejected_total", "reason")
	m.IntentConfidence = m.histogram("conv_intent_confidence", conf)
	m.IntentFallbacks = m.counter("conv_intent_fallback_total")

	m.PlansProduced = m.counter("conv_plans_total", "action")
	m.PlanLatency = m.histogram("conv_plan_latency_seconds", stage)
	m.PolicyDenials = m.counter("conv_policy_denials_total", "rule")

	m.Clarifications = m.counter("conv_clarifications_total", "kind")
	m.ClarificationRounds = m.histogram("conv_clarification_rounds", counts)
	m.ClarificationGaveUp = m.counter("conv_clarification_exhausted_total")

	m.ContextWrites = m.counter("conv_context_writes_total", "scope")
	m.ContextExpired = m.counter("conv_context_expired_total", "scope")
	m.ContextSize = m.histogram("conv_context_entries", sizes, "scope")

	m.PersonaSwitches = m.counter("conv_persona_switches_total", "from", "to")
	m.PersonaDenied = m.counter("conv_persona_denied_total", "from", "to")

	m.StageLatency = m.histogram("conv_stage_latency_seconds", stage, "stage")
	m.BudgetExceeded = m.counter("conv_budget_exceeded_total", "stage")
	m.BudgetRemain = m.histogram("conv_budget_remaining_seconds", stage)

	m.Started = m.counter("conv_started_total")
	m.Completed = m.counter("conv_completed_total", "outcome")
	m.Duration = m.histogram("conv_duration_seconds", secs)
	m.TurnsPerConv = m.histogram("conv_turns_per_conversation", counts)
	m.Active = m.gauge("conv_active")

	return m
}

// ---------------------------------------------------------------------------
// Instruments
// ---------------------------------------------------------------------------

// The instrument types are ALIASES, not wrappers.
//
// `type Counter = metrics.Counter` rather than `type Counter metrics.Counter`,
// so every existing reference in this package — and in anything importing it —
// keeps compiling and keeps its method set. That is what made this migration
// additive: no call site changed, no signature changed, and a caller holding a
// *conversation.Counter is holding exactly a *metrics.Counter.
//
// Before this, each phase carried its own ~490-line copy of these primitives.
// Six copies, and they had drifted where it mattered: three of the six could
// not emit histogram buckets, counts or sums from Snapshot() at all, so a
// cross-subsystem latency dashboard could not be built. See
// docs/hardening/METRICS_MIGRATION_REPORT.md.
type (
	// Counter is a monotonically increasing value, optionally labelled.
	Counter = metrics.Counter
	// Gauge is a value that may move in either direction.
	Gauge = metrics.Gauge
	// Histogram counts observations into caller-supplied buckets.
	Histogram = metrics.Histogram
	// Sample is one instrument series at one instant.
	Sample = metrics.Sample
)

func (m *Metrics) counter(name string, labels ...string) *Counter {
	return m.reg.Counter(name, labels...)
}

func (m *Metrics) gauge(name string) *Gauge {
	return m.reg.Gauge(name)
}

func (m *Metrics) histogram(name string, buckets []float64, labels ...string) *Histogram {
	return m.reg.Histogram(name, buckets, labels...)
}

// Registry exposes the underlying instrument registry.
//
// Added so a service can register its own instruments alongside this
// subsystem's, and export them through one scrape. The absence of this was the
// specific reason six copies of the primitives came to exist: the original
// registry kept its constructors unexported, so extending it was impossible and
// forking it was easy.
func (m *Metrics) Registry() *metrics.Registry { return m.reg }

// Snapshot returns every series from every instrument, in a stable order.
//
// Delegated to the shared registry, which means it now carries full histogram
// data — bounds, cumulative buckets, count and sum — in every subsystem. Three
// of the six previously emitted a single synthetic `name_count` value with none
// of that, and no consumer could recover a percentile or an average from it.
func (m *Metrics) Snapshot() []Sample { return m.reg.Snapshot() }
