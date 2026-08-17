# ADR-0016: Intent classification — implement the existing port, do not build a second engine

- **Status:** Accepted
- **Date:** 2026-08-16
- **Deciders:** repository owner
- **Consulted:** `packages/go/conversation` (Phase 10B) design record; Phase 11E
  voice pipeline; Phase 13 inspection findings
- **Informed:** anyone adding a classifier, a model backend, or a new
  conversational capability

---

## Context

Phase 13 is chartered to give the voice system "intent, turn semantics,
interruption context, confidence and response strategy."

**Inspection found that almost all of that already exists, frozen, in
`packages/go/conversation`.** That module is 5,205 lines of non-test code and
already contains:

| Capability | Where |
|---|---|
| Intent model | `intent.go` — `IntentName`, `Intent`, `Slot`, `Candidate`, `IntentState`, `IntentVerdict`, `IntentEngine` |
| Confidence | `IntentConfig` — `AcceptThreshold`, `RejectThreshold`, `AmbiguityMargin`, `MinASRConfidence`; `Intent.Margin()` |
| Bounded context | `context.go` — `ContextEngine`, `Entry`, `Scope`, `Sensitivity`, `Snapshot` |
| Clarification | `clarification.go` — `ClarificationEngine`, `ClarifyLowConfidence`, `ClarifyContradiction` |
| Turn semantics | `turn.go` — `TurnManager`, `Expectation` |
| Interruption | `interruption.go` — `InterruptionEngine`, `ResumePolicy`, `Checkpoint` |
| Response strategy | `planner.go`, `policy.go`, `Plan.Action` |

`Conversation` wires all of them together at `engine.go:263–275`.

**The gap is narrow and specific.** `conversation` defines the port:

```go
type IntentClassifier interface {
	// Classify returns candidates ordered highest-confidence first. An empty
	// result is legitimate and means "nothing recognised".
	Classify(u Utterance, expect Expectation) ([]Candidate, []Slot, error)
}
```

and then, at `harness.go:94`, records the problem in its own words:

> *"It is the ONLY implementation of [IntentClassifier] in this module"*

— referring to `ScriptedClassifier`, a deterministic **test double**.

`intent.go:277` states the consequence:

> *"classifier may be nil. An engine with no classifier resolves every utterance
> to the fallback intent."*

And `voice` never supplies one: `conversation.NewEngine` appears exactly once in
the repository, in `packages/go/voice/e2e_test.go:328`, without
`WithClassifier`.

**So today every voice utterance resolves to the fallback intent.** The
machinery is complete and unreachable. Phase 13's real job is to supply the
missing implementation, not to build a second machine.

## Decision Drivers

- **Do not duplicate a frozen subsystem.** A second intent engine, context store
  or clarification policy would be two sources of truth for one decision.
- **Do not modify frozen modules.** 10A–10F, 10.5, 11A–11E are closed.
- **No credential, no network, no model download** for the default path.
- **Determinism**, so the classifier is testable without a model.
- **A model-backed classifier must be addable later without re-architecting.**

## Considered Options

1. **Implement `conversation.IntentClassifier` in a new leaf module.**
2. A new independent `intelligence` package re-deriving intent, context, turn
   and clarification semantics.
3. A provider-specific classifier (e.g. bound to Ollama or a cloud NLU).
4. A classifier that depends directly on a model runtime.
5. Give the intelligence layer its own routing and governance path.

## Decision Outcome

**Chosen: Option 1.** Phase 13 implements the existing
`conversation.IntentClassifier` port from a new leaf module. The default
implementation is a **deterministic rule/lexicon classifier** with no model, no
network and no credential.

### 1. Why reuse the existing port

Because it already exists, is already wired, and is already the documented
extension seam. `intent.go:99–115` describes the interface as deliberately
narrow, and the module explains why the parameter is an `Utterance` rather than
a string:

> *"The parameter is deliberately opaque. Passing an Utterance rather than a
> string keeps the engine from ever touching caller text: it hands the utterance
> through and receives a shape back."*

