# Phase 13 — Limitations

Nothing here is a footnote. These are the honest boundaries of what Phase 13 is
and what was actually verified.

## 1. This is not model-based NLU

`intent.Classifier` is a **deterministic rule and lexicon matcher** over a closed
11-name vocabulary. It does not understand language. It matches cues and divides
evidence by a saturation constant.

## 2. No natural-language accuracy claim

No accuracy percentage is published anywhere in Phase 13. Fixture results are
**fixture pass rates** — "known input still yields known typed outcome" — and
must never be reported as NLU accuracy or model accuracy.

## 3. No real-world corpus evaluation

Fixtures are hand-written and hand-verified. There is no held-out set, no real
caller data, no accented / code-switched / noisy-ASR / adversarial corpus, no
human evaluation and no inter-annotator agreement. Anything outside the closed
vocabulary resolves to fallback **by construction** — a design property, not a
measured recognition rate.

## 4. No model or provider inference

Phase 13 performs none. There is no provider abstraction, no model download, no
network call, no credential. A model-backed classifier may later implement the
same `conversation.IntentClassifier` port, but **no such implementation exists in
this phase**. "No API key required" does not mean a model capability exists — it
means there is no model.

## 5. `-race` not available locally

```
cgo: C compiler "gcc" not found: exec: "gcc": executable file not found in %PATH%
go: -race requires cgo; enable cgo by setting CGO_ENABLED=1
```
No C compiler is installed; GCC/Docker were not installed to manufacture
evidence. **No race-safety claim is made for Phase 13.**

## 6. CI race verification pending

T14 added both modules to `hardening.yml`'s `AI_MODULES`, so CI *would* run
single and repeated shuffled `-race`. **CI has not executed and nothing has been
pushed.** Workflow configured is not CI executed. There is no CI run ID.

## 7. F2 remains OPEN and OUT OF SCOPE

Phase 12's `evalsubjects` `LatencyRatio: 2.0` is unchanged, untouched, and
remains the sole cause of the red release gate. No Phase 13 work targets it. When
hardening does run, `evalsubjects` is in `AI_MODULES`, so **the release gate may
still be red solely because of F2** — that is a pre-existing Phase 12 failure and
must not be attributed to Phase 13.

## 8. Trace export remains BLOCKED ON APPROVAL

Requires a new ADR and a third-party dependency. Not implemented, not designed
further in this phase.

## 9. Tied-timestamp context eviction is order-unspecified

Frozen `evictOldestLocked` compares with `SetAt.Before(oldest)`, false for equal
timestamps, so among entries sharing a `SetAt` the victim is decided by Go's
randomised map iteration. Reachable in production wherever writes land within one
clock tick. **Frozen observation, not patched, not a Phase 13 defect.** Phase 13
asserts only the bound, the eviction count and newest-survives in that case. See
[CONTEXT_MODEL.md](CONTEXT_MODEL.md).

## 10. `ScopeShared` does not share

Documented as *"visible across concurrent conversations for one subject"*, but
every `Conversation` builds its own `ContextEngine`, so a `ScopeShared` entry
stays per-conversation. **Frozen observation, verified and pinned by a test**,
because the name invites the opposite assumption.

## 11. The canonical slot sort is currently a no-op

Removing `sort.SliceStable(out, byName)` changes no output, because every slot
spec table is already declared name-ascending — proven by a control run that
passes with the sort deleted. It is retained as defence; the ordering *contract*
still has teeth. Latent redundancy, not a bug.

## 12. Pre-existing Phase 11E barge-in flake

`TestBargeIn_RepeatedInterruptionsUnderLoadDoNotEndTheCall` (frozen `voice`)
failed once under heavy CPU oversubscription:
`generation is 118 after 117 interruptions`. Reproducible only with several
concurrent full-package runs; passes in isolation. **Not caused by Phase 13**
(`voice` is byte-identical) and not fixed — it belongs to Phase 11E. Now that
hardening runs repeated shuffled race on `voice`, it may surface there.

## 13. 22 pre-existing gofmt deviations outside Phase 13

In `media`, `outbox`, `telemetry`, `repository`, `eventbus` and several
services. **None is a Phase 13 file** (verified: count 0). Left untouched; they
belong to the open Phase 12 gofumpt/lint policy item.

## 14. Not wired into a running service

Phase 12 T8 established that no service imports any AI-plane module.
`voiceintel` is a leaf imported by nothing. Phase 13 makes the classifier
**reachable and correct**, not **deployed**. No service wiring was performed.

## 15. `voiceintel` reaches `governance` transitively

Via `voice`; declared `// indirect` since T6 and present in the non-test build.
Pre-existing, not introduced by Phase 13, and never called by it. The protected
module `intent` has no governance dependency.

## 16. Coverage is a floor, not a quality measure

`intent` 94.5%, `voiceintel` 66.7% (floor 60%). Line coverage measures which
lines executed, not whether behaviour is correct.

## 17. Benchmarks are measurement, not optimization

No optimization was performed, so no speedup is claimed. Absolute figures are
from one developer machine; the CI runner is a noisy neighbour and its numbers
would differ.

## 18. Production readiness is NOT claimed

See [FINAL_REPORT.md](FINAL_REPORT.md) §16. No production traffic approval, no
load testing, no operational runbook, no rollback plan, no SLO.
