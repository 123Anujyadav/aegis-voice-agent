# Phase 16 T5 — Greeting Speech Contract & Plan-Input Seam

**Status:** DESIGN AUDIT. **No production code changed** (`pipeline.go` and
`bargein.go` digests identical to their T1/T3 states). Four contract tests added
to pin the frozen behaviours the design rests on. **No stop condition
triggered** — the design is expressible entirely within existing contracts.

---

## 1. The existing speaking path

```
speech.TranscriptSegment                         pipeline.go:863  (only entry)
  → runTurn(turn, final)                         :953  UNEXPORTED
      → Planner.Handle(EventUtterance)           :942
      → Plan                                      ← the Plan exists HERE
      → planSpeaks(plan.Action)                  :966  (Phase 16 T1)
      → Governor.Decide(governance.Request)      :976
      → Generator.Generate(rt.GenerateSpec{
            SessionID, Tier, System,
            Messages: [{RoleUser, final.Text}],  ← THE ONLY transcript-derived field
            MaxOutputTokens})                    :1079
      → synthesisSink.Write → speech.Chunker → TTSStream.Synthesize
      → media.Frame → Output.Deliver             :1336
      → FSM.To(StateListening)
      → Planner.Handle(EventSpeechComplete)      :1209
      → Observer.OnTurnComplete
```

**Everything after the plan is action-agnostic and transcript-free except one
field: `Messages`.** That is the entire coupling between the speaking path and
the transcript.

## 2. Plan-to-speech seam options

| | Option | Verdict |
|---|---|---|
| A | **exported `Pipeline` method** | **RECOMMENDED.** The pipeline already owns generation, synthesis, delivery and three of the four production lifecycle events. Session identity is structural — one pipeline, one conversation. |
| B | existing Session method | `voice.SessionFSM` is a passive state holder with no `Planner`, no registry and no generation. Unsuitable. |
| C | planner callback | Would invert the dependency: `conversation` is frozen and must not call back into `voice`. |
| D | export the internal helper | `runTurn` takes a `TranscriptSegment` and performs classification. Exporting it would export the wrong contract. |
| E | service-level orchestration | Would need its own governance → generation → chunker → TTS → delivery sequence: **a second response engine**. Rejected. |

**Recommendation: A.**

## 3. Generation input — no contract change required

| Question | Answer |
|---|---|
| mandatory fields | `SessionID`, `Tier` (`kernel.go:533`, `:559`) |
| transcript-derived | **only `Messages`** |
| optional | `RequestID`, `System`, `MaxOutputTokens`, `Temperature`, `Thinking` |
| can it represent a system-initiated response? | **yes** — `rt.RoleSystem` and `rt.RoleAssistant` already exist (`provider.go:89-96`) |
| can it represent a greeting with no caller input? | **yes** — and this is *defined behaviour*, not a loophole |
| does the generator contract support it? | **yes** |
| is a new field required? | **no** |

**Decisive evidence (`kernel.go:573-579`):**

```go
messages := spec.Messages
if len(messages) == 0 {
    if messages, err = session.Context().Assemble(0); err != nil { ... }
}
```

An empty `Messages` is not an error — the Kernel assembles from the session's
own context. A greeting can therefore be generated with `Messages: nil` plus a
`System` instruction, or with an explicit `RoleSystem` message.

**Stop condition 1 does NOT fire.**

## 4. Greeting content — the one genuine gap

Checked every candidate source:

| Source | Carries greeting content? |
|---|---|
| `Plan.Reason` | `"greeting"` — a **marker**, not content |
| `Plan.Intent` / `Clarification` / `Expectation` | empty for the greeting |
| `Persona` | **no** — capabilities, budgets, limits, latency profile. Behavioural policy, not content |
| `PipelineConfig.System` | a session-wide system instruction — exists, but is not greeting-specific |
| policy / templates | none exist |

> **No component in the repository holds greeting text.** The greeting must be
> *generated*, and the only existing lever for instructing the generator is
> `GenerateSpec.System`.

This is a configuration decision, not a contract gap: the deployment must supply
a system instruction that produces an opening greeting. **No greeting text
should be hard-coded**, and none is proposed here.

