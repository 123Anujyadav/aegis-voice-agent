# Conversation Evaluation Report

**Phase 10B** · `packages/go/conversation` · 2026-08-09

An assessment of the engine as a **conversational** artefact rather than a
software one: does it manage a dialogue the way a competent human receptionist
would, and where does it not?

---

## 1 · What can and cannot be evaluated at this phase

This is the first thing a reader needs, because an evaluation report that
overstates its reach is worse than none.

| Can be evaluated now | Cannot — and why |
|---|---|
| Floor management: turn-taking, backchannels, barge-in | **Answer quality** — there are no prompts and no model |
| Repair behaviour: how the engine handles being wrong | **Intent accuracy** — the classifier is framework-only by design |
| Boundedness: does every failure path terminate | **Naturalness / prosody** — belongs to the voice tier |
| Safety behaviour: emergency, fraud narrowing, escalation | **Caller satisfaction** — requires real calls and human raters |
| Determinism and explicability | **Task completion rate** — requires a deployment with real tasks |

**Everything below is structural evaluation against simulated dialogue.** No
human has spoken to this engine. Section 7 specifies what must happen before any
claim about real conversation quality is made.

---

## 2 · Evaluation dimensions

Conversation quality for a *control plane* is not "did it say the right thing".
It is five properties, each independently observable:

| # | Dimension | The failure it prevents |
|---|---|---|
| **D1** | Floor discipline | Talking over the caller; stopping every time they say "mm-hm" |
| **D2** | Repair | The clarification loop; acting confidently on a misunderstanding |
| **D3** | Boundedness | The call that never ends; the question asked four times |
| **D4** | Safety posture | Continuing to converse when it should have handed over |
| **D5** | Explicability | "Why did it do that?" being unanswerable after the fact |

---

## 3 · D1 · Floor discipline

### Backchannel discrimination

The engine distinguishes three kinds of simultaneous speech, which most voice
systems collapse into one:

| Overlap | Classification | Floor |
|---|---|---|
| < 250 ms | Grace — not yet evidence | Unchanged |
| 250–600 ms | **Backchannel** ("mm-hm", "right", "haan") | **Agent keeps it** |
| > 600 ms | **Barge-in** | **Caller takes it** |

**Why this matters conversationally.** A system that yields on any overlap stops
mid-sentence every time the caller agrees with it, which reads as an agent that
cannot hold a thought. A system that never yields talks over people. The
600 ms boundary is drawn from conversation analysis, where backchannels
cluster well under it.

*Evidence:* `TestTurnManager_BackchannelDoesNotStealFloor`,
`TestSim_BackchannelDoesNotDerailTheAgent`, `TestSim_BargeInYieldsTheFloor`.

### The caller always wins a genuine contest

Arbitration has exactly one asymmetry: on sustained overlap the caller is
granted the floor and the agent is not. There is no configuration that reverses
it. An agent competing with a human for the floor on a phone call is
intolerable, and no product requirement outweighs that.

### The one deliberate exception

The opening announcement does not yield — it is the caller's lawful basis under
frozen invariant I1. **The mitigation is what makes this tolerable
conversationally:** a barge-in during the greeting is **queued and applied the
instant it ends**, so a caller who starts speaking over the announcement is
heard immediately afterwards without repeating themselves.

Discarding that intent — the obvious implementation — would make the system feel
deaf at the very first moment of the call, which is the worst possible moment.

*Evidence:* `TestTurnManager_GreetingIsNonYieldingButQueues`.

**Assessment: strong.** This is the dimension the engine handles best.

---

## 4 · D2 · Repair

