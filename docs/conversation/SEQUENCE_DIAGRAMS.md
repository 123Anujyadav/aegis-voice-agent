# Conversation Engine — Sequence Diagrams

**Phase 10B** · The decision cycle and the seven scenarios that define it.

Every diagram below corresponds to a passing test, named beneath it.

---

## 1 · The decision cycle

The shape every caller utterance takes. Its order is fixed and each step is
budgeted.

```mermaid
sequenceDiagram
    autonumber
    participant C as Caller
    participant CV as Conversation
    participant LC as LatencyController
    participant TM as TurnManager
    participant IE as IntentEngine
    participant CX as ContextEngine
    participant CL as ClarificationEngine
    participant PL as Planner
    participant PO as PolicyEngine

    C->>CV: Event{Utterance}
    CV->>LC: Begin()  (150 ms budget, persona-scaled)

    CV->>TM: LastExpectation()
    TM-->>CV: ExpectYesNo | ExpectSlotValue | none

    CV->>LC: Enter(StageIntent)
    alt budget available
        CV->>IE: Resolve(utterance, expectation)
        IE-->>CV: Intent + Verdict
    else degraded
        Note over CV,IE: stage SKIPPED — falls back to<br/>the fallback intent, honestly
    end

    CV->>LC: Enter(StageContext)
    CV->>CX: Set(last_intent, ScopeConversation)

    CV->>CL: Assess(utterance, intent, verdict, contradicts)
    CL-->>CV: Request{Kind, Slot?}
    CV->>CL: Reserve(request, personaBudget)
    CL-->>CV: Request + allowed?

    CV->>CV: transition(Thinking, utterance)

    loop up to 3 attempts
        CV->>LC: Enter(StagePlanning)
        CV->>PL: Plan(PlanInput)
        PL-->>CV: Plan{Action, Expectation, NextState}

        CV->>LC: Enter(StagePolicy)
        Note right of LC: NEVER SKIPPABLE — I11
        CV->>PO: Evaluate(action, persona, boundaries)
        alt allowed
            PO-->>CV: Allow
        else denied
            PO-->>CV: Deny{rule, class}
            Note over CV,PL: mark action denied, re-plan
        end
    end

    CV->>TM: Release(caller)
    CV->>CV: transition(Speaking, planned)
    CV->>TM: Acquire(agent)
    CV->>LC: End()
    CV-->>C: Plan (rendered by the layer above)
```

**Planning precedes policy deliberately.** Policy is a veto over a concrete
proposal; evaluating every possible action against policy before choosing would
be slower and far less explicable. The bounded re-plan loop turns a veto into a
different decision rather than a failure — and after three refusals the engine
escalates rather than looping, because looping spends the caller's time
producing nothing.

*Tests:* `TestSim_HappyPath`, `TestPlanner_DecisionTable`

---

## 2 · Opening — the announcement cannot be skipped

```mermaid
sequenceDiagram
    autonumber
    participant C as Caller
    participant CV as Conversation
    participant TM as TurnManager

    CV->>TM: Acquire(agent, nonYielding=TRUE)
    CV->>CV: transition(Idle → Greeting)
    Note right of CV: no Idle → Listening edge exists

    par announcement plays
        CV-->>C: announcement (deterministic, I1)
    and caller tries to speak
        C->>TM: Acquire(caller)
        TM-->>C: FloorQueued
        Note over TM: NOT denied — QUEUED.<br/>Discarding it would make<br/>the system feel deaf
    end

    CV->>TM: Release(agent)
    TM->>TM: apply queued request
    TM-->>CV: caller now holds the floor
    CV->>CV: transition(Greeting → Listening)
```

*Tests:* `TestTurnManager_GreetingIsNonYieldingButQueues`,
`TestSim_GreetingAlwaysPrecedesConversation`,
`TestStateMachine_GreetingCannotBeBypassed`

---

## 3 · Backchannel versus barge-in

The same physical event — the caller producing audio while the agent speaks —
resolving two different ways.

