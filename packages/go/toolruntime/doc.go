// Package toolruntime is the Enterprise Tool Calling Runtime: the component
// that turns a stated intent into a bounded, permitted, audited, compensable
// execution.
//
// # The one decision everything else follows from
//
// A ToolIntent names a CAPABILITY, not a tool.
//
// The conversation engine says "I need to check availability for this business
// on Tuesday". It does not say "call tool calendar_lookup_v3". That sounds like
// a small indirection and it is not: it is the difference between a runtime
// that can be reasoned about and one that cannot.
//
// If the caller named the tool, then the caller would own version resolution,
// health checking, fallback selection and permission scoping — and every caller
// would own them separately, slightly differently, and wrongly at least once.
// Naming a capability moves all of that behind one boundary. The runtime
// resolves the capability to a registered tool at a pinned version, checks that
// it is healthy, checks that this actor may use it, builds a plan, and executes
// it under a budget. The caller learns what happened; it never learns how the
// sausage was routed.
//
// It is also what makes provider agnosticism structural rather than aspirational.
// A vendor's tool-calling protocol is a wire format for "call this named
// function with this JSON". A capability request is not expressible in that
// format, which is precisely why translating a provider's tool call into a
// ToolIntent is an adapter's job and why this package has never heard of any
// provider.
//
// # What this package does not do
//
// It does not talk to a telephony carrier, a CRM, a calendar, or a payment
// processor. There is not one real tool adapter in this module and there is not
// meant to be — [Tool] is an interface, and the only implementations here are
// test doubles that are named as such.
//
// It does not write memory. It publishes events; the memory engine subscribes.
// The rule is enforced by the absence of an import, not by a comment.
//
// It does not decide business policy. [PermissionEngine] evaluates grants it is
// given; it does not know what a "receptionist" is allowed to do, because that
// belongs to Identity and would be a second, drifting copy of the truth here.
//
// # The execution path
//
//	ToolIntent  →  Planner  →  Plan (inert)  →  Dispatcher  →  Executor  →  Events
//	                  │                             │             │
//	              Discovery                     Permission     Sandbox
//	              Registry                      Idempotency    Retry
//	                                            Queue          Compensation
//
// A [Plan] is data. Building one executes nothing, touches no tool, and has no
// side effect a caller could observe — which is what makes a plan reviewable
// before it runs, replayable after it runs, and testable without a tool in
// sight.
//
// # Determinism
//
// Given the same registry, the same intent and the same clock, planning
// produces byte-identical plans, and execution produces the same sequence of
// events. Every source of nondeterminism is closed deliberately: time comes
// from an injected [runtime.Clock], jitter comes from an injected seeded
// source, map iteration is sorted everywhere it is observable, and every
// comparator falls back to a stable identifier.
//
// This is not tidiness. An execution runtime that cannot be replayed cannot be
// audited, and a system that takes actions in the world on a person's behalf
// has to be able to answer "why did it do that" with something better than a
// log line and a shrug.
//
// # Invariants
//
// Twelve, listed in docs/tools/TOOL_RUNTIME.md and each enforced at a named
// place in the code. Most are enforced by absence — a missing import, a missing
// field, a missing constructor — because enforcement by absence cannot be
// forgotten, misconfigured, or switched off during an incident.
package toolruntime