## 5. Completion semantics — measured, both are legal

| Event | From `StateGreeting` | Resulting state | Resulting floor | Trigger recorded |
|---|---|---|---|---|
| `EventSpeechComplete` | **accepted** | `listening` | **caller** | `speech_complete` |
| `EventGreetingComplete` | **accepted** | `listening` | **none** | `greeting_complete` |

`greeting → listening` is a declared transition (`state.go:232`), and
`handleSpeechComplete` reaches it because the greeting plan's `Expectation` is
the zero value, so `stateForExpectation` yields `StateListening`.

**Both leave a usable conversation** — the first caller utterance classifies
either way (measured).

**But `EventGreetingComplete` is also accepted on an ordinary turn**, leaving the
floor unheld and recording a greeting trigger for a turn that was not a greeting.
Using it globally would "work" while corrupting the trace and the floor. So the
completion event must be chosen **per plan**, not adopted wholesale.

### The discriminator already exists, and it is typed

| Plan | `NextState` |
|---|---|
| greeting (`handleStart`) | **`StateGreeting`** |
| ordinary response | **`StateSpeaking`** |

`Plan.NextState` is a typed field of the frozen `Plan`. **No string comparison on
`Reason`, and nothing invented.**

## 6. Completion ownership

| Candidate | Knows audio finished | Knows ConversationID | Owns conversation semantics | Verdict |
|---|---|---|---|---|
| **Pipeline** | **yes** | **yes** (structural) | already emits 3 of 4 lifecycle events | **RECOMMENDED** |
| Call Driver | no — it does not observe delivery | yes | yes | would need a completion signal back from the pipeline, and the pipeline would have to suppress its own; two owners of one boundary |
| Conversation Engine | no | yes | yes | frozen; cannot see audio |
| FrameSink | yes | no | no | a delivery port, not a lifecycle owner |
| SessionFSM | no | no | no | passive |

**A constraint that decides it:** `voice.Planner` is `Handle(Event) (Plan, error)`
only — it exposes **no state accessor**. The pipeline therefore *cannot* inspect
the conversation to decide which event to send. It can only know from the plan it
was given. That makes the plan-carrying seam and the completion owner the same
component: **the Pipeline**.

## 7. Successful greeting lifecycle (design, not implemented)

| # | Step | Producer | Consumer | State | Floor | On failure |
|---|---|---|---|---|---|---|
| 1 | open conversation | Call Driver | `Bridge.Planner` | `idle` | none | construction error; no call |
| 2 | `EventStart` | Call Driver | conversation | `greeting` | agent | rejected if already open (harmless) |
| 3 | submit greeting plan | Call Driver → Pipeline seam | pipeline | `greeting` | agent | typed error, no audio |
| 4 | governance | Pipeline | `governance.Engine` | `greeting` | agent | denied → `EventResponseAbandoned` |
| 5 | generation | Pipeline | `rt.Kernel` | `greeting` | agent | failed → `EventResponseAbandoned` |
| 6 | chunk + synthesis | Pipeline | `speech` chunker/TTS | `greeting` | agent | failed → `EventResponseAbandoned` |
| 7 | delivery | Pipeline | `FrameSink` | `greeting` | agent | failed → `EventResponseAbandoned` |
| 8 | **`EventGreetingComplete`** | **Pipeline** (because `plan.NextState == StateGreeting`) | conversation | `listening` | none | — |
| 9 | first caller utterance | existing transcript path | conversation | `speaking` | agent | existing semantics |

## 8. Greeting failure — approved semantics already apply

**Measured:** `EventResponseAbandoned` during `StateGreeting` yields
`wait`/`"response_abandoned"`, `greeting → listening`, **floor = caller**, and
the caller is then heard normally.

The Phase 15 test — *"was there an accepted response that was supposed to be
spoken?"* — is satisfied: the greeting is an accepted `ActionRespond` plan.
**No semantic change is required**, and none is proposed.

## 9. Greeting interruption — existing `EventInterrupt` is sufficient

**Measured:** `EventInterrupt{InterruptionUser}` during `StateGreeting` yields
`ignore`/`"interrupted_user"`, `greeting → listening`, floor = caller, **one**
history entry, and the caller is heard next.

