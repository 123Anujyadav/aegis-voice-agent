package evalsubjects

import (
	"time"

	ev "github.com/callscreen/callscreen-platform/packages/go/evaluation"
)

// Library returns the platform's scenario set.
//
// THESE ARE THE QUESTIONS THE PLATFORM ASKS OF ITSELF. Each one exercises a
// property some other phase's design argument rests on, which is the selection
// criterion worth stating: a scenario that checks something no design decision
// depends on costs time and buys nothing.
//
// They are deliberately small. A scenario library grows, and the ones that grow
// are the ones somebody can read — a forty-step scenario that fails tells you
// nothing about which of its forty assumptions broke.
func Library() ev.ScenarioSet {
	var out ev.ScenarioSet
	out = append(out, runtimeScenarios()...)
	out = append(out, conversationScenarios()...)
	out = append(out, memoryScenarios()...)
	out = append(out, toolScenarios()...)
	out = append(out, governanceScenarios()...)
	return out
}

// Suites returns the platform's suites.
func Suites() []ev.Suite {
	var acceptance, compliance, benchmark []ev.ScenarioID
	for _, s := range Library() {
		switch s.Kind {
		case ev.KindGovernance, ev.KindEmergency:
			compliance = append(compliance, s.ID)
		}
		// Acceptance covers everything that is not a failure-injection
		// scenario: a release gate wants to know the platform works, and a
		// scenario whose point is that something broke would make the gate
		// unreadable.
		if s.Kind != ev.KindFailure {
			acceptance = append(acceptance, s.ID)
		}
		if s.Kind == ev.KindMemory || s.Kind == ev.KindGovernance {
			benchmark = append(benchmark, s.ID)
		}
	}

	return []ev.Suite{
		{
			ID: "acceptance", Kind: ev.KindAcceptance,
			Title: "Release acceptance", Owner: "platform-quality",
			Description: "Everything that must work before a release.",
			Scenarios:   acceptance,
		},
		{
			ID: "compliance", Kind: ev.KindCompliance,
			Title: "Governance and consent evidence", Owner: "platform-privacy",
			Description: "Scenarios evidencing that the platform refuses what it must.",
			Scenarios:   compliance,
		},
		{
			ID: "benchmark", Kind: ev.KindBenchmark,
			Title: "Hot-path benchmarks", Owner: "platform-quality",
			Description: "Latency-sensitive scenarios, run serially.",
			Scenarios:   benchmark,
			// Not Parallel: a benchmark suite sharing a CPU measures
			// contention. The registry refuses it anyway.
		},
	}
}

func scenario(id ev.ScenarioID, kind ev.ScenarioKind, subject ev.SubjectName,
	title string, steps ...ev.Step) ev.Scenario {
	return ev.Scenario{
		ID: id, Version: 1, Kind: kind, Title: title, Owner: "platform-quality",
		SubjectName: subject, Steps: steps, Tolerances: ev.DefaultTolerances(),
	}
}

// ---------------------------------------------------------------------------
// Runtime core (10A)
// ---------------------------------------------------------------------------

func runtimeScenarios() ev.ScenarioSet {
	return ev.ScenarioSet{
		// The breaker opening is the property four later phases assume. A
		// breaker that silently stopped opening would leave every retry loop
		// in the platform hammering a dead downstream.
		scenario("runtime.breaker-opens", ev.KindRuntime, SubjectRuntime,
			"A breaker opens after repeated failures",
			ev.Step{Name: "fail-1", Op: "breaker_allow", Args: ev.Values{"fail": ev.B(true)}},
			ev.Step{Name: "fail-2", Op: "breaker_allow", Args: ev.Values{"fail": ev.B(true)}},
			ev.Step{Name: "fail-3", Op: "breaker_allow", Args: ev.Values{"fail": ev.B(true)}},
			ev.Step{Name: "fail-4", Op: "breaker_allow", Args: ev.Values{"fail": ev.B(true)}},
			ev.Step{Name: "fail-5", Op: "breaker_allow", Args: ev.Values{"fail": ev.B(true)}},
			ev.Step{Name: "state", Op: "breaker_state"},
		),

		// Eviction under budget pressure, and pinned messages surviving it.
		scenario("runtime.window-evicts", ev.KindRuntime, SubjectRuntime,
			"A context window evicts under budget pressure and keeps pinned messages",
			ev.Step{Name: "pin", Op: "window_append",
				Args: ev.Values{"text": ev.S("a pinned instruction"), "pinned": ev.B(true)}},
			ev.Step{Name: "fill-1", Op: "window_append",
				Args: ev.Values{"text": ev.S("a long conversational message that consumes budget")}},
			ev.Step{Name: "fill-2", Op: "window_append",
				Args: ev.Values{"text": ev.S("another long conversational message consuming budget")}},
			ev.Step{Name: "fill-3", Op: "window_append",
				Args: ev.Values{"text": ev.S("a third long conversational message consuming budget")}},
			ev.Step{Name: "assemble", Op: "window_assemble"},
		),

		// The clock is injected everywhere; a scenario that advances it and
		// sees no movement would mean a phase had started reading wall time.
		scenario("runtime.clock-advances", ev.KindRuntime, SubjectRuntime,
			"An injected clock advances deterministically",
			ev.Step{Name: "start", Op: "clock_now"},
			ev.Step{Name: "advance", Op: "clock_now", Advance: 90 * time.Second},
		),
	}
}

