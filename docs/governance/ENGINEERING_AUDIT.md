# Engineering Audit — Safety, Policy & Governance Engine

**Phase 10E** · `packages/go/governance` · 2026-08
**Verdict: APPROVE WITH ONE BLOCKING FINDING (A2)**

A self-audit, written to be useful to a reviewer who wants to disagree with it.
Every defect found during construction is recorded with what it actually was
rather than what it looked like afterwards.

---

## 1 · Scope

| | |
|---|---|
| Module | `packages/go/governance` |
| Files | 24 Go files — **20 source, 4 test** |
| Lines | **10,123** — 7,337 source, 2,786 test |
| Dependencies | **1** — `packages/go/runtime` (Phase 10A, frozen) |
| External dependencies | **0** (`go list -deps` shows stdlib only) |
| Tests | **82** — 53 unit, 20 integration/stress/failure, 9 evaluation |
| Benchmarks | 20 |
| `gofmt` | Clean |
| `go vet` | Clean |
| `-count=5 -shuffle=on` | Passes |
| `-race` | **NOT RUN — see A2** |

**Frozen artifacts untouched.** Phases 10A–10D are unmodified and all four
suites still pass. `go.work` gained one line.

---

## 2 · Compliance with the brief

### Prohibited dependencies — verified absent

OpenAI Moderation · Anthropic Safety · Gemini Safety · LangChain Guardrails ·
Guardrails AI · NeMo Guardrails · Llama Guard · Microsoft Prompt Shields · any
external safety framework.

`go.mod` has one `require` line, first-party.

### Architectural rules — verified

| Rule | Enforcement |
|---|---|
| Every decision passes through the engine | **One entry point**, `Decide` |
| Memory runtime never bypasses policies | Structural: memory calls in; no import either way |
| Tool runtime never bypasses permissions | Same |
| Conversation engine never bypasses safety | Same |

**The enforcement is the absence of an alternative.** There is no second
decision function, no fast path and no bypass flag. A subsystem that skips the
engine does so by not calling it — visible in review, countable in production
via `Engine.Decisions()`.

### Excluded implementations — verified absent

| Excluded | Evidence |
|---|---|
| Fraud detection models | No model, no scoring heuristic. `Signal` is an input another phase asserts |
| Telephony intelligence | No import, no type |
| Conversation prompts | None |
| LLM safety APIs | No provider named anywhere in the module |
| Business logic | Operations, capabilities and consent bases are opaque strings |
| Payment policies | None |
| External vendor policies | None |

### Required stack

Go 1.25+ ✓. Provider agnostic ✓. Built from scratch ✓.

Python 3.12, protobuf, gRPC, Kafka, Redis and Aurora are named in the brief's
stack. **This phase delivers the Go engine and the event contract only.** Topic
names follow the frozen `eventbus` convention; `Publisher`, `Auditor`,
`Detector`, `Classifier` and `Masker` are the declared seams. Flagged as **A3**
rather than silently scoped out.

---

## 3 · Defects found and fixed during construction

Five, plus one design gap the tests exposed. Recorded because a phase report
claiming none would be a report nobody should trust — and because three of these
would each have made a headline property of this module false in production.

### F1 · The fail-closed path itself panicked

**Severity: critical. Found by test, fixed.**

`evaluateSafely` built its fallback decision **inside** the deferred recover
handler, reading `snap.Version` there.

When the panic was a nil snapshot — exactly the shape of bug the handler exists
to survive — the handler dereferenced the same nil and **panicked again, taking
the process down at the precise moment it was supposed to fail closed.**

Fixed by capturing the snapshot version and the request fingerprint **before**
evaluation begins.

> A recovery path that can fail the same way as the thing it recovers from is
> not a recovery path.

**Why it mattered:** the headline claim of this module is that it fails closed.
A crash is not a denial; it is an outage, and under a supervisor it is a crash
loop.

### F2 · Structural validation refused the most common action in the platform

**Severity: high. Found by test, fixed.**

`KindSpec.RequireReversibility` was set on `ActionMemory`, so **every memory
read was rejected as malformed** — a read genuinely changes nothing, and
`ReversibleNone` is the correct declaration.

Fixed by replacing the per-kind flag with `MutatingOperations`, naming the
operations that must not claim to change nothing. The flag survives only for
`ActionExternal`, where there is no operation vocabulary to enumerate.

**Why it mattered:** the failure was silent in the sense that mattered — reads
were denied with `malformed_action`, which a caller would reasonably read as the
platform being over-restrictive rather than as a validator bug.

### F3 · Every emergency override was a no-op

**Severity: critical. Found by test, fixed.**

