# Phase 15 — Response-Failure Semantics Decision Audit

**Status:** DECISION AUDIT ONLY. No production Go change, no frozen change, no
test added. Nothing implemented.

---

## 1. The problem, measured

When an intent is accepted and a response is planned but **no agent speech is
ever produced**, the conversation is left:

| | Conversation state | Floor | Terminal | Next caller utterance |
|---|---|---|---|---|
| healthy turn | `listening` | **caller** | no | planned normally |
| generation failure | **`speaking`** | **agent** | no | `action=ignore reason="backchannel"` |
| TTS failure | **`speaking`** | **agent** | no | `action=ignore reason="backchannel"` |
| governance denial | **`speaking`** | **agent** | no | `action=ignore reason="backchannel"` |

Because the agent still holds the floor, everything the caller says afterwards is
classified as a **backchannel and ignored**. The caller is *permanently unheard*
for the rest of the call, and nothing reports an error. This is a **silent
stuck-call**, which is worse than a loud failure.

## 2. THE DECISIVE FACT — only two code paths can release the agent floor

Every floor movement in the frozen engine, exhaustively (`engine.go`):

| Line | Call | Handler |
|---|---|---|
| 516 | `turns.Acquire(PartyAgent, true)` | `handleStart` |
| 526 | `turns.Release(PartyAgent, ExpectNothing)` | `handleGreetingComplete` |
| **564** | **`turns.Release(PartyAgent, last.Expectation)`** | **`handleSpeechComplete`** |
| **581** | **`turns.Acquire(PartyCaller, false)`** | **`handleSpeechComplete`** |
| **597** | **`turns.ForceYield(interruptBeneficiary(...))`** | **`handleInterrupt`** |
| 782/792 | caller release / agent acquire | `handleUtterance` |

**`transition()` moves state only — it never touches the floor** (`engine.go:393`).

> Therefore only `EventSpeechComplete` and `EventInterrupt` can return the floor
> to the caller. Both are semantically wrong for an abandoned response. **No
> other existing event can fix this, no matter which state it reaches.**

## 3. Why `EventFault` does NOT solve it

Traced rather than judged by name.

- `EventFault` → `Plan{ActionWait, "fault", StateError}` + `transition(StateError, TriggerFault)` (`engine.go:494`). Non-terminal.
- `StateError` may go to `{StateRecovery, StateEnded, StateEscalated}` (`state.go:292`).
- `Recover()` (`engine.go:803`): → `StateRecovery`; **if no snapshot → `StateEscalated`**; if a snapshot restores → `StateListening` with `TriggerRecovered`.

Two independent reasons this fails:

1. **Neither `EventFault` nor `Recover()` touches the floor.** Even on the happy
   `StateRecovery → StateListening` branch, the floor is still held by the agent,
   so the caller remains unheard. **The stuck condition survives.**
2. **Nothing in production ever calls `TakeSnapshot`** — verified: the only
   occurrence in the repository is its own definition (`context.go:326`). So
   `Recover()` always takes the `no_snapshot` branch and **escalates**, which
   ends the call.

Measured end-to-end: `EventFault` from speaking → `state=error floor=agent`;
`Recover()` → `state=escalated`; a later utterance → `ErrTerminal`.

**Conclusion: `EventFault` converts a silent stuck call into a terminated call
handed to a human, and does not restore the floor in any reachable path.**

## 4. Why the other existing events fail

| Event | Effect | Verdict |
|---|---|---|
| `EventSpeechComplete` | releases the floor correctly | **claims speech that never happened**; `handleSpeechComplete` also derives the next state from the previous plan's `Expectation`, so the engine would await an answer to a question the caller never heard |
| `EventInterrupt` | `ForceYield` → caller, so the floor IS restored | **nothing was interrupted**; it also writes an entry into `Interruptions().History()`, corrupting barge-in analytics with events that never occurred |
| `EventHangup` / `EventTimeout` | terminal | ends the call over a recoverable hiccup |
| `EventSilence` | not valid from `speaking` | — |

## 5. Failure-type comparison

| Failure | Domain vs infrastructure | Release floor? | Terminate? | Escalate? | ResponseAbandoned? |
|---|---|---|---|---|---|
| generation failure | infrastructure | **yes** | no | policy choice | **yes** |
| TTS failure | infrastructure | **yes** | no | policy choice | **yes** |
| governance denial | **domain outcome, explicitly “not a fault”** | **yes** | no | **no** — escalating every denial is wrong | **yes** |

All three share one *mechanical* need — release the floor without claiming
speech — while differing in what should happen **afterwards** (apologise, retry,
escalate). That difference is policy and belongs above the event, not inside it.

## 6. `EventResponseAbandoned` — evaluation against the required bar

