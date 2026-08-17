# Phase 13 — Dependency Map

**Produced before any Phase 13 code was written.** Every relationship below was
read out of the repository at `acc1f81`, not inferred from documentation. Where
a claim came from a command, the command is named.

See [ADR-0016](../adr/0016-intent-classification.md) for the decision this map
supports.

---

## 1. Modules relevant to Phase 13

| Module | Role in Phase 13 | Frozen |
|---|---|---|
| `packages/go/conversation` | Owns intent, context, clarification, turn and interruption semantics. **Defines the `IntentClassifier` port.** | **YES** (10B) |
| `packages/go/voice` | Orchestrates the voice turn. Consumes conversation through the `Planner` port. | **YES** (11E) |
| `packages/go/runtime` | Clock, identifiers, generation contracts. | **YES** (10A) |
| `packages/go/metrics` | The single instrument implementation. | **YES** (10.5) |
| `packages/go/governance` | The only decision boundary for tool-capable actions. | **YES** (10E) |
| `packages/go/audiointel` | Owns barge-in **detection**. | **YES** (11D) |
| `packages/go/speech` | Owns speech contracts and provider routing. | **YES** (11C) |
| `packages/go/evaluation`, `evalsubjects` | Evaluation harness and fixtures. | **YES** (10F) |
| `packages/go/metricsexport` | Prometheus exposition (Phase 12 T5). | No |
| **`packages/go/intent`** | **Planned** — the classifier implementation. | No (new) |

## 2. Current dependency direction

Verified with `go list -deps` per module.

```
voice ──► conversation ──► runtime ──► metrics
  │            │
  │            └──► metrics
  ├──► speech, media, audiointel, audiobridge, governance, runtime, metrics
  └──► (never: memory, toolruntime)
```

**`conversation`'s complete first-party closure is `metrics` and `runtime`.**
It imports no `governance`, no `toolruntime`, no `memory`. That narrowness is
what makes it safe to hang a classifier off it, and Phase 13 must not widen it.

## 3. Classifier ownership

| Concern | Owner | Note |
|---|---|---|
| The **port** `IntentClassifier` | `conversation` (frozen) | `intent.go:111` |
| The **engine** that applies verdicts/thresholds | `conversation` (frozen) | `IntentEngine`, `intent.go:263` |
| An **implementation** of the port | **`packages/go/intent` (Phase 13)** | none exists in production today |
| The **test double** | `conversation/harness.go:94` | `ScriptedClassifier` — explicitly "the ONLY implementation" |

Exact contract Phase 13 must satisfy:

```go
Classify(u Utterance, expect Expectation) ([]Candidate, []Slot, error)
```

- `Utterance.Text` is **SENSITIVE** (`intent.go:117–125`) — the only caller
  content in the module.
- `Expectation` is a **closed enum of 4** (`turn.go:72`): `ExpectNothing`,
  `ExpectDisambiguation`, `ExpectYesNo`, `ExpectSlotValue`.
- `Candidate` is `{Name IntentName; Confidence float64}`, **ordered
  highest-first**.
- Returning an empty slice is legitimate and means "nothing recognised".

**`IntentName` is an open `string` type** (`intent.go:14`) with four reserved
constants — `unknown`, `fallback`, `affirm`, `deny`. The frozen port therefore
does **not** bound the vocabulary; the classifier must, and only its own tests
can enforce that.

## 4. `voice` → `conversation`

`voice` depends on conversation through **one narrow port** (`pipeline.go:96`):

```go
type Planner interface {
	Handle(e conversation.Event) (conversation.Plan, error)
}
var _ Planner = (*conversation.Conversation)(nil)   // pipeline.go:123
```

The call site is `pipeline.go:942`:

```go
plan, err := p.cfg.Planner.Handle(conversation.Event{
	Kind:      conversation.EventUtterance,
	Utterance: conversation.Utterance{Text: final.Text},
	Party:     conversation.PartyCaller,
})
```

**Measured surface:** `voice` references 15 distinct `conversation.*` symbols,
and **none of them is an intelligence engine** —
`IntentEngine`, `ContextEngine`, `ClarificationEngine`, `InterruptionEngine` and
`IntentClassifier` each appear **0 times** in `voice`.

**Consequence:** the intelligence exists but is unreachable from voice, and with
no classifier configured every utterance resolves to the fallback intent
(`intent.go:277`). `conversation.NewEngine` appears exactly once in the whole
repository — `voice/e2e_test.go:328` — and without `WithClassifier`.

## 5. `conversation` → classifier port

```
Conversation.Handle
   └── IntentEngine.Resolve(utterance, expectation)
          └── IntentClassifier.Classify(...)      ◄── Phase 13 plugs in here
                 └── candidates + slots
          └── applies MinASRConfidence, RejectThreshold,
              AcceptThreshold, AmbiguityMargin  → IntentVerdict
```

