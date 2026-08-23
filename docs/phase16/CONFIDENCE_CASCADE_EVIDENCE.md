# Phase 16 T13 — The Confidence Escalation Cascade, Pinned

> **SUPERSEDED BY T14. The behaviour recorded here no longer exists.**
>
> This document describes the defect as it was **measured** before the approved
> amendment, and is kept because the T14 decision is only intelligible against
> it. Nothing here is a description of current behaviour. For what the system
> does now, see
> [CONFIDENCE_POLICY_IMPLEMENTATION.md](CONFIDENCE_POLICY_IMPLEMENTATION.md).

**Status when written: EVIDENCE COMPLETE, no production change.** T13's whole
job was to turn the T12 source-derived cascade into executable evidence so the
decision could be taken safely.

---

## 1. Why a pin was required first

[T12](CONFIDENCE_DECISION_AUDIT.md) derived the cascade from source but never
executed it. A defect argued from reading is not a defect demonstrated, and the
product decision that followed was expensive enough to deserve evidence rather
than inference. No implementation was permitted until it ran.

## 2. The cascade, as measured

Three caller turns, a perfectly recognised transcript, and the confidence a real
recogniser reports — driven through the real `voice.Pipeline`, the real
`IntentEngine`, the real `ClarificationEngine` and the real planner:

```
verdicts = [noise noise noise]
plans    = [noise_repeat noise_repeat noise_unrecoverable]
final    = escalated / escalated   (TERMINAL)
```

Stage by stage:

```
turn1 {generated:true  chunks:1 frames:2 outcome:completed/ok}
turn2 {generated:true  chunks:2 frames:4 outcome:completed/ok}
turn3 {generated:false chunks:2 frames:4 outcome:completed/ok}

conversation = escalated / escalated  (terminal)
session      = listening              (NOT terminal)
abandonments = 0   interruptions = 0
```

The mechanism: `IntentNoise` → `Request{Kind: ClarifyNoise}`; `RepeatOnNoise`
defaults to **2** (`clarification.go:109`), so the third consecutive noise
verdict makes `Reserve` return `false`, and the planner returns
`ActionEscalate` / `noise_unrecoverable` / `StateEscalated`, which is terminal.

Planner rungs 1–3 were verified **not** to preempt this: the default persona is
`PersonaBusinessReceptionist`, which sets `MaxTurns` 40 and leaves
`EscalateOnUncertainty` false. Only `PersonaEmergencyAssistant` sets it, and it
is not the default. Had it been, the call would have escalated on turn one for
an unrelated reason and the pin would have been evidence for the wrong
mechanism.

## 3. Three findings that mattered

1. **The failure was invisible to operators.** Every pipeline turn reported
   `completed/ok`, with zero abandonments and zero faults. A dashboard counting
   turn outcomes saw three clean completions while the caller was escalated away
   mid-call.
2. **The first two turns were not free.** Each spent a real generation and a
   real synthesis telling the caller they could not be heard — on audio the
   system had transcribed perfectly.
3. **Session and conversation disagreed.** The transport stayed open and would
   have carried more audio; the frozen conversation had terminated and refused
   it.

## 4. Non-vacuity

A control test used the identical rig, transcript and path, differing **only**
in that the recogniser reported a confidence — and resolved to
`request_transfer`. The words were never the problem.

## 5. Mutations — 5 attempted, 4 CAUGHT, 1 INERT

| | Mutation | Result |
|---|---|---|
| M1 | gate removed | CAUGHT |
| M2 | pipeline substitutes a number | CAUGHT |
| M3 | transcript replaced | CAUGHT |
| M4 | escalation branch replaced with `ActionIgnore` | CAUGHT |
| M5 | sentinel special-cased as accepted | CAUGHT — surgically, one subtest |
| M8 | transcript withheld from the conversation | **INERT — not credited** |

M8 was inert *because the gate already suppressed the classifier* — noise either
way. It became catchable once T14 resolved the blocker.

## 6. What became of these tests

They were **converted, not deleted**, by T14, each naming its predecessor:

| T13 test | Became |
|---|---|
| `TestT13_UnknownConfidenceEscalatesTheCallOnTheThirdTurn` | `TestT14_UnknownConfidenceNeverEntersTheNoiseCascade` |
| `TestT13_TheCascadeLifecycleIsPinnedStageByStage` | `TestT14_UnknownConfidenceCompletesTheNormalResponseLifecycle` |
| `TestT13_TheGateIsWhatRejectsIt` | `TestT14_TheConfidenceMatrix` |
| `TestT13_TheSameWordsResolveWhenAConfidenceIsPresent` | retained under its own name |

## 7. Task status

| Category | T13 |
|---|---|
| **Implemented and verified** | Executable evidence only — four tests pinning the then-current cascade through the real pipeline, gate, clarification engine and planner. **No production code changed.** |
| **Measured but not implemented** | The fix. T13 was forbidden to implement it; the decision was taken separately and implemented by [T14](CONFIDENCE_POLICY_IMPLEMENTATION.md). |
| **Known limitations** | The tests pinned a **defect**, so they were designed to fail once the policy changed — and did. They were converted, not deleted (§6). Mutation M8 was inert while the gate suppressed the classifier. |
| **Mutation results** | 5 attempted — **4 CAUGHT, 1 INERT** (M8, honestly not credited). See §5. |
| **Deterministic test status** | `-count=10 -shuffle=on` green. No sleeps, no polling, no timing luck — synchronisation via the existing bounded `awaitTurn` channel handshake. |
| **Race status** | `-race NOT RUN LOCALLY — no race-safety claim; CI authoritative` (`CGO_ENABLED=0`, gcc absent). |
| **Frozen-module integrity** | All frozen modules unchanged. `conversation` carried only the Phase 15 T4 amendment at the time. Mutated files were restored and verified byte-identical by md5. |
| **Security / dependency** | No transcript or PCM in logs or metrics, no credential, no network, no model download, no unbounded queue, no package-level mutable state, no new dependency. |
| **Resume point at the time** | Product decision required. Answered, then implemented by T14. |

## 8. Boundary

No claim of recognition accuracy, model quality, telephony, race safety or
production readiness. T13 changed no production code and decided no policy.