Repair is what separates a usable voice product from a demo. The engine models
**six distinct repair situations** where most systems model one ("I didn't catch
that"):

| Situation | Diagnosis | Question shape | Expectation set |
|---|---|---|---|
| Unusable audio | `Noise` | "Could you repeat that" | Unconstrained |
| Cut off mid-thought | `Incomplete` | Repeat | Unconstrained |
| Conflicts with what we know | `Contradiction` | "Did you mean X instead?" | **Yes/no** |
| Two readings compete | `Ambiguous` | "A or B?" | **Disambiguation** |
| Understood weakly | `LowConfidence` | "Did you mean X?" | **Yes/no** |
| Understood but incomplete | `MissingSlot` | "What date?" | **Slot value** |

### Diagnosis order encodes clinical reasoning

Noise before truncation before contradiction before ambiguity. Each rung asks a
question the previous rung must have already answered: *was it speech at all →
was it complete → does it conflict → is it ambiguous → is it confident → is it
complete in its parameters.*

Getting this order wrong produces the characteristic bad behaviour of voice
bots: asking a clarifying question about a topic the caller never raised,
because unusable audio was treated as a low-confidence intent.

*Evidence:* `TestClarification_AssessOrdersDiagnosisCorrectly`.

### The expectation is carried forward

Each repair sets an expectation that changes how the **next** utterance is read.
"Yes" following a confirmation is an answer; "yes" while merely listening is an
utterance to classify. The engine deliberately bypasses the classifier for
confirmations — asserted by a test that fails if the model is consulted at all.

**Conversationally this is the highest-value decision in the engine.** Getting
"no" wrong on a confirmation means doing something the caller explicitly
refused.

### Ambiguity is caught even when confidence is high

An intent at 0.90 with a runner-up at 0.85 is ambiguous. A confidence threshold
alone cannot see this, and it is the most common source of confident wrong
answers in deployed systems.

*Evidence:* `TestIntent_AmbiguityDetectedDespiteHighConfidence`.

### A defect found here, and what it revealed

The planner originally discarded clarification whenever the intent classified
confidently. **A contradiction is precisely the confident case** — the caller
said "actually make it Tuesday" and was understood perfectly. The engine would
have acted on the change without confirming it.

The clarification engine detected the contradiction correctly; the planner threw
the detection away. Only an end-to-end test could see that, and the lesson
generalises: **a subsystem being right is not enough if its consumer filters the
result away.**

*Evidence:* `TestFailure_ContradictionTriggersConfirmation`.

**Assessment: strong, with one caveat.** The repair *taxonomy* is unusually rich.
Whether the repair *questions* read naturally is unknowable until prompts exist.

---

## 5 · D3 · Boundedness

Every failure path in the engine terminates, and every bound escalates rather
than retrying.

| Bound | Default | Exit |
|---|---|---|
| Clarifications, total | 2–4 per persona | Escalate |
| Clarifications, same subject | 2 | Escalate |
| Repeats on noise | 2 | Escalate |
| Turns | 3–40 per persona | Escalate, else End |
| Duration | 30 s – 5 min per persona | Escalate, else End |
| Interruptions | 6 | Escalate |
| Plan/policy re-plans | 3 | Escalate |

**A permanently ambiguous caller terminates in four exchanges**, not never
(`TestSim_ClarificationLadderThenEscalation`). **An unusable line terminates**
rather than being endured (`TestSim_NoiseIsIgnoredThenEscalated`). **A
500-exchange stress conversation terminates** at its persona boundary
(`TestStress_LongConversationBounded`).

The state machine is proved by test to have no state from which a terminal state
is unreachable — a conversation that cannot end is, on a phone line, a call that
never hangs up.

### The design question this raises

**Escalation is the universal exit, and that is a strong assumption.** It is
correct when a human is available. In a deployment with no human fallback —
after hours, an unstaffed line — escalating may be a worse caller experience than
one more attempt or a graceful message. The engine currently has no way to
express "escalation is unavailable here", and `boundaryExit` falls back to
`End` only when the persona lacks `CapEscalate`.

Raised as an open question in [ENGINEERING_AUDIT §8](ENGINEERING_AUDIT.md).

**Assessment: strong, with a deployment-shaped caveat.**

---

## 6 · D4 · Safety posture

### Emergency

Five independent mechanisms enforce U7, so no single failure re-enables
conversation: a monotonic flag, a one-way persona switch, the planner's first
rung, a non-overridable safety policy rule, and floor preemption that outranks
even the non-yielding greeting.

A conversation reaching emergency **accepts nothing further** — subsequent
events return `ErrTerminal`.

*Evidence:* `TestSim_EmergencyEndsTheConversationImmediately` and four others.

### Fraud narrowing is a ratchet

Any persona may narrow to Fraud Shield; **Fraud Shield never broadens.** A caller
who talks their way out of fraud screening is exactly the attack, and the
narrowing is one-way and locked.

Fraud Shield forbids answering, message-taking, callback collection, transfer
and every disclosure. Its purpose is to give a hostile caller **no surface**, and
the capability set says so rather than trusting the layer above.

*Evidence:* `TestPersona_FraudShieldNeverBroadens`,
`TestPersona_FraudShieldDisclosesNothing`, `TestSim_FraudShieldRefusesToDisclose`.

### Emergency persona never clarifies

`EscalateOnUncertainty` makes the emergency persona escalate rather than ask a
clarifying question. Asking "could you repeat that?" during an emergency spends
the only resource that matters.

*Evidence:* `TestSim_EmergencyPersonaNeverClarifies`.

**Assessment: strong.** The safety behaviours are the most redundantly enforced
in the module, which is proportionate to their consequence.

---

## 7 · D5 · Explicability

Every state change records **why**: a `From`, a `To`, a `Trigger` and a
machine-readable note. Every plan carries a `Reason` code. Every policy denial
names the deciding rule and its class.

A conversation can be reconstructed after the fact from its trace alone:

```
greeting[start] -> listening[greeting_complete] -> thinking[utterance]
  -> speaking[planned] -> confirmation[speech_complete]
  -> thinking[utterance] -> speaking[planned] -> listening[speech_complete]
```

**Determinism makes this reconstruction trustworthy.** The same event sequence
and clock produce the same trace, verified across 16 runs comparing every state
and action (`TestSim_DeterministicReplay`). An incident can therefore be
replayed rather than theorised about — which for a conversational system, where
"it said something strange once" is the usual bug report, is the difference
between a diagnosis and a guess.

**Assessment: strong.**

---

## 8 · Scenario results

Every scenario below is a passing simulation test.

| # | Scenario | Expected behaviour | Result |
|---|---|---|---|
| 1 | Straightforward question | Answer, return floor | ✅ |
| 2 | Utterance before greeting | Not treated as a turn | ✅ |
| 3 | Backchannel during agent speech | Agent keeps floor | ✅ |
| 4 | Sustained barge-in | Caller takes floor, utterance abandoned | ✅ |
| 5 | Permanently ambiguous request | Bounded clarification, then escalate | ✅ |
| 6 | Confirmation answered "yes" | Read as an answer, classifier not called | ✅ |
| 7 | Confirmation answered in Hindi ("haan") | Read as an answer | ✅ |
| 8 | Contradicting a prior statement | Confirmed, not assumed | ✅ |
| 9 | Unusable audio, repeated | Ignore, then escalate | ✅ |
| 10 | Emergency mid-call | Immediate escalation, persona locked, terminal | ✅ |
| 11 | Hostile caller asking for identity | Refused by capability | ✅ |
| 12 | Classifier unavailable | Fallback response, conversation survives | ✅ |
| 13 | Model stream dies mid-utterance | Recoverable error, not terminal | ✅ |
| 14 | Recovery with a snapshot | Resume listening, context restored | ✅ |
| 15 | Recovery with no snapshot | Escalate rather than continue | ✅ |
| 16 | Caller hangs up at any point | Clean termination from every state | ✅ |
| 17 | Turn limit reached | Terminate at boundary | ✅ |
| 18 | 200 concurrent conversations | All complete, no leaked gauge | ✅ |

---

## 9 · Known conversational weaknesses

Stated because an evaluation that finds nothing wrong has not looked.

| # | Weakness | Impact | Mitigation path |
|---|---|---|---|
| **W1** | **Escalation is the only universal exit.** With no human available it may be worse than one more attempt | Medium | Add a persona flag for "no escalation target"; fall back to a graceful close |
| **W2** | **No topic tracking.** The engine has no notion of a conversation *topic* separate from the active intent, so a caller who returns to an earlier subject is a new classification | Medium | Belongs above this layer; the context engine has the storage for it |
| **W3** | **No repair of the agent's own errors.** A caller saying "no, that's wrong" after the agent answered is a new utterance, not a correction of the prior turn | Medium | Needs a correction intent and a turn-rollback concept |
| **W4** | **Backchannel thresholds are uncalibrated for Indic speech.** 250/600 ms comes from conversation-analysis literature that is predominantly English | Medium | Measure against real Hindi/Tamil call audio before launch |
| **W5** | **The yes/no vocabulary is small and transliteration-dependent.** "haan" is covered; Devanagari script "हाँ" is not | Low–Medium | Extend once the ASR's output script is known |
| **W6** | **Silence is under-modelled.** One threshold, no distinction between thinking-silence and abandonment-silence | Low | Add a second, longer threshold before treating silence as departure |
| **W7** | **No prosodic or paralinguistic input.** Anger, distress and urgency are invisible to the engine except through the emergency classifier | Low here | Voice tier; would arrive as additional signals |

**W4 is the one to act on first.** It is measurable now with recorded call audio
and it directly affects the dimension the engine is otherwise strongest at.

---

## 10 · Proposed evaluation methodology for the next phase

Nothing in this report substitutes for these.

| # | Method | Measures | Gate |
|---|---|---|---|
| **E1** | Replay recorded calls through the engine with a real classifier | Intent accuracy, clarification rate | Before first production call |
| **E2** | Human raters score transcript pairs (engine vs. human receptionist) | Naturalness, appropriateness | Before launch |
| **E3** | Backchannel threshold sweep against labelled Indic call audio | W4 calibration | Before launch |
| **E4** | Adversarial suite: callers attempting capability escalation via speech | Capability confinement under real injection | **Launch blocker** |
| **E5** | Emergency false-positive/negative measurement | U7 correctness | **Launch blocker** — already a blocker in `UX_FREEZE §9` |
| **E6** | Task completion rate per persona in a shadow deployment | D3 boundedness in the field | Before general availability |
| **E7** | Escalation rate by cause | Whether W1 is hurting real callers | Continuous |

**E7 is the metric that will tell us most.** The engine escalates on
clarification exhaustion, noise, boundaries, interruption storms and policy
exhaustion. If one cause dominates in production, that is where the
conversational design is actually failing — and the engine already emits the
counters to see it (`conv_clarification_exhausted_total`,
`conv_policy_denials_total`, `conv_interruptions_total`).

---

## 11 · Conclusion

As a **conversation control plane**, the engine is strong on floor discipline,
repair taxonomy, boundedness, safety posture and explicability. Those are the
properties that separate a usable voice product from a demo, and each is
enforced structurally rather than by convention.

As a **conversation**, it is unevaluated. There are no prompts, no model, no real
calls and no human raters, and no claim about how it *sounds* can be made from
this phase. The seven weaknesses in §9 are the honest starting list, and W4 —
backchannel thresholds calibrated on English against a market that speaks Hindi
and Tamil — is the one most likely to be felt by a real caller on day one.

**Recommendation: approve as a control plane; evaluate as a conversation before
launch.** E4 and E5 are launch blockers regardless of what this report says.