```mermaid
sequenceDiagram
    autonumber
    participant C as Caller
    participant CV as Conversation
    participant TM as TurnManager

    Note over CV: state = Speaking, agent holds floor

    C->>CV: Event{Overlap}  "mm-hm"
    CV->>TM: NoteOverlap(caller)
    TM->>TM: overlap = 0 ms < OverlapGrace (250 ms)
    TM-->>CV: FloorBackchannel
    CV-->>C: Plan{Ignore}
    Note over CV: agent KEEPS the floor —<br/>stopping every time the caller<br/>agrees would be intolerable

    C->>CV: Event{Overlap}  sustained speech
    CV->>TM: NoteOverlap(caller)
    TM->>TM: overlap > BackchannelMaxDuration (600 ms)
    TM-->>CV: FloorGranted
    CV->>CV: handleInterrupt(User)
    CV->>CV: transition(Speaking → Interrupted → Listening)
    Note over CV: resume policy = ABANDON.<br/>Replaying the rest of the sentence<br/>is the worst thing a voice system does
```

*Tests:* `TestTurnManager_BackchannelDoesNotStealFloor`,
`TestSim_BackchannelDoesNotDerailTheAgent`, `TestSim_BargeInYieldsTheFloor`

---

## 4 · Clarification ladder, ending in escalation

A caller whose request is permanently ambiguous. The budget is what stops this
becoming the classic voice-bot loop.

```mermaid
sequenceDiagram
    autonumber
    participant C as Caller
    participant CV as Conversation
    participant IE as IntentEngine
    participant CL as ClarificationEngine
    participant PL as Planner

    C->>CV: "the thing"
    CV->>IE: Resolve
    IE-->>CV: {option_a 0.60, option_b 0.58} → margin 0.02
    Note right of IE: AMBIGUOUS despite<br/>being above reject.<br/>A bare threshold misses this
    CV->>CL: Reserve(Ambiguous)
    CL-->>CV: round 1, allowed
    CV->>PL: Plan
    PL-->>CV: Clarify → ExpectDisambiguation
    CV-->>C: (clarifying question)

    C->>CV: "the thing"
    CV->>CL: Reserve(Ambiguous)
    CL-->>CV: round 2, FINAL

    C->>CV: "the thing"
    CV->>CL: Reserve(Ambiguous)
    CL-->>CV: NOT ALLOWED — budget spent
    CV->>PL: Plan(clarificationAllowed=false)
    PL-->>CV: Escalate{clarification_exhausted}
    CV->>CV: transition(→ Escalated)
    Note over CV,PL: exhaustion ESCALATES.<br/>It never asks again
```

*Tests:* `TestSim_ClarificationLadderThenEscalation`,
`TestClarification_BudgetExhaustionStopsAsking`

---

## 5 · Confirmation — why "yes" bypasses the classifier

```mermaid
sequenceDiagram
    autonumber
    participant C as Caller
    participant CV as Conversation
    participant TM as TurnManager
    participant IE as IntentEngine
    participant CLS as IntentClassifier

    Note over CV: agent asked a low-confidence confirmation
    CV->>TM: Release(agent, ExpectYesNo)
    CV->>CV: transition(Speaking → Confirmation)

    C->>CV: "yes"
    CV->>TM: LastExpectation()
    TM-->>CV: ExpectYesNo
    CV->>IE: Resolve(utterance, ExpectYesNo)

    IE->>IE: classifyYesNo() — fixed vocabulary,<br/>English + Hindi transliteration
    IE-->>CV: IntentAffirm, confidence 1.0

    Note over IE,CLS: THE CLASSIFIER IS NEVER CALLED.<br/>Running "yes" through general<br/>classification is how a confirmation<br/>gets read as a new request
```

The test asserts `Classifier.Calls()` is unchanged — it fails if the model is
consulted at all.

*Tests:* `TestIntent_YesNoShortCircuitsClassification`,
`TestSim_ConfirmationYesIsInterpretedAsAnswer`, `TestIntent_YesNoRecognisesHindi`

---

## 6 · Emergency — the product gets out of the way

```mermaid
sequenceDiagram
    autonumber
    participant C as Caller
    participant CV as Conversation
    participant IN as InterruptionEngine
    participant PR as PersonaRuntime
    participant TM as TurnManager
    participant PO as PolicyEngine

    C->>CV: Event{Interrupt, Emergency}
    CV->>IN: Raise(Emergency)
    IN->>IN: emergencyAt = now (MONOTONIC — never cleared)
    IN-->>CV: Interruption{Resume: Never}

    CV->>PR: Switch(EmergencyAssistant)
    Note right of PR: always permitted from any persona;<br/>one-way, and locks
    PR-->>CV: switched

    CV->>TM: ForceYield(system, Emergency)
    Note right of TM: preempts even a<br/>NON-YIELDING greeting.<br/>U7 outranks I1 here

    CV->>CV: transition(→ Interrupted → Escalated)
    CV-->>C: Plan{Escalate}

    C->>CV: any further event
    CV-->>C: ErrTerminal

    Note over PO: every subsequent policy check<br/>denies everything but<br/>Escalate / End / Ignore
```