// ---------------------------------------------------------------------------
// Conversation (10B)
// ---------------------------------------------------------------------------

func conversationScenarios() ev.ScenarioSet {
	return ev.ScenarioSet{
		scenario("conversation.turn-taking", ev.KindConversation, SubjectConversation,
			"A conversation progresses through turns and plans an action each time",
			ev.Step{Name: "begin", Op: "begin"},
			ev.Step{Name: "start", Op: "start"},
			ev.Step{Name: "turn-1", Op: "say", Args: ev.Values{"text": ev.S("hello there")}},
			ev.Step{Name: "turn-2", Op: "say", Args: ev.Values{"text": ev.S("I need an appointment")}},
			ev.Step{Name: "state", Op: "state"},
		),

		// Escalation reaching a terminal state is what the conversation
		// engine's whole outcome model rests on.
		scenario("conversation.escalation", ev.KindRecovery, SubjectConversation,
			"A conversation escalates to a human and reaches a terminal outcome",
			ev.Step{Name: "begin", Op: "begin"},
			ev.Step{Name: "start", Op: "start"},
			ev.Step{Name: "turn", Op: "say", Args: ev.Values{"text": ev.S("this is complicated")}},
			ev.Step{Name: "escalate", Op: "escalate"},
			ev.Step{Name: "state", Op: "state"},
		),
	}
}

// ---------------------------------------------------------------------------
// Memory (10C)
// ---------------------------------------------------------------------------

func memoryScenarios() ev.ScenarioSet {
	return ev.ScenarioSet{
		scenario("memory.store-retrieve", ev.KindMemory, SubjectMemory,
			"A stored record is retrievable and reports its tier",
			ev.Step{Name: "store", Op: "store",
				Args: ev.Values{"name": ev.S("greeting"), "data": ev.S("hello")}},
			ev.Step{Name: "retrieve", Op: "retrieve", Args: ev.Values{"name": ev.S("greeting")}},
			ev.Step{Name: "count", Op: "count"},
		),

		// The four distinct negative outcomes are a design decision Phase 10C
		// argued for at length. This is the scenario that would notice them
		// collapsing back into one.
		scenario("memory.missing-is-not-found", ev.KindMemory, SubjectMemory,
			"Retrieving a record that never existed reports not_found, not an error",
			ev.Step{Name: "retrieve", Op: "retrieve", Args: ev.Values{"name": ev.S("never-stored")}},
		),

		// Erasure spanning every namespace is the compliance-critical one.
		scenario("memory.forget-is-complete", ev.KindMemory, SubjectMemory,
			"Forgetting a subject removes its records and reports completeness",
			ev.Step{Name: "store-1", Op: "store", Args: ev.Values{"name": ev.S("a")}},
			ev.Step{Name: "store-2", Op: "store", Args: ev.Values{"name": ev.S("b")}},
			ev.Step{Name: "forget", Op: "forget"},
			ev.Step{Name: "retrieve", Op: "retrieve", Args: ev.Values{"name": ev.S("a")}},
		),

		// A failure-injection scenario that injects a REAL refusal: an
		// oversized record the engine's own size cap rejects.
		scenario("memory.oversized-refused", ev.KindFailure, SubjectMemory,
			"An oversized record is refused by the engine's own size cap",
			ev.Step{Name: "store", Op: "store",
				Args:   ev.Values{"name": ev.S("huge")},
				Inject: &ev.Failure{Kind: ev.FailMemory, Detail: "oversized_record"}},
		),
	}
}

// ---------------------------------------------------------------------------
// Tool runtime (10D)
// ---------------------------------------------------------------------------