| Criterion | Met? | Evidence |
|---|---|---|
| current contract cannot represent it | **yes** | §2 — only speech-complete/interrupt move the floor |
| floor can be safely restored | **yes** | reuses `Release(PartyAgent)` + `Acquire(PartyCaller)` |
| **no new state machine** | **yes** | **`StateSpeaking → StateListening` is ALREADY a declared transition** (`state.go:250-253`) |
| no second lifecycle system | **yes** | one event, one handler |
| one unambiguous meaning | **yes** | “the planned response will not be spoken” |
| existing semantics unchanged | **yes** | speech-complete and interrupt untouched |

**Key semantic point:** the handler must transition to **plain `StateListening`**,
NOT `stateForExpectation(last.Expectation)` as `handleSpeechComplete` does. The
agent never asked the question, so the caller cannot be answering it. Reusing the
expectation would leave the engine waiting for a yes/no nobody was asked for.

## 7. Minimum frozen surface, if approved

**Do not implement.** Exact surface, verified from source:

| File | Change | Note |
|---|---|---|
| `conversation/engine.go` | add `EventResponseAbandoned` **at the END** of the `EventKind` const block | the block is `iota`-based; inserting anywhere but the end renumbers every later event |
| `conversation/engine.go` | one case in `EventKind.String()` | |
| `conversation/engine.go` | one case in `Handle`'s switch | the `default` already returns invariant `INV-CV-1` |
| `conversation/engine.go` | new `handleResponseAbandoned()` — `Release(PartyAgent, ExpectNothing)`, `transition(StateListening, TriggerResponseAbandoned, reason)`, `Acquire(PartyCaller, false)` | mirrors `handleSpeechComplete` minus the expectation |
| `conversation/state.go` | one `TriggerResponseAbandoned` constant | |
| `conversation/*_test.go` | tests for the new behaviour | |

**No transition-table change is required** — the edge already exists. No
exhaustiveness test over `EventKind` was found that would also need updating.

Follow-up (non-frozen, separate approval): `voice/pipeline.go` would emit it on
the three failure branches.

## 8. Alternatives

| | Option | Semantic correctness | User experience | Lifecycle safety | Frozen impact | Complexity | Silent-failure risk |
|---|---|---|---|---|---|---|---|
| A | keep stuck floor | n/a | **caller unheard for the rest of the call** | broken | none | none | **highest — fails invisibly** |
| B | `EventFault` for gen/TTS, denial stuck | wrong for denial | ends call on a hiccup; denial still stuck | **does not release the floor** | none | low | high |
| C | `EventFault` for all | **mislabels denial as a fault** | escalates every denial to a human | **does not release the floor** | none | low | high |
| D | `EventSpeechComplete` for all | **false — claims speech** | engine awaits an answer to an unasked question | floor restored, state wrong | none | low | medium |
| E | **`EventResponseAbandoned`** | **correct and unambiguous** | caller can speak again; policy decides the rest | **correct** | **minimal, no new state** | low–medium | **lowest** |
| F | terminate on every failed response | honest but brutal | drops the call on a transient hiccup | safe | none | low | low |

## 9. Recommendation

**Option E.** It is the only option that both restores the floor and tells the
truth. B and C do not restore the floor at all — the defect survives them.

## 10. The product decision, stated separately

**ENGINEERING FACT (not a matter of opinion):** the frozen contract cannot
release the agent floor for a denied or failed response without either claiming
speech that did not happen or recording an interruption that did not happen.

**PRODUCT DECISIONS (not mine to make):**

1. Should a denied/failed response **release the floor** (caller may speak again,
   having heard nothing) or **escalate to a human**?
2. If it releases: should the caller hear an apology or retry first — and if so,
   which component owns that, given the response is exactly what failed?
3. Should governance denial behave differently from an infrastructure failure,
   given the engine treats denial as a domain outcome?
4. Is amending the frozen `conversation` contract acceptable at all, or is the
   stuck call preferable to a frozen-module change?

## 11. Test plan for the eventual implementation

Not implemented now.

1. governance denial releases the floor, conversation non-terminal
2. generation failure likewise
3. TTS failure likewise
4. floor holder becomes caller in all three
5. the next caller utterance is planned normally (not `backchannel`)
6. **zero** `TriggerSpeechComplete` on all three paths
7. **zero** entries in `Interruptions().History()`
8. next state is plain `listening`, never the previous plan's expectation state
9. exactly one abandonment transition per failed turn
10. session isolation — one session's abandonment does not affect another
11. determinism under `-count=10 -shuffle=on`
12. mutations: abandonment removed; emitted on the success path; emitted twice;
    expectation-derived state used instead of `listening`