No `EventGreetingInterrupted`, no new state, no new interruption kind.

**One caveat to carry forward:** INV-CV-2 makes the opening turn **non-yielding**
because the greeting carries the caller's lawful-basis announcement. The frozen
turn manager *queues* a barge-in during it rather than discarding it. Whether a
caller may lawfully cut off that announcement is a **compliance question, not a
technical one**, and it is not answered here.

## 10. Cancellation

The existing guards already produce the right behaviour and need no change: the
completion tail returns early on `p.ctx.Err() != nil`, and `abandonResponse`
carries the same guard. Measured today: cancelling a conversation that never
spoke yields **0 completions, 0 abandonments, 0 interruptions**. A cancelled
greeting must therefore emit **neither** `EventGreetingComplete` **nor**
`EventResponseAbandoned`.

## 11–12. Multi-turn and isolation

The greeting occupies turn 0 and completes before any caller utterance, so turn
count, `last_intent`, context, expectation and interruption history are all
untouched by it. Isolation is structural — one pipeline, one conversation, no
registry, no shared plan — exactly as Phase 16 T3 established for interruption
forwarding. **No registry is required or proposed.**

## 13–14. Module impact

**Frozen: NONE.** No change to conversation, speech, runtime, governance, memory,
toolruntime, audiointel, audiobridge, media, metrics, evaluation, evalsubjects or
telephony. The Phase 15 `EventResponseAbandoned` amendment is unchanged and not
extended.

**`packages/go/voice`** — the seam and the completion branch.
**`services/go/voice/cmd/server`** — the future call driver.
Dependency direction is preserved: the service depends on voice; voice depends
on the frozen modules; nothing depends on the service.

## 15. Proposed API surface (NOT implemented)

```go
// Speak submits a plan the conversation has already decided on into the
// existing speaking path.
func (p *Pipeline) Speak(ctx context.Context, plan conversation.Plan) error
```

| Property | Design |
|---|---|
| input | `conversation.Plan` — already produced by the caller's `Handle` |
| returns | `error` — typed, matching the existing `ErrProviderUnavailable` / `ErrProviderFailed` vocabulary |
| blocking | blocks until delivery completes or fails, like `runTurn` |
| concurrency | **one turn at a time per pipeline**, as today; concurrent calls must be refused, not queued |
| requires | a started, non-terminal session |
| failure reporting | the existing `p.note` + `EventResponseAbandoned` path |
| completion | `EventGreetingComplete` when `plan.NextState == StateGreeting`, otherwise `EventSpeechComplete` |
| refuses | plans where `!planSpeaks(plan.Action)` |

## 16. Future test plan

**voice** (5): plan reaches the speaking path · generation receives a
system-instructed request with no transcript · completion event chosen by
`NextState` · `Speak` refuses a non-speaking plan · `Speak` refuses a terminal
session.

**service** (9): greeting audio delivered · `EventGreetingComplete` exactly once ·
caller floor after greeting · first utterance accepted · greeting generation
failure → abandonment · greeting TTS failure → abandonment · greeting
interruption → one interruption, no completion · greeting cancellation → neither
event · two-session isolation.

**conversation**: none. The contract is already proven and must not change.

## 17. Future mutation plan

| | Mutation | Caught by |
|---|---|---|
| M1 | remove the plan → speaking seam | greeting audio + completion tests |
| M2 | remove the greeting generation input | generation-request assertion |
| M3 | emit `EventSpeechComplete` instead of `EventGreetingComplete` | trigger-identity assertion (both reach `listening`, so **the trace trigger and the floor are the only discriminators** — asserting state alone would NOT catch this) |
| M4 | emit `EventGreetingComplete` before delivery finishes | delivery-ordering assertion via the sink handshake |
| M5 | emit `EventGreetingComplete` twice | exactly-once count over `Trace()` |

M3 is the subtle one and the reason the completion test must assert the
**trigger and the floor**, not merely the resulting state.

## 18. Security

Nothing proposed adds transcript logging, PCM logging, credentials, persistence,
unbounded queues, package-level mutable state or sensitive labels. The greeting
carries no caller content by construction — there is no caller input yet.