Cross-scope conflict resolution let a **lower** scope re-strengthen an override,
on the reasoning that an override *"relaxes a rule rather than disabling every
rule beneath it"*.

That reasoning is wrong. Emergency is evaluated before Global, so the emergency
allow became the incumbent — and then the global denial the emergency existed to
relax simply won again one scope later. **The entire emergency mechanism did
nothing.**

Fixed with three ordered rules: an override from a higher scope is final for
everything below it; an override may displace an incumbent only when that
incumbent's scope is overridable; otherwise the safer outcome wins.

The compliance guard now falls out of the ordering rather than needing a special
case — `winner.scope.Overridable()` is false only for compliance, and compliance
is evaluated first.

**Why it mattered:** an emergency mechanism that silently does nothing is worse
than none, because an on-call engineer declares one and believes the platform
has been unblocked.

### F4 · The consent gate could never open

**Severity: high. Found by integration test, fixed.**

`resolveConsent` only ever **raised** the outcome. When a policy decided
`require_consent` and the subject then granted it, the obligation was dropped —
and the outcome stayed `require_consent` **forever**. The caller obtained
consent, asked again, and got the same answer.

Fixed by adding the downgrade: when every consent obligation is satisfied and
the outcome is exactly `require_consent`, it resolves to `allow` with reason
`consent_satisfied`.

**Why it mattered:** it made the whole consent mechanism a dead end. A unit test
of `resolveConsent` in isolation would have passed; only running the full flow
caught it.

### F5 · Obligations from lower-priority policies were silently dropped

**Severity: medium. Found by reading an evaluation-suite log, fixed.**

Within a scope, only the winning policy's obligations survived.

The case that exposed it: a **personal, irreversible notification** matches both
`baseline.irreversible` (require confirmation) and `baseline.personal-data`
(require consent). They sit at different priorities in one scope, so the
winner-takes-all version **silently discarded the confirmation requirement** —
and the losing policy sat in the trace as matched-but-not-decisive, so the loss
was invisible.

Fixed by accumulating obligations from every matching policy. A policy that must
remove one does so from a higher scope with `Override`, where the relaxation is
visible and attributed.

**Found by reading a log line in a passing test**, not by an assertion. The
evaluation suite printed `irreversible notify → require_consent by
baseline.personal-data` and the missing confirmation obligation was visible only
to somebody asking why.

### The reason-code finding

Not a defect in shipped code, but worth recording: a test fixture wrote a
consent basis name into a policy reason (`needs_call_recording`), and the
`TestEvents_CarryNoContent` assertion caught it appearing in a permanent event
topic.

The fixture was wrong, but it demonstrated that **reason codes were unbounded
free text on a path into Kafka**. Reasons are now validated as bounded
identifiers — lowercase, digits, underscores, ≤64 characters — for two
independent reasons: metric cardinality and the permanence of a topic.

---

## 4 · Open findings

### A1 · Fifth copy of the metrics primitives — accepted

**Severity: low. Not fixed. Fourth recurrence of the same finding.**

`runtime.Metrics` (10A) exports its types but keeps its constructors unexported,
so it is closed for extension. Phases 10B, 10C, 10D and now 10E each carry their
own counter/gauge/histogram plumbing — **five copies in the platform.**

Not fixed because 10A is frozen. The correct fix is a superseding ADR exporting
`runtime.NewCounter` and friends, then deleting four duplicates.

**This has now been the first handover item for three consecutive phases.** Each
one that ships around it makes the eventual correction larger, and the argument
for deferring it again is weaker every time.

### A2 · Race detector never run — **BLOCKING**

**Severity: high. Not fixed. Cannot be fixed on this machine.**

`go test -race` requires cgo, cgo requires a C toolchain, and there is none here.
**This module has never been run under the race detector.**

That now applies to **five** concurrent modules. This one is moderately
concurrency-dense: a copy-on-write registry read with no lock, a consent
registry under RWMutex with per-record mutation, an escalation queue with a
first-writer-wins race by design, and an emergency engine that mutates the
policy registry.

**What exists instead:** concurrent-decision, policy-churn-during-decision,
concurrent-consent and racing-escalation tests, passing at
`-count=5 -shuffle=on`.

**Required before production:** one CI job on Linux with cgo enabled running
`go test -race -count=5 ./...` across all five modules. Until then the strongest
honest claim is *"no data race was observed"*.

### A3 · Phase delivers the Go engine only

**Severity: informational.** Python 3.12 utilities, `.proto` definitions, the
gRPC service, the Kafka producer, the Redis-backed consent cache and the Aurora
audit store are named in the brief's stack and are **not** in this deliverable.

