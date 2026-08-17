# Phase 13 — Intent Model

Deterministic, closed-vocabulary, rule/lexicon matching. **Not model-based NLU.**
No accuracy claim is made anywhere in this document.

## Closed vocabulary — 11 names

Source: `packages/go/intent/lexicon.go` (`Vocabulary()`), verified by
`TestPackage_*` vocabulary guards.

| Name | Meaning |
|---|---|
| `greeting` | opening pleasantry, no request attached |
| `caller_identity` | caller states who they are |
| `call_purpose` | caller states why they are calling |
| `leave_message` | request to leave a message |
| `request_callback` | request to be called back |
| `request_transfer` | request to reach the person being screened |
| `repeat` | request to repeat what was just said |
| `hold` | request to wait |
| `end_call` | request to end the call |
| `affirm` | frozen reserved name (`conversation.IntentAffirm`) |
| `deny` | frozen reserved name (`conversation.IntentDeny`) |

Plus the frozen reserved `unknown` and `fallback`. **No intent name outside this
set may be produced** — `New()` rejects out-of-vocabulary rules at construction,
and tests assert every emitted name is a member.

## Candidate generation

1. `tokenize(text)` — lowercase, alphanumeric runs, **bounded at
   `maxTokens = 512`** (`lexicon.go:217`). The tokenizer *returns early* once the
   bound is reached (`lexicon.go:247`), so longer input costs no more.
2. For each rule (iterating a **slice**, never a map — map order is randomised
   per process): `score(tokens, rule)` accumulates evidence.
3. Evidence is counted **per cue, not per occurrence**. MEASURED: 600 repetitions
   of one cue score identically to a single occurrence
   (`TestT13_OversizedInputRespectsTokenBound`).
4. Multi-word cues weigh more: `cueWeight` returns `len(cue)+1` for phrases, `1`
   for a lone token — so "call me back" is decisive and a stray "back" is not.

## Confidence calculation

```
conf = evidence / saturation      (saturation defaults to 3.0)
conf = min(conf, 1.0)
```

MEASURED attainable values with the real lexicon: **0.333** (one single-token
cue), **0.667** (two), **1.0** (a phrase cue, or three singles). This matters —
0.667 is what makes the low-confidence band reachable through genuine input at
all. See [CONFIDENCE_MODEL.md](CONFIDENCE_MODEL.md).

## Candidate ordering — deterministic

`sortCandidates` (`lexicon.go:279`): confidence **descending**, ties broken by
name **ascending**. Mirrors the frozen engine's own rule. VERIFIED LOCALLY over
200 repetitions (`TestT11_CandidateOrderingIsDeterministic`) using a fixture
with genuinely distinct confidences.

Bounded by `Config.MaxCandidates` (`DefaultMaxCandidates = 5`).

## Slots

Closed 6-name slot vocabulary: `caller_name`, `company_name`, `party_name`,
`callback_number`, `time_reference`, `message_body`. Bounds:
`MaxSlotsPerIntent = 4`, `MaxSlotNameLen = 32`, value tokens ≤ 6, digit runs
7–15.

Slots are returned **for the resolved (top) intent only**, and carry
**shape only** — `Name`, `Filled`, `Confidence`, `Required`. The frozen
`conversation.Slot` has **no value field**, so slot *values* never cross the
port; anything needing values uses the context engine.

Slot ordering is name-ascending (`slots.go:241`). FINDING: that sort is
currently a **no-op**, because every spec table is already declared
name-ascending — proven by a control run in T11 that passes with the sort
removed. It is retained as defence; the ordering *contract* has teeth (T11 M2).

## The three outcomes — kept apart

**Never collapsed.** The frozen engine in fact distinguishes **four**:

| Outcome | Classifier result | Verdict → plan |
|---|---|---|
| **unknown** | **0 candidates** | fallback → `ActionRespond`, reason `fallback` |
| **below-reject** | 1 cand < 0.45 | `IntentReject` → `ActionEscalate`, reason `intent_rejected` |
| **low confidence** | 1 cand in [0.45, 0.75) | `IntentClarify` → `ActionConfirm`, `ClarifyLowConfidence` |
| **ambiguous** | ≥2 cands, margin < 0.15 | `IntentClarify` → `ActionClarify`, `ClarifyAmbiguous` |

VERIFIED LOCALLY by `TestT13_FourOutcomesStayDistinct`, which requires four
*different* `(action, reason, clarification)` triples — asserting each in
isolation would not catch a merge.

`Intent.Margin()` (frozen, `intent.go:89`) = top confidence minus the best
differently-named alternative. `Alternatives` carries the full candidate list.

## Malformed input

MEASURED (`TestT13_EvaluationFixtures`): empty, whitespace-only, control bytes
and unknown vocabulary all yield **0 candidates → fallback**, no panic. Oversized
input is truncated at the token bound. No raw payload reaches any operational
field — see [SECURITY.md](SECURITY.md).

## Expectation rule

The single expectation rule: under `ExpectSlotValue`, a lone incidental keyword
does **not** start a new intent (a phrase-level cue still does). Rationale:
"Monday" answering "which day?" should fill a slot, not launch a request.
Under `ExpectYesNo` the frozen engine short-circuits to affirm/deny before
general classification.