Wiring is `engine.go:246` (`NewIntentEngine(cfg.Intent, e.classifier, …)`) and
injection is `engine.go:201` (`WithClassifier`). Every engine a `Conversation`
holds is built at `engine.go:263–275`: `fsm, turns, intents, context, clarify,
personas, policy`.

**Phase 13 supplies one argument to an existing constructor. It adds no stage.**

## 6. `runtime` and `metrics` relationships

- **`runtime`** — clock (`rt.Clock`, `rt.FakeClock` for deterministic tests) and
  identifiers, including `runtime.CorrelationID` as unified by ADR-0014. The
  classifier takes a clock rather than calling `time.Now()`, so tests are
  deterministic.
- **`metrics`** — the single instrument implementation; `conversation` already
  owns conversation-layer instruments (`conversation/metrics.go`). Any Phase 13
  instrument must use `packages/go/metrics` and must respect the platform's
  bounded, closed label vocabulary (38 keys measured in Phase 12 T5).

**Hard rule inherited from ADR-0012 and Phase 12 T5:** transcript, utterance
text, slot values, PCM and credentials must never appear in a metric label or a
bounded operational event.

## 7. Forbidden dependencies for `packages/go/intent`

| Must NOT import | Why |
|---|---|
| `toolruntime` | The classifier proposes meaning; it must be structurally incapable of executing anything. |
| `governance` | It must not be able to make or influence a policy decision. Governance stays the single boundary. |
| `memory` | A classifier that writes memory is a second memory system. |
| `speech`, `media`, `audiobridge`, `voice/providers/*` | No provider-specific coupling; classification must not depend on which provider is configured. |
| `platform`, `net/http`, any server/CLI | It is a library, not a service. |
| **any third-party module** | Contained per ADR-0015; the default classifier is stdlib + first-party only. |

Enforceable structurally with `go list -deps`, the same technique Phase 11E used
for its import guard.

**Permitted:** `conversation` (for the port and its types), and transitively
`runtime` and `metrics`.

## 8. Frozen modules — unchanged by Phase 13 as planned

`speech` · `runtime` · `governance` · `conversation` · `memory` · `toolruntime` ·
`audiointel` · `audiobridge` · `media` · `metrics` · `evaluation` ·
`evalsubjects` · `telephony`

Digests are compared against the Phase 12 baseline at every checkpoint. T1
changed no `.go` file at all.

## 9. Planned Phase 13 dependency additions

| Addition | Kind | Approved by |
|---|---|---|
| `packages/go/intent` → `conversation` | first-party | ADR-0016 |
| `packages/go/intent` → `runtime` (clock) | first-party, transitive | ADR-0016 |
| `packages/go/intent` → `metrics` (optional instruments) | first-party, transitive | ADR-0016 |
| `go.work` entry for the new module | workspace | T2 |

**No third-party dependency is planned.** If one becomes genuinely necessary,
Phase 13 stops and requests approval, per ADR-0015's precedent.

## 10. T6 — the frozen-risk boundary

T6 wires a real classifier into a conversation engine that `voice` can use.

**Expected:** no frozen change. `voice.Planner` accepts any `Handle` implementer,
and `conversation.WithClassifier` already exists, so a composition helper in the
new module should suffice.

**The risk:** `voice` constructs no conversation engine in production code today
— only in a test. If wiring one requires changing `voice` (11E) or
`conversation` (10B), **T6 stops and reports**:

- exact file and signature
- why the gate cannot otherwise be met
- the smallest possible change
- compatibility impact
- a proposed ADR

**It does not edit the frozen module.**

## 11. T14 — the CI approval boundary

| Workflow | Picks up a new module? |
|---|---|
| `pr-go.yml` | **Automatically** — discovers modules from `go.work` |
| `hardening.yml` | **No** — `AI_MODULES` is a hard-coded list of 14 entries |

So a new module gets per-push `-race` for free, but **not** the nightly
repeated-shuffled race gate — the mode that found the `toolruntime` data race in
Phase 12 Q1. Adding it requires editing `hardening.yml`, which is CI
configuration outside default scope. **T14 requests approval; it does not edit
the workflow unilaterally.**

## 12. Out of scope, tracked elsewhere

- **F2** — frozen Phase 10F `LatencyRatio: 2.0`; keeps the release gate red.
  Not touched by Phase 13.
- **B3 trace export** — BLOCKED ON APPROVAL; needs an ADR and a third-party
  dependency.
- **Service wiring** — the AI plane is imported by no service; pre-existing and
  broader than Phase 13.