The port is the architecture's answer to exactly the question Phase 13 asks.
Implementing it costs one module and changes nothing frozen. Everything
downstream — thresholds, verdicts, clarification, context, turn state — is
already built and already tested.

### 2. Why a parallel intelligence engine is rejected

A second engine would mean two intent models, two confidence policies, two
context stores and two clarification decisions for one conversation. When they
disagreed — and they would — nothing in the system would say which is
authoritative.

It also contradicts constraints this platform already enforces elsewhere: no
second router (ADR-0006 / `speech.ProviderRouter`), no second governance engine,
no second interruption mechanism (Phase 11E), no second correlation identity
(ADR-0014). The same reasoning applies here and is not weakened by the subject
being "intelligence" rather than routing.

Concretely, it would also duplicate `ContextEngine` — and a second context store
that outlives a turn is a second memory system, which Phase 13's own charter
forbids.

### 3. Approved classifier architecture

- **Default: deterministic rule/lexicon classifier.** A pure function of
  `(Utterance, Expectation, config)`. Same input, same output, every time.
- **No provider-specific dependency in the classifier core.** It must not import
  `speech`, `voice/providers/*`, `runtime` model plumbing, or any vendor SDK.
- **No network and no API dependency** for the default implementation.
- **The classifier owns vocabulary bounding.** This is a real obligation rather
  than an inherited guarantee: `IntentName` is an open `string` type
  (`intent.go:14`) with only four reserved constants — `unknown`, `fallback`,
  `affirm`, `deny`. The frozen port does **not** constrain the vocabulary, so
  the classifier must, and must never emit a name outside its declared set.
- **Empty results are legitimate.** "Nothing recognised" is an empty candidate
  list, which is distinct from a low-confidence candidate. Conflating the two
  produces a clarification about a topic the caller never raised — the failure
  mode `intent.go:298` already warns about.

### 4. Confidence semantics — inherited, not invented

The classifier adopts the thresholds the frozen `IntentConfig` already defines
and does not introduce alternatives:

| Setting | Value | Meaning |
|---|---|---|
| `AcceptThreshold` | **0.75** | at or above, the intent is acted on |
| `RejectThreshold` | **0.45** | below, the candidate is discarded |
| `AmbiguityMargin` | as configured | gap to the nearest alternative |
| `MinASRConfidence` | as configured | recogniser floor |

Between 0.45 and 0.75 is the clarification band, which is why
`ClarifyLowConfidence` exists.

**Margin matters independently of the top score.** `Intent.Margin()` exists
because a high-confidence top candidate with a near-equal runner-up is ambiguous
even though it clears the accept threshold — a case a threshold alone cannot
catch. The classifier must therefore return *ordered alternatives*, not only a
winner, or the margin is uncomputable and the ambiguity is invisible.

`Confidence` is a `float64` in [0,1]. **"Unknown" is not zero confidence** — it
is the absence of a candidate.

### 5. Dependency direction and module boundaries

```
voice ──(Planner port)──► conversation ──(IntentClassifier port)──► intent
                               │                                      │
                               └──────────► runtime, metrics ◄─────────┘
```

Both arrows point at the frozen module through a narrow interface, and the
frozen module points at neither implementation. `conversation`'s dependency
closure is `metrics` and `runtime` only — verified with `go list -deps`; it
imports no `governance`, `toolruntime` or `memory`, and adding a classifier must
not change that.

The classifier module is a **leaf**: nothing in the platform imports it except
composition code that wires a conversation engine.

### 6. Frozen-module impact

**None from this ADR (T1), which changes documentation only.**

For Phase 13 as planned, also none: the work is a new leaf module implementing
an existing interface. Two tasks carry a risk that is recorded rather than
assumed —

- **T6 (voice ↔ conversation bridge).** Present evidence says `voice.Planner`
  (`pipeline.go:96`) is a sufficient seam. If it proves insufficient, the phase
  **stops and reports** rather than editing Phase 11E or 10B.
- **T14 (CI).** `hardening.yml`'s `AI_MODULES` is a hard-coded list of 14
  modules, so a new module does not enter the nightly race gate automatically.
  That edit is CI configuration, not a frozen module, and requires approval.