*Tests:* `TestSim_EmergencyEndsTheConversationImmediately`,
`TestInterruption_EmergencyIsIrreversible`,
`TestTurnManager_EmergencyPreemptsNonYielding`,
`TestPolicy_SafetyDeniesEverythingButExitDuringEmergency`

---

## 7 · Provider failure and recovery

```mermaid
sequenceDiagram
    autonumber
    participant P as Provider
    participant CV as Conversation
    participant IN as InterruptionEngine
    participant CX as ContextEngine

    Note over CV: state = Speaking
    CV->>IN: Checkpoint{turn, offset}

    P--xCV: stream died
    CV->>IN: Raise(Provider)
    alt checkpoint older than 400 ms
        IN-->>CV: ResumeFromCheckpoint
        Note right of IN: the caller heard a legitimate<br/>partial answer; the rest is wanted
    else checkpoint very recent
        IN-->>CV: ResumeRestart
        Note right of IN: resuming from two words in<br/>is incoherent
    end

    CV->>CV: transition(→ Interrupted → Error)
    Note over CV: Error is NOT terminal — that is<br/>the difference between an error<br/>and a failure

    CV->>CV: Recover()
    CV->>CV: transition(Error → Recovery)
    CV->>CX: LatestSnapshot()
    alt snapshot exists
        CX-->>CV: snapshot
        CV->>CX: Restore(id)
        Note right of CX: Conversation + Session roll back.<br/>Business reference data does NOT —<br/>restoring stale opening hours is<br/>worse than the error
        CV->>CV: transition(Recovery → Listening)
    else no snapshot
        CX-->>CV: none
        CV->>CV: transition(Recovery → Escalated)
        Note right of CV: continuing on half-written<br/>context is worse than a handover
    end
```

*Tests:* `TestFailure_ProviderInterruptionEntersRecoverableError`,
`TestFailure_RecoveryWithSnapshotResumes`,
`TestFailure_RecoveryWithoutSnapshotEscalates`,
`TestInterruption_ProviderFailureEarlyRestarts`

---

## 8 · Latency degradation under pressure

```mermaid
sequenceDiagram
    autonumber
    participant CV as Conversation
    participant LC as LatencyController
    participant IE as IntentEngine
    participant PO as PolicyEngine

    CV->>LC: Begin()  total = 150 ms
    Note over LC: persona LatencyProfile scales this —<br/>emergency gets 0.5x

    CV->>LC: Enter(StageIntent)
    LC->>LC: elapsed > 75% of total?
    alt under threshold
        LC-->>CV: run = true
        CV->>IE: Resolve
    else over threshold
        LC-->>CV: run = FALSE, degraded = true
        Note over CV,IE: skipped — falls back to the<br/>fallback intent rather than<br/>attaching a confidence<br/>we never computed
    end

    CV->>LC: Enter(StagePolicy)
    LC->>LC: Stage.Skippable() == false
    LC-->>CV: run = TRUE, always
    CV->>PO: Evaluate
    Note over LC,PO: I11 — the runtime may shed at<br/>admission or downgrade a tier,<br/>but it may NEVER skip the<br/>safety layer

    CV->>LC: End()
```

*Tests:* `TestLatency_PolicyIsNeverSkippable`,
`TestLatency_DegradesSkippableStagesOnly`

---

## 9 · Conversation lifetime

```mermaid
sequenceDiagram
    autonumber
    participant App as Orchestration
    participant E as Engine
    participant CV as Conversation
    participant M as Metrics

    App->>E: Begin(id, persona)
    E->>E: build FSM, turns, intents, context,<br/>clarification, personas (SHARED registry)
    E->>M: Started++, Active++
    E-->>App: *Conversation

    loop each event
        App->>CV: Handle(Event)
        CV-->>App: Plan
    end

    App->>CV: Handle(Hangup)
    CV->>CV: transition(→ Ended)
    CV->>CV: finish(outcome)
    CV->>M: Completed[outcome]++, Duration, TurnsPerConv
    CV->>M: Active--
    CV->>E: active.Delete(id)

    Note over CV: finish() is idempotent —<br/>terminal accounting happens<br/>exactly once whatever path<br/>reached it
```

*Tests:* `TestMetrics_ConversationAccounting`,
`TestStress_ManyConcurrentConversations` (asserts the Active gauge returns to
zero across 200 concurrent conversations)
