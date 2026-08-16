// Package governance is the Enterprise Safety, Policy & Governance Engine: the
// final authority on whether the platform may act.
//
// # One door
//
// Every conversation decision, every tool execution, every memory write and
// every external action passes through [Engine.Decide]. There is one entry
// point, it takes one type — [Request] wrapping an [Action] — and it returns
// one type, [Decision].
//
// That is the whole architecture, and it is the reason the rule "no subsystem
// may bypass this engine" is enforceable rather than aspirational. Five typed
// entry points, one per calling subsystem, would be five rules; each would grow
// its own special cases, and the sixth caller would quietly get its own. One
// door means a bypass is a missing call, which is visible in review and
// countable in production: [Engine.Decisions] against each subsystem's own
// action count.
//
// It also means this module imports nothing it governs. Conversation (10B),
// memory (10C) and toolruntime (10D) call in; none of them appears in this
// module's dependency graph. A governance engine that knew the shape of its
// callers would acquire a special case per caller, and a special case per
// caller is how something eventually gets an exemption.
//
// # Policies are data; evaluation is a pure function
//
// A [Policy] is declarative: a scope, a priority, a set of [Rule] values, each
// with conditions and an effect. [Evaluator.Evaluate] is a pure function of
// (snapshot, request, instant). No I/O, no mutation, no hidden state, no clock
// of its own.
//
// Everything the brief asks for follows from that one property. Determinism is
// free. Replay is free — a [Decision] carries the policy-set version it was made
// against, so the same decision can be recomputed later and compared.
// Explainability is free, because a pure function over declared rules can name
// exactly which rule decided and why. And testing is free of infrastructure:
// the whole evaluator suite runs with no clock, no broker and no store.
//
// # The default is deny
//
// An engine with no policies loaded denies everything. That is not a
// convenience to be configured away: a safety engine whose failure mode is
// "allow" has, at the exact moment it is broken, stopped being a safety engine.
//
// The consequence is deliberate and slightly uncomfortable: a runtime that boots
// with an empty registry cannot do anything at all until baseline policies are
// registered. That is the correct discomfort. It converts "we forgot to load
// policies" from a silent security incident into a loud outage.
//
// # Compliance policies cannot be overridden
//
// Emergency overrides exist because systems that cannot be overridden get
// overridden anyway, at 3 a.m., by someone with database access and no audit
// trail. So [Emergency] is a first-class, bounded, attributed mechanism.
//
// But it stops at [ScopeCompliance]. An override may relax an organisation's
// own rule; it may not relax a legal one. If a deployment genuinely needs to,
// that is a decision for a lawyer and a change to the compliance policy — not a
// button an on-call engineer presses at 3 a.m.
//
// # Ten outcomes, not two
//
// [Outcome] is not a boolean. Allow and Deny are two of ten; the other eight —
// escalate, require confirmation, require consent, require human, require
// supervisor, retry later, queue, defer — exist because a system that can only
// say yes or no says no to things it should have asked about.
//
// Each carries [Obligation] values: what must be true before the action may
// proceed. An obligation is machine-readable and specific, so a caller can
// satisfy it rather than guess.
//
// # What this package does not do
//
// It runs no fraud model, holds no telephony logic, contains no prompt, knows
// no business rules and calls no vendor safety API. [Detector] and [Classifier]
// are interfaces with no real implementation here; [Risk] is a framework for
// aggregating signals that other phases produce.
//
// # Invariants
//
// Twelve, listed in docs/governance/SAFETY_ARCHITECTURE.md and each enforced at
// a named place in the code. Most are enforced by absence — a missing import, a
// missing field, a missing constructor — because enforcement by absence cannot
// be forgotten, misconfigured, or switched off during an incident.
package governance