### 7. Rejected alternatives

**Parallel intelligence engine** — two sources of truth for one decision;
duplicates a frozen subsystem; creates a second context store, which is a second
memory system.

**Provider-specific classifier** — binds conversational meaning to whichever
provider happens to be configured, so swapping providers would silently change
what the platform understands. It also drags provider availability into a code
path that must work when no model exists at all.

**Direct model dependency in the classifier core** — makes the default path
require a runtime, a download, or a credential, and makes classification
untestable without one. ADR-0006 already confines model choice to routing tiers;
this would smuggle it into the conversation layer.

**Second routing or governance path** — every tool-capable action must continue
through the existing governance boundary. The classifier proposes meaning; it
decides nothing and executes nothing. It holds no governance or tool reference,
and that is enforceable structurally by its import set.

### 8. Future model-backed classifier — the seam only

A model-backed classifier is added by **implementing the same interface**:

```go
type ModelClassifier struct{ /* … */ }
func (m *ModelClassifier) Classify(u Utterance, expect Expectation) ([]Candidate, []Slot, error)
```

and passing it to `conversation.WithClassifier`. Nothing else changes: the
engine, thresholds, verdicts, clarification policy and context all stay put,
because they were never coupled to how candidates are produced.

Consequences that would need deciding **at that time, not now**:

- whether it needs a credential or a local model (ADR-0006 governs which model
  tiers are permissible; Ollama remains development-only),
- latency against ADR-0011's budget, since classification would move onto the
  turn's critical path,
- how a provider outage degrades — the natural answer being to fall back to the
  deterministic classifier, which is another reason to build that one first.

**This ADR does not adopt a model, and no accuracy claim is made or implied by
it.** A deterministic rule classifier measured against hand-written fixtures
measures the fixtures; it is not natural-language-understanding accuracy and
will not be reported as such.

### Consequences

**Positive**

- The frozen intelligence machinery becomes reachable for the first time.
- No frozen module changes; no third-party dependency; no credential.
- Classification is deterministic and testable with no model present.
- A model backend remains a drop-in behind an unchanged interface.

**Negative**

- A rule/lexicon classifier understands only what it is told to understand. Its
  ceiling is real and must be stated in the limitations, not discovered.
- Vocabulary bounding is the classifier's responsibility rather than the port's,
  so it is enforceable only by that module's own tests.
- Phase 13 delivers less novel machinery than its charter implies — because the
  machinery already existed.

**Neutral**

- `ScriptedClassifier` stays as the test double it was designed to be.

### Confidence

**High.** The port, its documentation and its single test-double implementation
all point at this design, and the decision is reversible: a classifier is one
module behind one interface.

### Revisit Trigger

Revisit when **any** of the following is first observed:

- The deterministic classifier's fallback rate in real traffic exceeds a level
  the product finds acceptable — the concrete signal that a model is needed.
- A model-backed classifier is approved, which requires its own ADR covering
  credential, latency budget and degradation.
- `conversation.IntentClassifier` changes shape, which would be a frozen-contract
  change and an ADR in its own right.

## References

- `packages/go/conversation/intent.go` — the port, `IntentConfig` thresholds,
  `IntentName`, the classification-order policy
- `packages/go/conversation/harness.go:94` — "the ONLY implementation"
- `packages/go/conversation/engine.go:246–275` — where the engines are wired
- `packages/go/conversation/turn.go:72` — `Expectation`
- `packages/go/voice/pipeline.go:96` — the `Planner` seam
- [ADR-0006](0006-llm-routing-strategy.md) — model routing; Ollama is
  development-only
- [ADR-0011](0011-end-to-end-latency-budget.md) — the budget a model classifier
  would have to fit
- [ADR-0012](0012-privacy-dpdp-consent-retention.md) — utterance text is
  SENSITIVE and must never become a label
- [ADR-0014](0014-correlation-identity.md) — precedent for "one mechanism, not
  two"
- `docs/phase13/DEPENDENCY_MAP.md`
- Supersedes: none. Superseded by: none.