func toolScenarios() ev.ScenarioSet {
	return ev.ScenarioSet{
		scenario("tool.execute", ev.KindTool, SubjectTool,
			"A registered tool executes and reports one attempt",
			ev.Step{Name: "register", Op: "register_read",
				Args: ev.Values{"capability": ev.S("lookup")}},
			ev.Step{Name: "execute", Op: "execute",
				Args: ev.Values{"capability": ev.S("lookup")}},
			ev.Step{Name: "calls", Op: "tool_calls",
				Args: ev.Values{"capability": ev.S("lookup")}},
		),

		// INV-TOOL-8: building a plan executes nothing. The tool's call count
		// must not move, which is exactly what this scenario observes.
		scenario("tool.plan-is-inert", ev.KindTool, SubjectTool,
			"Building a plan invokes no tool",
			ev.Step{Name: "register", Op: "register_read",
				Args: ev.Values{"capability": ev.S("lookup")}},
			ev.Step{Name: "plan", Op: "plan", Args: ev.Values{"capability": ev.S("lookup")}},
			ev.Step{Name: "calls", Op: "tool_calls",
				Args: ev.Values{"capability": ev.S("lookup")}},
		),

		// INV-TOOL-3: two executions in one correlation produce one tool call.
		scenario("tool.idempotency", ev.KindTool, SubjectTool,
			"A repeated mutating execution in one correlation invokes the tool once",
			ev.Step{Name: "register", Op: "register_write",
				Args: ev.Values{"capability": ev.S("book")}},
			ev.Step{Name: "first", Op: "execute",
				Args: ev.Values{"capability": ev.S("book"), "correlation": ev.S("fixed")}},
			ev.Step{Name: "second", Op: "execute",
				Args: ev.Values{"capability": ev.S("book"), "correlation": ev.S("fixed")}},
			ev.Step{Name: "calls", Op: "tool_calls",
				Args: ev.Values{"capability": ev.S("book")}},
		),

		scenario("tool.failure-is-classified", ev.KindFailure, SubjectTool,
			"A failing tool produces a classified outcome rather than a crash",
			ev.Step{Name: "register", Op: "register_read",
				Args:   ev.Values{"capability": ev.S("broken"), "max_attempts": ev.N(1)},
				Inject: &ev.Failure{Kind: ev.FailTool, Detail: "always_fails"}},
			ev.Step{Name: "execute", Op: "execute",
				Args: ev.Values{"capability": ev.S("broken")}},
			ev.Step{Name: "health", Op: "health"},
		),
	}
}

// ---------------------------------------------------------------------------
// Governance (10E)
// ---------------------------------------------------------------------------

func governanceScenarios() ev.ScenarioSet {
	return ev.ScenarioSet{
		// INV-GOV-2: the baseline permits a read and refuses a secret. Both in
		// one scenario, because a platform that permitted everything and a
		// platform that refused everything would each pass half of it.
		scenario("governance.baseline-decides", ev.KindGovernance, SubjectGovernance,
			"The baseline permits a read and refuses secret material",
			ev.Step{Name: "read", Op: "decide", Args: ev.Values{"action": ev.S("read")}},
			ev.Step{Name: "secret", Op: "decide", Args: ev.Values{"action": ev.S("secret")}},
		),

		// The consent gate opening is what Phase 10E's F4 fixed. A scenario
		// that watches it open is the one that would notice it closing again.
		scenario("governance.consent-gate", ev.KindGovernance, SubjectGovernance,
			"A personal write is gated on consent and proceeds once consent exists",
			ev.Step{Name: "before", Op: "decide", Args: ev.Values{"action": ev.S("write")}},
			ev.Step{Name: "grant", Op: "grant_consent"},
			ev.Step{Name: "after", Op: "decide", Args: ev.Values{"action": ev.S("write")}},
			ev.Step{Name: "revoke", Op: "revoke_consent"},
			ev.Step{Name: "again", Op: "decide", Args: ev.Values{"action": ev.S("write")}},
		),

		// Risk raising an outcome, and never lowering it.
		scenario("governance.risk-raises", ev.KindGovernance, SubjectGovernance,
			"Critical risk raises an outcome that policy alone would permit",
			ev.Step{Name: "low", Op: "decide",
				Args: ev.Values{"action": ev.S("read"), "risk": ev.S("low")}},
			ev.Step{Name: "critical", Op: "decide",
				Args: ev.Values{"action": ev.S("read"), "risk": ev.S("critical")}},
		),

		// INV-GOV-3: an emergency relaxes what it may and never compliance.
		scenario("governance.emergency-containment", ev.KindEmergency, SubjectGovernance,
			"An emergency cannot relax a compliance rule",
			ev.Step{Name: "activate", Op: "activate_emergency"},
			ev.Step{Name: "secret", Op: "decide", Args: ev.Values{"action": ev.S("secret")}},
			ev.Step{Name: "health", Op: "health"},
		),

		scenario("governance.malformed-refused", ev.KindFailure, SubjectGovernance,
			"A malformed action is refused as malformed, not as a policy denial",
			ev.Step{Name: "decide", Op: "decide",
				Args:   ev.Values{"action": ev.S("read")},
				Inject: &ev.Failure{Kind: ev.FailGovernance, Detail: "malformed_action"}},
		),
	}
}