The seams are declared and exercised by doubles. Flagged so the gap is a
decision rather than an omission.

### A4 · Evaluation is O(policies)

**Severity: medium.** A decision visits every policy to produce a complete trace:
**403 ns marginal per policy, 80 µs at 200 policies.**

The obvious index — by action kind — is refused because it would make the trace
incomplete, and the trace is what makes a denial explainable. PERFORMANCE §5
states the trade and names the change to make if a deployment outgrows it: a
`TraceLevel` configuration, not a silent index.

**Watch the policy count, not the decision count.** At 1,000 policies a
ten-decision turn spends 3.8 ms in governance.

### A5 · Policies are Go values, not files

**Severity: medium. The most likely thing to surprise a reader.**

The module says "policies are data" and means it structurally — a `Policy` has
no closures and no callbacks. But **there is no serialisation**: no JSON, no
YAML, no protobuf, no loader, no policy file.

A policy is written in Go, compiled in, and registered by a call. That means:

- policy changes require a deploy;
- an operator cannot review a diff of "the policies" independently of code;
- the `Digest` is comparable across deployments but not against a file.

Deliberate for 10E — a serialisation format is a wire-format decision and the
wire format is protobuf, which is A3's scope — but it is the gap most likely to
be assumed closed. Until it lands, "policies are data" means "policies are
reviewable Go values", not "policies are configuration".

### A6 · The request fingerprint is computed twice per decision

**Severity: low, deliberate.** ~1.8 µs, 19% of a small decision. It buys a panic
handler that cannot fail the way F1 failed. PERFORMANCE §6.

### A7 · The engine cannot detect its own bypass

**Severity: medium, structural.** `Engine.Decisions()` is a counter. Detecting a
bypass means comparing it against each subsystem's own action count, **outside
this module**, and nothing here enforces that comparison.

A subsystem that never calls `Decide` is indistinguishable from one that has
nothing to do. Recorded here and in SECURITY_REVIEW §R3; the mitigation is a
platform-level invariant check, not a code change in this package.

---

## 5 · Design decisions a reviewer should challenge

| Decision | The counter-argument |
|---|---|
| **One generic `Action` rather than five typed ones** | Type safety per subsystem. Rebuttal: five entry points is five rules, and the sixth caller gets its own |
| **Default deny, non-configurable** | A deployment may genuinely want permissive behaviour. Rebuttal: it writes an allow policy, which appears in every trace |
| **Global sits above Organization** | Tenants expect their own rules to win. Rebuttal: that is exactly the configuration the platform floor exists to prevent |
| **Compliance unoverridable, with no escape hatch** | A real incident might need it. Rebuttal: that is a lawyer's decision and a policy change, and a hatch would be used at 3 a.m. |
| **The satisfied consent gate resolves to `Allow`** | The next-most-severe policy is discarded. Rebuttal: the evaluator keeps one winner rather than a ranking, and a runner-up nobody can see in the trace is worse than an explicit second rule |
| **Nine selectors, no arithmetic or regex** | Real policies want more. Rebuttal: a policy language becomes a program nobody reviews |
| **Obligations accumulate; outcomes do not** | A high-priority allow cannot drop a low-priority obligation. Rebuttal: fail-closed, and `Override` is the visible way to relax |
| **Trace includes non-matching policies** | 403 ns per policy. Rebuttal: it answers the operator's actual question |
| **Risk aggregation is monotonic** | A genuine all-clear signal cannot lower risk. Rebuttal: reassurance belongs in policy, where the trace shows it |
| **Consent evidence is a fingerprint** | Cannot produce the artefact on demand. Rebuttal: holding it would make this an unmanaged personal-data store |

---

## 6 · Invariant enforcement

