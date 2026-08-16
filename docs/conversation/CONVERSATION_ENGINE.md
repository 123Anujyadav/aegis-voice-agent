# Conversation Intelligence Engine — Documentation

**Phase 10B** · `packages/go/conversation` · Status: **PROPOSED — awaiting approval**

A real-time conversation operating system for enterprise voice calls, built from
scratch on the Go standard library and the frozen Phase 10A runtime.

---

## 1 · What this is

| It is | It is not |
|---|---|
| The control plane of a live conversation | A chatbot |
| A deterministic state machine, a floor arbiter, a policy evaluator, a planner | An LLM wrapper |
| Provider- and model-agnostic by construction | Built on LangChain, CrewAI, AutoGen, Semantic Kernel, DSPy, Haystack or an Agents SDK |

There is **no prompt text** in this package, **no model is called from it**, and
**no vendor is imported**. It decides *what to do*; the layer above turns that
decision into words.

### The inversion that keeps it clean

The engine never calls a provider. It emits a [`Plan`](#7--response-planning-engine)
and an orchestration layer executes it. That inversion is why this package
contains no prompts, no telephony and no fraud logic — it has no way to express
them.

---

## 2 · Relationship to Phase 10A

Phase 10A is **frozen and unmodified**. This module imports it and adds nothing
to it.

```
   ┌──────────────────────────────────────────────────────┐
   │  conversation  (Phase 10B)                           │
   │  floor control · intent lifecycle · policy · planning│
   │  personas · clarification · context · latency        │
   │  KNOWS NOTHING ABOUT: tokens, vendors, transport     │
   └────────────────────────┬─────────────────────────────┘
                            │ imports
   ┌────────────────────────▼─────────────────────────────┐
   │  runtime  (Phase 10A, FROZEN)                        │
   │  sessions · streaming · cancellation · providers     │
   │  Clock · FSM · identifiers · error taxonomy          │
   │  KNOWS NOTHING ABOUT: conversation                   │
   └────────────────────────┬─────────────────────────────┘
                            │ imports
                    ┌───────▼────────┐
                    │  Go stdlib     │
                    └────────────────┘
```

**One dependency, and it is first-party.** The transitive closure of this module
is the Go standard library plus `packages/go/runtime`.

### What is reused rather than reimplemented

| From 10A | Used for | Why not rebuild |
|---|---|---|
| `rt.Clock` / `rt.FakeClock` | Every timeout, TTL and overlap window | A second clock abstraction would drift from the first |
| `rt.FSM[S]` | The conversation state machine | Already refuses undeclared transitions, refuses terminal exit, runs hooks unlocked, and is tested |
| `rt.ErrInvalidTransition` | Transition refusal | One error taxonomy across both layers |

### What could not be reused, and why

**`rt.Metrics` is closed for extension.** Its `Counter`, `Gauge` and `Histogram`
types are exported but their constructors are unexported, so a downstream module
cannot register an instrument into a 10A instrument set. Because 10A is frozen,
this module carries its own equivalent. That duplication is a finding, not a
design choice — see [ENGINEERING_AUDIT §A1](ENGINEERING_AUDIT.md).

---

## 3 · The twelve subsystems

| # | Subsystem | Type | File |
|---|---|---|---|
| 1 | Conversation State Machine | `State`, `transitionTable` | `state.go` |
| 2 | Dialogue Manager | `Conversation` | `engine.go` |
| 3 | Turn Manager | `TurnManager`, `Turn` | `turn.go` |
| 4 | Intent Engine *(framework only)* | `IntentEngine`, `IntentClassifier` | `intent.go` |
| 5 | Response Planning Engine | `Planner`, `Plan` | `planner.go` |
| 6 | Clarification Engine | `ClarificationEngine` | `clarification.go` |
| 7 | Context Engine | `ContextEngine`, `Scope` | `context.go` |
| 8 | Persona Runtime | `PersonaRuntime`, `Persona` | `persona.go` |
| 9 | Conversation Policy Engine | `PolicyEngine`, `Rule` | `policy.go` |
| 10 | Interruption Engine | `InterruptionEngine` | `interruption.go` |
| 11 | Latency Controller | `LatencyController`, `Stage` | `latency.go` |
| 12 | Conversation Metrics | `Metrics` | `metrics.go` |

Supporting: `errors.go` (typed errors), `harness.go` (`Harness`, `Simulator`,
`ScriptedClassifier`).

---

## 4 · Conversation State Machine

Seventeen states, every transition declared in one table
([`state.go`](../../packages/go/conversation/state.go)). Full diagram:
[STATE_TRANSITION_DIAGRAM.md](STATE_TRANSITION_DIAGRAM.md).

### The idea that carries the design

**Listening is not the only awaiting state.** Four states await the caller, and
three of them carry an *expectation*:

| State | Awaiting | Expectation | The same "yes" means |
|---|---|---|---|
| `Listening` | Anything | — | An utterance to classify |
| `Clarification` | Disambiguation | `ExpectDisambiguation` | A choice between candidates |
| `Confirmation` | Yes/no | `ExpectYesNo` | **An answer** |
| `Question` | A slot value | `ExpectSlotValue` | A value |

A system modelling only "listening" cannot interpret any of them correctly, and
mishandles every yes/no. This distinction is why `Speaking` fans out into four
successors rather than always returning to `Listening`.

### Structural guarantees

Three properties are **asserted by test**, not by inspection
(`TestStateMachine_TableIsWellFormed`):

1. Every state is reachable from `Idle` — no state can be dead code.
2. Every non-terminal state can reach a terminal state — no conversation can
   fail to end, which on a phone line means a call that never hangs up.
3. Terminal states declare no outgoing edges.

**There is no `Idle → Listening` edge.** That absence is the enforcement of the
opening announcement: dialogue cannot begin before the greeting has played, and
no code path can skip it because none exists.

---

## 5 · Turn Manager and floor control

**Half-duplex by construction.** At most one party holds the floor. Simultaneous
*audio* is a physical fact of a phone line; simultaneous *floor ownership* is a
modelling error, and `TurnManager` makes it unrepresentable.

### Arbitration policy

| # | Rule | Why |
|---|---|---|
| 1 | Emergency and transfer preempt everything | U7 outranks even the non-yielding greeting |
| 2 | A non-yielding turn is **queued**, not denied | The caller's intent to speak is real; discarding it makes the system feel deaf |
| 3 | Overlap under ~600 ms is a **backchannel** | "mm-hm" is simultaneous speech and is *not* an interruption. Arbitrating on any overlap makes the agent stop every time the caller agrees with it |
| 4 | Otherwise the caller wins, always | An agent that competes with a human for the floor is intolerable |

### The greeting exception

The opening turn is the **only** non-yielding turn (INV-CV-2). On this platform
it carries the announcement that is the caller's lawful basis under frozen
invariant I1, so it must complete. A barge-in during it is **queued and applied
the instant it ends** — so the guarantee holds without the caller having to
repeat themselves.

---

## 6 · Intent Engine — framework only

`IntentClassifier` **has no implementation in this module**, deliberately. The
brief specifies the intent engine as framework-only, and shipping a classifier
would breach it. `ScriptedClassifier` in `harness.go` exists solely for test.

What the framework owns: routing, thresholds, ambiguity detection, lifecycle,
fallback and validation.

### Resolution order, and why it is this order

1. **Noise first.** An unintelligible utterance is *not* a low-confidence
   intent. Treating it as one produces a clarification about a topic the caller
   never raised.
2. **Constrained expectation next.** When a yes/no is expected, "yes" means yes.
   Running it through general classification is how a confirmation gets misread
   as a new request — asserted by `TestIntent_YesNoShortCircuitsClassification`,
   which fails if the classifier is called at all.
3. **Confidence, then ambiguity, then completeness.**

### Ambiguity is not low confidence

An intent at 0.90 with a runner-up at 0.85 is **ambiguous despite being
confident**. A bare threshold cannot catch it, and it is the most common source
of confident wrong answers. `Intent.Margin()` exists for exactly this, and
`AmbiguityMargin` defaults to 0.15.

The accept/reject band (0.45–0.75) is deliberately wide: on a noisy line with
Indic-accented speech, one clarifying question costs far less than acting on a
misclassification.

---

## 7 · Response Planning Engine

`Planner.Plan` is a **pure function** of `PlanInput` — no clock, no map
iteration, no randomness. That is what makes the decision space exhaustively
table-testable (`TestPlanner_DecisionTable`) and what makes replay deterministic.

### The decision ladder

Priority order; each rung returns a complete decision.

| # | Condition | Action |
|---|---|---|
| 1 | Emergency raised | **Escalate** |
| 2 | Persona escalates on uncertainty, and verdict ≠ accept | **Escalate** |
| 3 | Turn / duration / interruption boundary reached | Escalate, else End |
| 4 | Noise | Ignore, or Clarify, or Escalate when repeats are spent |
| 5 | Clarification warranted **and affordable** | Clarify / Confirm / Ask |
| 5b | Clarification warranted and **exhausted** | **Escalate** — never ask again |
| 6 | Intent rejected | Escalate, else Reject |
| 7 | Confirmed denial ("no") | Respond — "no" is an instruction |
| 8 | Intent accepted and complete | **Respond** |
| 9 | Nothing matched | Wait — conservative default |

### Two clarification kinds are confidence-independent

`ClarifyContradiction` and `ClarifyIncomplete` clarify **even when the intent
classified confidently**. A contradiction is precisely the case where the
utterance was understood perfectly and conflicts with what we already know;
gating it on low confidence means a confident contradiction is silently acted
on. This was a real bug, caught by
`TestFailure_ContradictionTriggersConfirmation` — see
[ENGINEERING_AUDIT §F1](ENGINEERING_AUDIT.md).

---

## 8 · Clarification Engine

**The budget is the point.** A clarification engine without one produces the
single most recognisable voice-product failure: ask, mishear, ask again, caller
hangs up.

Three independent bounds:

| Bound | Default | Purpose |
|---|---|---|
| Persona `ClarificationBudget` | 2–4 | Total across the conversation |
| `MaxRoundsPerSubject` | 2 | Asking three *different* questions is reasonable; the same one three times is not |
| `RepeatOnNoise` | 2 | A caller on a bad line does not improve by being asked a third time |

**Exhaustion escalates. It never repeats.** `Reserve` returning false means hand
this to a human.

`Resolved` frees a subject's counter, so a caller who clarifies successfully is
not refused later for the same ambiguity.

---

## 9 · Context Engine

Five scopes, each a separate map so that a cross-scope read is a deliberate act
rather than a filter someone forgot.

| Scope | Lifetime | Expires |
|---|---|---|
| `Temporary` | One decision cycle | 30 s |
| `Conversation` | This call | 10 min |
| `Session` | Across calls in a session | 10 min |
| `Shared` | Concurrent calls, one subject | 10 min |
| `Business` | Reference data | **Never** |

Lookup precedence: Temporary → Conversation → Session → Shared → Business.
Most specific and most recent wins.

**Business context never expires**, because expiring it mid-call would make the
agent forget the opening hours it just quoted.

**Snapshots exclude Business and Shared.** Rolling back reference data because a
conversation errored would be worse than the error, and shared context belongs
to other conversations that did not fail.

`Export(maxSensitivity)` enforces a ceiling at the boundary rather than asking
callers to filter afterwards and occasionally forget.

---

## 10 · Persona Runtime

Four personas. A persona is **capabilities and constraints, not personality** —
there is no name, voice, tone or prompt, and the frozen UX record separately
forbids an anthropomorphic persona.

| Persona | May | Notably may **not** |
|---|---|---|
| Business Receptionist | Answer, clarify, take a message, transfer, disclose availability | **Disclose the subscriber's identity** |
| Personal Assistant | Answer, clarify, take a message, collect callback | Disclose identity **or availability**, transfer |
| Fraud Shield | Verify, clarify, end, escalate | **Everything else** — a hostile caller gets no surface |
| Emergency Assistant | **Escalate, hand over the dialer, end** | Everything else, including clarifying |

### Switching is one-way toward narrower

| Rule | Why |
|---|---|
| Any → Emergency, always | U7 is unconditional; a rule that could block it would eventually block it |
| Emergency → nothing | "We decided it wasn't an emergency after all" is not a mid-call judgement |
| Any → Fraud Shield | Narrowing capability is always safe |
| Fraud Shield → broader: **denied** | A caller who talked their way out of fraud screening is exactly the attack |

Narrowing sets a lock, so a future registered persona cannot open a path out by
accident. Emergency remains reachable even when locked.

---

## 11 · Conversation Policy Engine

Rules are **pure functions** evaluated in class order. **Deny overrides, and
safety overrides everything.**

| Class | Overridable | Contains |
|---|---|---|
| `Safety` | **Never** | Emergency confinement, terminal protection |
| `Persona` | No | Capability boundaries |
| `Boundary` | No | Turns, duration, clarification budget, interruption storm |
| `Business` | — | Commercial preference |

There is **no mechanism** for a business rule to permit what a safety rule
forbade. Evaluation stops at the first deny and safety runs first — the moment
such a mechanism exists, someone uses it during an incident to restore
throughput.

Boundary rules always leave `End`, `Escalate` and `Transfer` permitted, or a
conversation at its limit would wedge with no legal exit.

---

## 12 · Interruption Engine

| Kind | Priority | Resume | Why |
|---|---|---|---|
| Emergency | 5 | **Never** | The call is over; a human has it |
| Transfer | 4 | Never | Same |
| Provider | 3 | Checkpoint, or restart if < 400 ms in | The caller heard a legitimate partial answer |
| AI (self) | 2 | Abandon | It stopped because what it was saying was wrong |
| User (barge-in) | 1 | **Abandon** | Replaying the rest of the sentence is the most irritating thing a voice system can do |

Checkpoints record **position, never content** — the transcript owns what was
said, and duplicating it would create a second copy of SENSITIVE data with
weaker handling.

`EmergencyRaised()` is monotonic. Once true it stays true, because a flag that
could be cleared would permit resuming a call that must not resume.

---

## 13 · Latency Controller

Total decision-cycle budget: **150 ms**, deliberately small. ADR-0011 allocates
900 ms p50 end-to-end, nearly all of it to recognition, inference and synthesis.
The engine sits between them and must be close to free.

| Stage | Budget | Skippable |
|---|---:|---|
| Turn detection | 10 ms | Yes |
| Intent | 60 ms | Yes |
| Context | 15 ms | Yes |
| **Policy** | 20 ms | **No** |
| Planning | 35 ms | Yes |
| **Transition** | 10 ms | **No** |

**Policy is never skippable.** Frozen invariant I11 permits shedding at
admission and downgrading a tier; it forbids skipping the safety layer, and
policy evaluation is where safety rules live. A budget controller that could
skip it would defeat I11 from a direction nobody was watching.

Transition is unskippable for a different reason: skipping it leaves the
conversation in a state that disagrees with what it did.

Persona `LatencyProfile` scales the whole allocation — emergency gets 0.5×
because taking longer to get out of the way is itself a failure.

---

## 14 · Conversation invariants

| # | Invariant | Enforced by |
|---|---|---|
| **INV-CV-1** | Every event kind is explicitly handled; an unknown kind errors rather than silently no-ops | `Handle` default arm |
| **INV-CV-2** | The opening turn is non-yielding; a barge-in during it is queued, not discarded | `TurnManager.arbitrateLocked` |
| **INV-CV-3** | `Conversation.transition` is the only writer of state | Structure — no other function calls `fsm.To` |
| **INV-CV-4** | `Thinking` is entered only when a decision is genuinely in flight | `handleUtterance` |
| **INV-CV-5** | Intent lifecycle is monotonic | `IntentEngine.Advance` |
| **INV-CV-6** | Recovery restores only a retained snapshot; otherwise escalate | `Conversation.Recover` |
| **INV-CV-7** | Policy evaluation is never skipped under budget pressure | `Stage.Skippable` |
| **INV-CV-8** | Emergency is irreversible and outranks everything | `InterruptionEngine`, `switchAllowed`, safety rule |
| **INV-CV-9** | Clarification is bounded; exhaustion escalates | `ClarificationEngine.Reserve` |
| **INV-CV-10** | Caller text reaches only the classifier; it is never logged, labelled or traced | `Utterance.Text` is the sole carrier |

Most are enforced by **absence** — a missing edge, a missing command, a missing
config field. Enforcement by absence cannot be forgotten or misconfigured.

---

## 15 · Determinism

Given the same event sequence and the same injected clock, the engine produces
the same state trace, the same plans and the same metrics.

| Source of nondeterminism | How it is removed |
|---|---|
| Wall clock | Everything goes through `rt.Clock` |
| Map iteration | Policy rules sorted by class/priority/name; candidates sorted by confidence then name; `MissingRequired` sorted |
| Equal-confidence candidates | Tie-broken by name, so the same input always resolves the same way |
| Randomness | None exists in this package |

Asserted by `TestSim_DeterministicReplay`, which runs a full dialogue 16 times
and compares every state and every action.

---

## 16 · Concurrency

Every exported type is safe for concurrent use unless documented otherwise;
`Harness` and `Simulator` are single-test-scoped.

| Structure | Strategy |
|---|---|
| `Engine.active` | `sync.Map` — write-once per conversation, read rarely |
| `Conversation` | RWMutex over trace and plan; subsystems own their own locks |
| `TurnManager` | Single mutex — floor decisions must be totally ordered |
| `PolicyEngine`, `PersonaRuntime` | RWMutex, copy-on-read; **copy-on-write** for registration |
| `Metrics` | RWMutex on the label map, atomics per series |
| `Planner` | **Stateless** — nothing to synchronise |

No global mutable state. Two engines in one process share nothing, which is what
makes the suite `t.Parallel()` throughout.

---

## 17 · Testing

| Suite | Count | Covers |
|---|---:|---|
| Unit (`conversation_test.go`) | **47** | State machine, turns, interruption, intent, context, persona, policy, clarification, planner, latency |
| Simulation & integration (`simulation_test.go`) | **23** | Full dialogues, failure injection, stress, concurrency, metrics |
| Benchmarks (`bench_test.go`) | **18** | Every hot path |

Verified: `go vet` clean · `gofmt` clean · **70 tests pass at `-count=10
-shuffle=on`** · workspace builds with 10A and platform intact.

**Not verified: `-race`.** No C toolchain on the development machine — same gap
as Phase 10A, tracked as [ENGINEERING_AUDIT §A2](ENGINEERING_AUDIT.md) and
blocking.

---

## 18 · Deliberate omissions

Per the brief's STOP list, verified absent:

| Excluded | Verified |
|---|---|
| LLM prompts | No prompt text anywhere; `Plan` carries an action, never words |
| Memory reasoning | `ContextEngine` manages scope and expiry, not meaning |
| Tool execution | `StateToolExecution` models the *wait*; nothing executes |
| Telephony intelligence | No import, no type, no reference |
| Fraud intelligence | `PersonaFraudShield` is a capability set; no scoring |
| Business workflows | Policy rules are boundaries, not processes |

Also absent by design: config hot-reload, a global registry, persona
personality, and any classifier implementation.
