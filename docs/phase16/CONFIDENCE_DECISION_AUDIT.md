# Phase 16 T12 — ASR Confidence Decision Audit

**Status: AUDIT COMPLETE. No code changed by T12.** Its recommendation was
approved and implemented by T14; see
[CONFIDENCE_POLICY_IMPLEMENTATION.md](CONFIDENCE_POLICY_IMPLEMENTATION.md).

Line references in this document were re-verified against the tree at the time
of writing, **after** the T14 amendment shifted them. The gate T12 originally
cited as `intent.go:309` is now `intent.go:333`.

---

## 1. The question

Does an utterance from a recogniser that reports **no** confidence pass the
acoustic noise gate, and how is "not reported" expressed to a frozen engine
whose field is documented `0..1`?

## 2. The trace, from source

| Stage | Location | Fact |
|---|---|---|
| Tool invocation | `whispercli.go:482` | `--output_format txt` |
| Origin | `whispercli.go:691` | `Confidence: speech.ConfidenceUnknown`, hardcoded |
| Sentinel | `speech/transcript.go:112` | `ConfidenceUnknown = -1.0` |
| Contract permits it | `speech/transcript.go` validation | `!= ConfidenceUnknown && (…<0 \|\| …>1)` |
| Pipeline seam | `voice/pipeline.go:1072-1074` | `ASRConfidence: final.Confidence` — forwarded verbatim |
| Gate | `conversation/intent.go:333` | the sole numeric comparison |
| Classifier | `intent/classifier.go:148` | **declines to read it** — "deliberately NOT used to weight the score" |

**Does WhisperCLI expose a confidence? No — not on the path used.**
`--output_format txt` carries none. `avg_logprob` exists only under
`--output_format json`, and it is a mean token log-probability, **not** a
calibrated `[0,1]` confidence.

**Two singular seams, and this is the structural point:**

- `pipeline.go:1072` is the **only** production site constructing a
  `conversation.Utterance` (`conversation/harness.go` is test-support).
- `intent.go:333` is the **only** reader of `ASRConfidence` anywhere.

## 3. The decisive finding

| Provider | Confidence | Kind |
|---|---|---|
| `whispercli.go:691` | `ConfidenceUnknown` | **real** |
| `whispercpp.go:487` | `ConfidenceUnknown` | **real** |
| `speech/harness.go` | `0.7`, `0.95` | **fake** |

**Every real provider in this repository reports no confidence. Only test fakes
emit numbers.** The acoustic gate therefore governed *zero* real providers — it
had only ever been exercised against synthetic values. Enabling it for real
traffic would have been a change in kind, not degree.

## 4. Why the sentinel is not a low confidence

`speech/transcript.go:107-111` states it directly: *"a provider reporting zero
confidence and a provider reporting nothing at all are different situations, and
a router that conflated them would treat silence as certainty of failure."* The
pre-T14 gate committed exactly that conflation one layer above the sentinel:
`-1.0 < 0.40` is arithmetically true and semantically meaningless, because
`-1.0` was never a measurement.

## 5. Every non-frozen route was closed

| Route | Verdict |
|---|---|
| Pipeline substitutes a number | Forbidden — fabricates the confidence the provider refused to invent |
| Configuration | **Impossible.** `MinASRConfidence` is bounded to `[0,1]`. Even at `0`, `-1.0 < 0` is **true** — still noise |
| Classifier injection | **Impossible.** The gate returns *before* `Classify` is called |
| Gate injection | **No seam.** `IntentEngine` is built inside `Begin`, held unexported; the complete `Option` set is `WithClock`, `WithLogger`, `WithMetrics`, `WithClassifier` |

A fifth constraint compounded it: `packages/go/conversation` does **not** depend
on `packages/go/speech`, and its `go.mod` documents that as deliberate. It could
not name `speech.ConfidenceUnknown` without a new inter-module dependency.

**Conclusion: the fix necessarily required amending frozen `conversation`.** T12
stopped there rather than proceed unapproved.

## 6. Options evaluated

| | Option | Verdict |
|---|---|---|
| A | Unavailable confidence passes the gate | **Recommended** — needs product approval |
| B | Treat unavailable as noise | REJECT as a deliberate choice (it was the default) |
| C | Map to a synthetic number | **REJECT** — fabrication; two provider tests exist to prevent it |
| D | Extend the contract to represent "unknown" | Needs product decision; larger frozen change |
| E | Require every provider to supply a confidence | **REJECT** — collapses into C at the adapter |

## 7. Fixture provenance sub-audit

Recorded separately in [FIXTURE_PROVENANCE.md](FIXTURE_PROVENANCE.md) and
decided by [ADR-0023](../adr/0023-binary-test-fixture-provenance.md).

## 8. Task status

| Category | T12 |
|---|---|
| **Implemented and verified** | Nothing. T12 was an audit and changed no code. |
| **Measured but not implemented** | The whole cascade, derived from source: `ConfidenceUnknown` → `IntentNoise` → two `noise_repeat` → `noise_unrecoverable` → terminal. Derived only — **executed** later by [T13](CONFIDENCE_CASCADE_EVIDENCE.md). |
| **Known limitations** | The cascade was argued from reading, not run. T12 said so explicitly and refused to implement on that basis. |
| **Mutation results** | None. No tests were added. |
| **Deterministic test status** | N/A — no new tests. Existing suites re-run green. |
| **Race status** | `-race NOT RUN LOCALLY — no race-safety claim; CI authoritative` (`CGO_ENABLED=0`, gcc absent). |
| **Frozen-module integrity** | All frozen modules unchanged. `conversation` carried only the Phase 15 T4 amendment at the time. |
| **Security / dependency** | No network, no model download, no credential, no new dependency, no persistence. Read-only source analysis. |
| **Resume point at the time** | Blocked on a product decision — did unreported confidence pass the gate? Answered and implemented by [T14](CONFIDENCE_POLICY_IMPLEMENTATION.md). |

## 9. Boundary

No claim of recognition accuracy, model quality, telephony, race safety or
production readiness. T12 changed no code.