| # | Invariant | Enforced at | Test |
|---|---|---|---|
| INV-GOV-1 | One entry point, no bypass path | `Decide` is the only decision function | Structural |
| INV-GOV-2 | Default deny; zero `Outcome` is deny | `Config.validate`, constant order | `TestOutcome_ZeroValueIsDeny`, `TestConfig_RefusesToDefaultToAllow` |
| INV-GOV-3 | Compliance cannot be overridden | `Scope.Overridable`, `Emergency.validate` | `TestEvaluator_EmergencyOverrideCanLoosenButNotCompliance` |
| INV-GOV-4 | Consent transitions follow the table | `canTransition` | `TestConsent_FourNegativeOutcomesAreDistinct` |
| INV-GOV-5 | Every decision names a policy and a bounded reason | `Rule.validate`, `checkReasonCode` | `TestEvaluation_EveryDecisionIsExplainable` |
| INV-GOV-6 | Every policy consulted appears in the trace | `evaluateScope` | `TestEvaluator_TraceNamesEveryPolicyConsulted` |
| INV-GOV-7 | No content in decisions, events or audit | No field; validator; reason codes | `TestEvents_CarryNoContent`, `TestValidator_RefusesContentShapedAttributes` |
| INV-GOV-8 | Evaluation is pure | `Evaluate` signature | `TestEvaluator_IsPureAndTakesNoClock` |
| INV-GOV-9 | One decision, one snapshot | Copy-on-write registry | `TestStress_PolicyChurnDuringDecisions` |
| INV-GOV-10 | An engine starts once | `Engine.Start` | `TestEngine_StartsOnce` |
| INV-GOV-11 | Risk is monotonic; thresholds only raise | `Aggregate`, `Thresholds.Apply` | `TestEvaluation_RiskIsMonotonicAndOnlyRaises` |
| INV-GOV-12 | No global mutable state | Construction | `TestEngine_TwoEnginesShareNothing` |

**Nine of twelve are enforced by absence or by construction.** Enforcement by
absence cannot be forgotten, misconfigured, or switched off during an incident.

INV-GOV-1 and INV-GOV-7 are the weak ones: the first cannot be enforced from
inside this module (A7), and the second is bounded rather than proved
(SECURITY_REVIEW §R1).

---

## 7 · Test quality

| Property | Assessment |
|---|---|
| Behaviour vs implementation | Tests assert observable outcomes; two exceptions (`consentTransitions`, `checkReasonCode`) are tests *about* those structures |
| Failure injection | Audit failure, publisher failure, evaluation panic, policy conflict, malformed action, expired grant, racing resolution |
| Determinism asserted | 50-run decision fingerprints, 100-run pure evaluations, metric snapshot ordering |
| Concurrency | Concurrent decisions, policy churn during decisions, concurrent consent, racing escalation resolution |
| Clock | Every time-dependent test uses `FakeClock`; **no test sleeps on real time** |
| Matrix coverage | All 36 ordered scope pairs; all 10 outcomes; all 4 consent states; 9 selectors |
| Flakiness | None observed at `-count=5 -shuffle=on` |

**Gap: no property-based or fuzz testing.** `Attr.canonical` is an excellent
fuzz target — "distinct attributes never share an encoding" is exactly what a
fuzzer checks cheaply, and a collision there would silently break replay
comparison. Recommended for 10F.

**Gap: the panic path is tested once, through a nil snapshot.** A panic
originating inside a condition — which is where a future selector bug would live
— is not covered.

**Gap: no benchmark regression gate.** The scaling numbers in PERFORMANCE §3 were
read by hand. A CI gate on `ns/op` would catch the day somebody adds an O(n²)
resolution step.

---

## 8 · Verdict

**APPROVE WITH ONE BLOCKING FINDING.**

The engine meets the brief: from scratch, no external safety framework, one
entry point, default deny, ten outcomes, nine scopes with a fixed precedence,
compliance that no emergency can reach, consent with four distinguishable
failure states and immediate revocation, monotonic risk, and a decision that
always names a policy and a bounded reason. 82 tests, and the evaluation suite
measures rather than asserts: 100% of ungoverned actions denied, 36 of 36 scope
pairs resolving safely, 5 of 5 consent states distinguishable, 0 divergences
across 50 identical runs.

**Three of the five defects found would each have made a headline property
false** — fail-closed that crashed, emergency overrides that did nothing, and a
consent gate that could never open. All three were found by tests rather than by
inspection, which is the argument for the suite rather than for the code.

**A2 blocks production, not approval of the phase.** Five concurrent modules
have now been built without ever running the race detector.

Second priority: **A5**. "Policies are data" is true structurally and false
operationally until there is a loader, and it is the claim a reader is most
likely to over-read.

### Handover to Phase 10F

| Item | Action |
|---|---|
| **A2** | CI job: Linux, cgo, `-race -count=5` across all five modules |
| **A1** | Superseding ADR exporting `runtime` metric constructors; delete four duplicates |
| **A5** | Policy serialisation and a loader, once protobuf lands |
| **A3** | `.proto`, gRPC service, Kafka producer, Aurora audit store, Redis consent cache, Python utilities |
| **A7** | Platform-level bypass check comparing `Decisions()` against each subsystem's action count |
| **A4** | `TraceLevel` configuration if a deployment passes ~1,000 policies |
| §7 gaps | Fuzz `Attr.canonical`; cover a panic inside a condition; benchmark regression gate |
