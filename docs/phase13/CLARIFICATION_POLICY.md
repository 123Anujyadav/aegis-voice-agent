# Phase 13 — Clarification Policy

All behaviour here belongs to the **frozen** `conversation` package. Phase 13
adds no clarification policy and no UX rules. Every numeric claim cites a source
line or the test that measured it.

## Clarification kinds (frozen, `clarification.go:14`)

`ClarifyNone` · `ClarifyAmbiguous` · `ClarifyLowConfidence` ·
`ClarifyMissingSlot` · `ClarifyContradiction` · `ClarifyNoise` ·
`ClarifyIncomplete`

Phase 13 selects among them using frozen predicates only
(`Intent.Complete()`, `Intent.Margin()`, `IntentConfig.AmbiguityMargin`):

1. `!Complete()` yields `ClarifyMissingSlot` — the intent is understood, so
   asking about the gap beats asking which intent was meant.
2. more than one alternative and `Margin() < AmbiguityMargin` yields
   `ClarifyAmbiguous`.
3. otherwise `ClarifyLowConfidence`.

## Clarification budget — MEASURED

Source: `conversation/persona.go:137` — the default persona
(`PersonaBusinessReceptionist`, set at `engine.go:124`) has
**`ClarificationBudget: 3`**.

MEASURED behaviour (T10 diagnostic, reproduced in T12): repeating an utterance
whose intent has an **unfilled required slot** produces

```
turn 0  action=ask       reason="clarify_missing_slot"    state=question
turn 1  action=ask       reason="clarify_missing_slot"    state=question
turn 2  action=escalate  reason="clarification_exhausted" state=escalated (terminal)
```

The agent asks twice and **escalates on the third attempt**. Trace record:
`thinking->escalated trigger=planned note=clarification_exhausted`.

This is the budget working as designed. It is exercised deliberately by
`TestT10_ClarificationBudgetEscalationIsPerSession`, which also proves the
escalation is confined to the session that caused it and does not disturb
concurrent healthy sessions.

**Consequence for test and benchmark authors:** an utterance whose intent has an
unfilled required slot cannot be repeated indefinitely. MEASURED as repeatable
(complete intents, `ActionRespond`): `request_callback` with a number,
`request_transfer`, `repeat`, `hold`, `caller_identity`. MEASURED as not
repeatable: `leave_message`, `call_purpose`, `greeting`.

## Conversation length bound — MEASURED, with a correction

Source: `conversation/persona.go:138` — the default persona has
**`MaxTurns: 40`**, compared at `planner.go:135`
(`in.TurnCount >= in.Persona.MaxTurns`).

`TurnManager.Count()` returns `len(t.history)` (`turn.go:469`) and counts turns
for **both parties**. So 40 turns is **20 caller round-trips**, which is exactly
what T12 measured:

```
turn 19 ok: action=respond state=listening
turn 20 speechcomplete ERR: conversation: terminal
        (state=escalated action=escalate reason="max_turns_reached")
```

**Correction to earlier checkpoints.** T12–T14 recorded this as
"MaxTurns = 20". That was the observed *caller-turn* count, not the configured
value. The configuration is `MaxTurns: 40` counting both parties; the observable
limit is 20 caller round-trips. Both describe the same frozen behaviour.

Other personas, not the default: `PersonaPersonalAssistant` 30,
`PersonaFraudShield` 20.

## Interaction with cancellation, interruption and termination

- **Cancellation** — a withdrawal ("never mind") maps to the frozen
  `IntentAbandoned`, and is deliberately **not** a denial: under `ExpectYesNo`,
  "no" is `IntentDeny` answering the question and the intent stays active.
- **Interruption** — barge-in is decided by the frozen `TurnManager` and floor
  arbitration, not by Phase 13. An interrupted turn leaves the conversation in a
  declared state.
- **Termination** — escalation (`clarification_exhausted`, `max_turns_reached`,
  `intent_rejected`) is terminal. Post-terminal events are refused with
  `conversation: terminal`, append no transition record, and change no state
  (`assertNoStaleOutput`, driven through `voice.Planner`).

## What Phase 13 does not do

No new clarification kind, no prompt wording, no retry policy, no budget of its
own, and no UX rule that is not already present in frozen code.
