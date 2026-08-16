# 14 · AI Orchestration

**Subdomain:** **CORE** · **Prefix:** `AI` · **Topic domain:** `ai`

> Two agents live here, with two trust boundaries, and they share nothing.

---

## 14.1 Purpose

Conduct the conversation with the caller on the Subscriber's behalf, produce the
record of it, and separately answer the Subscriber's questions about their own
calls — while guaranteeing that the caller-facing agent can never be talked into
revealing who it is speaking for.

## 14.2 Responsibilities

**Owns**

- `ScreeningConversation` — the caller-facing conversation
- `Announcement` — deterministic, model-free, immutable (I1)
- `Transcript` and `CallSummary` — the durable record
- `AssistantSession` — the subscriber-facing Personal Assistant
- Model tier routing across the four-tier ladder (ADR-0006)
- The safety layer and prompt-injection defence (I4, I11)
- **Emergency intent detection** and the handover it triggers (U7)
- `PromptTemplate`, `PromptRollout`, `EvaluationRun`

**Does not own**

| Not owned | Owned by |
|---|---|
| Speech recognition and synthesis | Voice (Customer–Supplier) |
| The call and its media path | Telephony (Partnership) |
| The fraud verdict | Fraud Detection |
| How the assistant should sound and what it may disclose | Consumer sets it; AI enforces it |
| The operator surface over prompts and evals | Administration commands; AI owns the data and its invariants |

### Why prompts and evaluations live here

The invariants guarding them are **AI-domain invariants**: the announcement
cannot be model-generated (I1), thinking stays enabled on tool-calling tiers
(I3), a rollout requires a passing evaluation. Housing the data in
Administration would place those invariants in a context that does not
understand them, and enforcement would degrade from a construction-time
guarantee to a UI check.

---

## 14.3 Domain Entities

### `ScreeningConversation` — aggregate root, ephemeral

**Attributes**

```
id              : ConversationId           INTERNAL  · EPHEMERAL
callSessionId   : CallSessionId <ref>      INTERNAL  · EPHEMERAL
lineId          : LineId <ref>             INTERNAL  · EPHEMERAL
announcement    : Announcement             PUBLIC    · EPHEMERAL
disclosureScope : DisclosureScope          PERSONAL  · EPHEMERAL
turns           : ConversationTurn[] <owned>
currentTier     : ModelTier                INTERNAL  · SHORT
tierHistory     : TierTransition[] <owned> INTERNAL  · SHORT
toolInvocations : ToolInvocation[] <owned> INTERNAL  · SHORT
state           : ConversationState        PUBLIC    · EPHEMERAL
safetyEvents    : SafetyEvent[] <owned>    INTERNAL  · SHORT
startedAt       : Instant                  INTERNAL  · EPHEMERAL
endedAt         : Instant?                 INTERNAL  · EPHEMERAL
```

**Relationships** — One per screened `CallSession`. Reads `AssistantProfile` and
`DisclosureScope` from Consumer at construction and **holds them immutably for
the session's life** — a mid-call preference change must not retroactively widen
what the agent may say. Produces a `Transcript` on completion.

**Lifecycle** — Created when Telephony answers. **Its first act is always the
Announcement** — there is no state from which the conversation loop is reachable
without it. Lives in Redis; the durable artefact is the `Transcript`, written
after the call. Ended by conclusion, caller hangup, takeover, transfer, safety
termination, or emergency handover.

**Validation Rules** — Cannot be constructed without a resolvable
`Announcement` for the configured locale. `currentTier` may only move along the
ADR-0006 ladder. A tool invocation outside `disclosureScope` is refused at
invocation, not filtered from the response — filtering a response that should
never have been produced leaves the data in a log somewhere.

**Privacy Classification** — `EPHEMERAL`. `disclosureScope` is `PERSONAL`
because its content describes the Subscriber. Turn content is `SENSITIVE` and
becomes durable only as a `Transcript`.

**Audit Requirements** — **Change** on safety events, injection detections, and
emergency detections. The conversation itself is not audited per-turn; the
Transcript carries the record.

---

### `ConversationTurn`

```
id           : TurnId               INTERNAL  · EPHEMERAL
speaker      : Speaker              PUBLIC    · EPHEMERAL
content      : String               SENSITIVE · EPHEMERAL · residency-bound
provenance   : Provenance           PUBLIC    · EPHEMERAL
tier         : ModelTier?           INTERNAL  · SHORT
segmentRef   : SegmentId? <ref>     INTERNAL  · EPHEMERAL
occurredAt   : Instant              INTERNAL  · EPHEMERAL
latencyMs    : Int?                 INTERNAL  · SHORT
```

**Lifecycle** — Appended as each side completes a turn. Promoted to a
`TranscriptTurn` when the conversation ends. **Only final utterances become
turns** (INV-VO-2 from the supplier side).

**Validation Rules** — `provenance` is mandatory and drives the interface's
AiBadge (U4). A caller turn always carries `provenance = CALLER_SPEECH`, which
is the domain's way of marking it untrusted. An assistant turn always carries
`MODEL` or `DETERMINISTIC` — and the Announcement turn is `DETERMINISTIC`,
which is why it carries no AiBadge.

**Privacy Classification** — `SENSITIVE`. Speech content.

---

### `Announcement` — **value object**, not an entity

Modelled as a value object deliberately, so that it cannot acquire a setter.

```
version     : AnnouncementVersion    PUBLIC · LEGAL_HOLD
locale      : LanguageTag            PUBLIC · LEGAL_HOLD
text        : String                 PUBLIC · LEGAL_HOLD
effectiveFrom: Instant               PUBLIC · LEGAL_HOLD
```

**Why a value object.** It has no lifecycle of its own, no identity beyond its
version, and — most importantly — **no mutable state**. An entity invites a
`setText`. A versioned immutable value invites publishing a new version, which
is exactly the operation we want to be deliberate and reviewable.

**Validation Rules** — Model-free by construction: it is retrieved from a
versioned catalogue, never generated. It must identify both the automated
assistant and the recording. There is no code path that renders a conversation
without one.

**Privacy Classification** — `PUBLIC`, `LEGAL_HOLD`. The text itself is not
personal data, and retaining every version permanently is how we prove what a
caller was told on a given date.

**Audit Requirements** — **Change** level on publication. Every played
announcement is evidenced by
`telephony.call.announcement_played.v1` carrying its version.

---

### `Transcript` — aggregate root, durable

**Attributes**

```
id             : TranscriptId            INTERNAL  · STANDARD
callSessionId  : CallSessionId <ref>     INTERNAL  · STANDARD
subscriberId   : SubscriberId <ref>      INTERNAL  · STANDARD
turns          : TranscriptTurn[] <owned>
annotations    : TranscriptAnnotation[] <owned>
primaryLanguage: LanguageTag             PUBLIC    · STANDARD
announcementVer: AnnouncementVersion     PUBLIC    · LEGAL_HOLD
expiresAt      : Instant                 INTERNAL  · STANDARD
deletedAt      : Instant?                INTERNAL  · STANDARD
deletionReason : DeletionReason?         PUBLIC    · STANDARD
```

**Relationships** — References a `CallSession`. Referenced by
`FraudAssessment`'s `EvidenceReference`, by `CallSummary`, and by Consumer's
`CallHistoryEntry`. It references neither of them — the record does not depend on
what was concluded about it.

**Lifecycle** — Finalised when the conversation ends. **Append-only
thereafter** (INV-AI-4): a correction is a `TranscriptAnnotation`, attributed and
timestamped, never an edit. Expires per the Subscriber's retention preference
(7–180 days, default 90). Deleted on expiry, call deletion, or erasure — and the
reason is retained so the interface states the policy rather than showing an
error.

**Validation Rules** — Every transcript begins with the Announcement turn
(INV-AI-1). `expiresAt` cannot exceed the Subscriber's preference. A transcript
cannot exist for a call that was never screened.

**Privacy Classification** — `SENSITIVE` throughout, residency-bound. Never
exported to analytics. Access individually audited.
`announcementVer` is `LEGAL_HOLD` and survives the transcript's deletion — proof
of lawful basis outlives the content it authorised.

**Audit Requirements** — **Access** level. Every read through Administration
requires an `AccessGrant` scoped to this session (INV-AD-4), and every read
writes an entry the Subscriber can request.

---

### `CallSummary`

```
id            : SummaryId            INTERNAL  · STANDARD
transcriptId  : TranscriptId <ref>   INTERNAL  · STANDARD
text          : String               SENSITIVE · STANDARD · residency-bound
provenance    : Provenance           PUBLIC    · STANDARD
modelVersion  : ModelVersion         INTERNAL  · STANDARD
promptVersion : PromptVersion        INTERNAL  · STANDARD
generatedAt   : Instant              INTERNAL  · STANDARD
```

**Validation Rules** — `provenance` is always `MODEL`, and the interface is
required to render an AiBadge (U4). `modelVersion` and `promptVersion` are
mandatory: a summary whose origin cannot be reconstructed cannot be evaluated
when a model changes.

**Lifecycle** — Generated after the call. Regenerated only by an explicit
rescoring command, which creates a **new** summary superseding the old rather
than editing it.

---

### `AssistantSession` — aggregate root, the Personal Assistant

**Attributes**

```
id            : AssistantSessionId       INTERNAL  · EPHEMERAL
subscriberId  : SubscriberId <ref>       INTERNAL  · EPHEMERAL
messages      : AssistantMessage[] <owned>
scope         : AssistantScope           INTERNAL  · EPHEMERAL
pinned        : Boolean                  PUBLIC    · STANDARD
startedAt     : Instant                  INTERNAL  · EPHEMERAL
```

**Relationships** — References exactly one authenticated `Subscriber`. Its tool
set is **disjoint** from the Screening Agent's (INV-AI-8).

**Lifecycle** — Ephemeral per session by default; persisted only if the
Subscriber pins it. **There is no permanent chat log to mine or to leak** — that
absence is a design decision, not an oversight.

**Validation Rules** — `scope` is exactly one subscriber's own data, resolved
server-side from the authenticated session and never from a parameter. A message
containing a claim with no `Citation` is **not rendered** (P-AI-9) — Principle 2
enforced at construction rather than by prompt instruction.

**Privacy Classification** — `EPHEMERAL` unless pinned, then `STANDARD`.
Message content is `SENSITIVE`.

**Audit Requirements** — **Change** on pinning. Content is the Subscriber's own
and is not audited on their own reads.

---

### `AssistantMessage`

```
id          : MessageId          INTERNAL  · EPHEMERAL
role        : MessageRole        PUBLIC    · EPHEMERAL
content     : String             SENSITIVE · EPHEMERAL
provenance  : Provenance         PUBLIC    · EPHEMERAL
citations   : Citation[]         INTERNAL  · EPHEMERAL
toolClasses : ToolClass[]        PUBLIC    · EPHEMERAL
proposedActions : ProposedAction[] <owned>
```

**Validation Rules** — An assistant message asserting a fact about a call
carries at least one `Citation`. `toolClasses` names *what class of tool was
consulted* — "searched 90 days of calls" — and never the reasoning, never the
prompt, never chain-of-thought (INV-AI-10).

---

### `PromptTemplate` · `PromptRollout` · `EvaluationRun`

```
PromptTemplate
  id · tier · name · body · version · status · authorRef · publishedAt
  classification: INTERNAL · STANDARD   (body is INTERNAL, never SECRET —
                                         it is reviewable, not a credential)

PromptRollout
  id · templateId · fromVersion · toVersion · percentage
  evaluationRunId <ref>   ── MANDATORY
  rollbackTriggers[] · startedAt · state

EvaluationRun
  id · templateId · gates[] · results[] · startedAt · completedAt · verdict
  gates: ACCURACY · FRAUD_RECALL · SAFETY · INJECTION · LATENCY · COST
```

**Validation Rules — enforced at save, not at review**

| Rejected at save | Because |
|---|---|
| A template that removes or model-generates the Announcement | **I1** |
| A template that disables thinking on a tool-calling tier | **I3** — disabling it silently drops tool calls with no error |
| A template granting the Screening Agent a tool returning subscriber PII | **I4** |
| A rollout with no attached `EvaluationRun` | The gate exists to be passed, not admired |
| A rollout whose evaluation regressed `SAFETY` or `INJECTION` | Blocked at **any** percentage, with no UI override |

**Audit Requirements** — **Change** level on every publication, rollout and
rollback, attributed.

---

## 14.4 Value Objects

| Value object | Definition | Notes |
|---|---|---|
| **`Announcement`** | version + locale + text + effectiveFrom | Immutable. The caller's lawful basis (I1) |
| `ModelTier` | `NONE` · `HAIKU_4_5` · `SONNET_5` · `OPUS_5` | The four-tier ladder, fixed by ADR-0006. Shared kernel |
| `TierTransition` | from + to + reason + at | Escalation is a recorded decision, not a side effect |
| **`Provenance`** | `MODEL` · `DETERMINISTIC` · `EDITORIAL` · `CALLER_SPEECH` | Drives the AiBadge. **Every string in the product has one** |
| `ToolInvocation` | name + argsHash + resultClass + at | Args are hashed, not stored — they may echo caller speech |
| `ToolClass` | `CALL_SEARCH` · `TRANSCRIPT_READ` · `AVAILABILITY_CHECK` · … | What was consulted, disclosed to the user. Never the reasoning |
| `Citation` | callSessionId + turnId | A claim with none is not rendered |
| `ThinkingState` | in-flight request reference + startedAt | **True only while a request is genuinely outstanding** (U8) |
| `SafetyEvent` | type + severity + action + at | `INJECTION_DETECTED` · `UNSAFE_REQUEST` · `SCOPE_VIOLATION` · `EMERGENCY_DETECTED` |
| `EmergencyDetection` | confidence + triggeringTurnId + detectedAt | Control-flow, not a risk grade |
| `AssistantScope` | exactly one `SubscriberId`, server-resolved | Never a parameter |
| `ProposedAction` | actionType + targets[] + requiresReview | The Personal Assistant proposes; it never executes |
| `ConversationState` | See [§14.13](#1413-state-machines) | |

---

## 14.5 Aggregates

| Aggregate | Root | Contains | Store |
|---|---|---|---|
| **ScreeningConversation** | `ScreeningConversation` | `ConversationTurn[]`, `TierTransition[]`, `ToolInvocation[]`, `SafetyEvent[]` | Redis |
| **Transcript** | `Transcript` | `TranscriptTurn[]`, `TranscriptAnnotation[]` | `content` Aurora + S3 |
| **CallSummary** | `CallSummary` | — | `content` Aurora |
| **AssistantSession** | `AssistantSession` | `AssistantMessage[]`, `ProposedAction[]` | Redis; Aurora if pinned |
| **PromptTemplate** | `PromptTemplate` | — | `content` Aurora |
| **PromptRollout** | `PromptRollout` | `RollbackTrigger[]` | `content` Aurora |
| **EvaluationRun** | `EvaluationRun` | `GateResult[]` | `content` Aurora |

```
╔══════════════ SCREENING AGENT ══════════════╗  ╔═══ PERSONAL ASSISTANT ═══╗
║  counterparty: HOSTILE BY ASSUMPTION        ║  ║  counterparty: TRUSTED   ║
║                                             ║  ║                          ║
║ ┌─────────────────────────────────────────┐ ║  ║ ┌──────────────────────┐ ║
║ │ ScreeningConversation  «root»  EPHEMERAL│ ║  ║ │ AssistantSession     │ ║
║ │  Announcement  ◀── immutable VO, FIRST  │ ║  ║ │ «root»  EPHEMERAL    │ ║
║ │  disclosureScope ◀─ held immutably      │ ║  ║ │  scope = ONE         │ ║
║ │  ┌───────────────┐ ┌──────────────────┐ │ ║  ║ │   subscriber, server │ ║
║ │  │ConversationTurn│ │ ToolInvocation[] │ │ ║  ║ │   resolved           │ ║
║ │  │ provenance ✱  │ │ NO SUBSCRIBER PII│ │ ║  ║ │  ┌────────────────┐  │ ║
║ │  └───────────────┘ └──────────────────┘ │ ║  ║ │  │AssistantMessage│  │ ║
║ │  ┌──────────────┐  ┌──────────────────┐ │ ║  ║ │  │ citations ✱    │  │ ║
║ │  │TierTransition│  │ SafetyEvent[]    │ │ ║  ║ │  │ toolClasses    │  │ ║
║ │  └──────────────┘  └──────────────────┘ │ ║  ║ │  └────────────────┘  │ ║
║ └────────────────┬────────────────────────┘ ║  ║ └──────────────────────┘ ║
╚══════════════════╪══════════════════════════╝  ╚══════════════════════════╝
                   │  DISJOINT TOOL SETS · DISJOINT PROMPTS · INV-AI-8
                   ▼
        ┌────────────────────────────┐     ┌─────────────────────────┐
        │ Transcript  «root» DURABLE │────▶│ CallSummary  «root»     │
        │  append-only · SENSITIVE   │     │  provenance = MODEL     │
        │  announcementVer LEGAL_HOLD│     └─────────────────────────┘
        └────────────────────────────┘

   ┌──────────────────┐   ┌──────────────────┐   ┌────────────────────┐
   │ PromptTemplate   │──▶│ PromptRollout    │◀──│ EvaluationRun      │
   │ «root»           │   │ «root»           │   │ «root»             │
   │ I1 · I3 · I4     │   │ evalRunId        │   │ 6 gates            │
   │ enforced at save │   │ MANDATORY        │   │ SAFETY blocks all  │
   └──────────────────┘   └──────────────────┘   └────────────────────┘
```

---

## 14.6 Domain Services

| Service | Responsibility | Notes |
|---|---|---|
| **`AnnouncementService`** | Return the versioned `Announcement` for a locale | Deterministic lookup. **No model is reachable from this path** — a model here could be suppressed by prompt injection (I1 rationale) |
| `TierRoutingService` | Route along the ADR-0006 ladder and record every transition | Escalates on complexity, **never on caller request** |
| **`SafetyLayerService`** | Evaluate every model output before it is spoken | **Never shed under load** (I11) |
| **`PromptInjectionDefenceService`** | Treat caller speech as hostile input | The agent talks to hostile strangers by design (I4) |
| **`ToolAuthorisationService`** | Refuse any invocation exceeding `DisclosureScope` | Refuses at invocation, not by filtering the response |
| `EmergencyDetectionService` | Classify emergency intent and terminate the conversation | Control-flow, immediate, irreversible for that call (U7) |
| `SummaryGenerationService` | Produce a `CallSummary` with full provenance | |
| **`AssistantScopeService`** | Resolve the Personal Assistant's scope from the authenticated session | Never from a parameter. A scope parameter is a horizontal privilege escalation waiting to be found |
| **`CitationEnforcementService`** | Suppress any assistant claim lacking a `Citation` | Principle 2 as a construction-time constraint |
| `EvaluationGateService` | Evaluate the six gates and permit or block a rollout | Safety and injection regressions block at any percentage |

---

## 14.7 Repositories

`TranscriptRepository` · `CallSummaryRepository` · `PromptTemplateRepository` ·
`PromptRolloutRepository` · `EvaluationRunRepository` ·
`AssistantSessionRepository` (pinned sessions only)

**`ScreeningConversation` has no repository.** It is Redis-resident and
ephemeral; the durable artefact is the `Transcript`.

---

## 14.8 Domain Events

| Event | Payload | Consumers |
|---|---|---|
| `ai.conversation.started.v1` | conversationId, callSessionId, announcementVersion | Telephony, Analytics |
| `ai.conversation.turn_completed.v1` | conversationId, turnId, speaker, tier, latencyMs | **Fraud**, Analytics |
| `ai.conversation.tier_escalated.v1` | conversationId, from, to, reason | Analytics, Billing |
| `ai.conversation.tier_downgraded.v1` | conversationId, from, to, reason | **Consumer** (degradation banner), Analytics |
| `ai.conversation.ended.v1` | conversationId, outcome, turnCount, durationMs | Telephony, Fraud, Billing |
| **`ai.emergency.detected.v1`** | callSessionId, confidence, triggeringTurnId | **Telephony, Notifications, Consumer** |
| `ai.safety.intervention.v1` | conversationId, eventType, action | Administration, Analytics |
| `ai.injection.detected.v1` | conversationId, patternClass | **Administration**, Fraud, Analytics |
| `ai.transcript.finalised.v1` | transcriptId, callSessionId, turnCount, language | Consumer, Fraud |
| `ai.transcript.deleted.v1` | transcriptId, reason | Consumer, Fraud |
| `ai.summary.generated.v1` | summaryId, transcriptId, modelVersion | Consumer |
| `ai.prompt.published.v1` | templateId, tier, version, authorRef | Administration |
| `ai.prompt.rolled_out.v1` | rolloutId, templateId, percentage, evaluationRunId | Administration |
| `ai.prompt.rolled_back.v1` | rolloutId, trigger | **Administration**, Analytics |
| `ai.evaluation.completed.v1` | runId, templateId, gatesPassed, gatesFailed | Administration |

**No event carries a turn's text, a summary's text, or a prompt body.** A
consumer that needs content fetches it from this context, subject to its access
rules. `ai.injection.detected.v1` carries a *pattern class*, never the injection
string — publishing an attack payload to every consumer would be an odd way to
handle a security event.

---

## 14.9 Commands

| Command | Refused when |
|---|---|
| `StartScreeningConversation(callSessionId, profile, scope)` | No `Announcement` resolvable for the locale |
| `ProcessCallerUtterance(conversationId, segmentId)` | Conversation not in `LISTENING` |
| `GenerateResponse(conversationId)` | Safety layer unavailable — **the conversation ends rather than proceeding unsafely** |
| `EscalateTier(conversationId, reason)` | Already at the top of the ladder |
| `InvokeTool(conversationId, tool, args)` | Tool exceeds `DisclosureScope`, or is not in the Screening Agent's set |
| `EndConversation(conversationId, outcome)` | — |
| `FinaliseTranscript(conversationId)` | Conversation not ended |
| `AnnotateTranscript(transcriptId, annotation)` | Transcript deleted |
| `GenerateSummary(transcriptId)` | Transcript deleted |
| `DeleteTranscript(transcriptId, reason)` | — |
| `AskAssistant(subscriberId, query)` | Scope cannot be resolved from the authenticated session |
| `ConfirmProposedAction(sessionId, actionId)` | Action not reviewed as a list |
| `PublishPrompt(template)` | Any of the four save-time validations fails |
| `StartRollout(templateId, version, percentage, evaluationRunId)` | **No evaluation run**, or safety/injection regressed |
| `RollbackPrompt(rolloutId, trigger)` | — (always permitted) |
| `RunEvaluation(templateId, gates)` | — |

**There is no `SkipAnnouncement`, no `DisableSafetyLayer`, and no
`SetAssistantScope` command.** Their absence is the enforcement.

---

## 14.10 Queries

| Query | Scope |
|---|---|
| `GetTranscript(transcriptId)` | Subscriber's own; Administration only with an `AccessGrant` scoped to the session |
| `SearchOwnTranscripts(subscriberId, query)` | **The Subscriber's own data only.** No global equivalent exists (INV-AD-6) |
| `GetSummary(transcriptId)` | Subscriber's own |
| `GetConversationState(conversationId)` | Live surface, internal |
| `GetAnnouncement(locale, version?)` | Public. The Subscriber sees exactly what callers hear |
| `GetPromptVersion(templateId, version)` | `ai_engineer` |
| `GetEvaluationResult(runId)` | `ai_engineer` |

---

## 14.11 Policies

| # | Policy |
|---|---|
| **P-AI-1** | The Announcement plays before any model output, always, deterministically. There is no conditional path (I1) |
| **P-AI-2** | Thinking stays enabled on every tool-calling tier (I3) |
| **P-AI-3** | Tier escalates on task complexity, **never on caller request**. A caller asking for "a manager" or "the smart AI" changes nothing |
| **P-AI-4** | Under load, downgrade a tier and notify honestly. **Never skip the safety layer** (I11) |
| **P-AI-5** | Caller-supplied text never becomes a command, a scope-widening tool argument, or interface chrome (I4, U10) |
| **P-AI-6** | Every model-generated string carries `Provenance = MODEL` (U4) |
| **P-AI-7** | Only final utterances become `TranscriptTurn`s |
| **P-AI-8** | A rollout requires a passing `EvaluationRun`; a safety or injection regression blocks it at any percentage, with no override in the tool |
| **P-AI-9** | The Personal Assistant renders no claim without a `Citation` |
| **P-AI-10** | Detected emergency intent terminates the conversation **immediately** and yields control to Telephony for handover (U7) |
| **P-AI-11** | `DisclosureScope` is captured at conversation start and held immutably. A mid-call widening does not apply retroactively |
| **P-AI-12** | When the safety layer is unavailable, end the conversation. Proceeding unsafely is never the degraded mode |

---

## 14.12 Invariants

| # | Invariant | Source |
|---|---|---|
| **INV-AI-1** | The `Announcement` is model-free and immutable. No subscriber setting, prompt, or operator action can alter it | **I1** |
| **INV-AI-2** | Thinking is enabled on every tool-calling tier | **I3** |
| **INV-AI-3** | No tool reachable by the Screening Agent returns subscriber PII beyond `DisclosureScope` | **I4** |
| **INV-AI-4** | A `Transcript` is append-only. Corrections are attributed annotations | |
| **INV-AI-5** | The Personal Assistant's scope is exactly one authenticated Subscriber's own data, resolved server-side | |
| **INV-AI-6** | Every string rendered to a user carries a `Provenance` | **U4** |
| **INV-AI-7** | `ThinkingState` is true only while a model request is genuinely outstanding | **U8** |
| **INV-AI-8** | The two agents share no session, no tool set, and no prompt | Principle 3 |
| **INV-AI-9** | An emergency detection terminates the conversation and cannot be overridden for that call | **U7** |
| **INV-AI-10** | Chain-of-thought is never persisted, never published in an event, and never rendered to any user | |
| **INV-AI-11** | Every `Transcript` begins with the Announcement turn | **I1** |
| **INV-AI-12** | A `PromptRollout` cannot exist without a passing `EvaluationRun` | ADR-0006 |

---

## 14.13 State Machines

### `ScreeningConversation`

```
                    INITIALISING
                         │
                         ▼
                    ANNOUNCING  ◀── I1. NO PATH BYPASSES THIS STATE.
                         │
                         ▼
          ┌─────────▶ LISTENING ──endpoint──▶ THINKING ──first frame──▶ SPEAKING
          │              ▲                        │                        │
          │              └──── barge-in ≤20ms ────┴────────────────────────┘
          │                                       │
          │                              safety layer unavailable
          │                                       ▼
          │                              SAFETY_TERMINATED «terminal»
          │
          ├── emergency detected ──▶ EMERGENCY_HANDOVER «terminal»
          │                            control passes to Telephony,
          │                            irreversible for this call
          │
          ├── takeover / transfer ──▶ YIELDED «terminal»
          │
          └── conversation concludes ─▶ CONCLUDING ──▶ ENDED «terminal»
                                                          │
                                                          ▼
                                                  Transcript finalised
```

**There is no transition from `INITIALISING` to `LISTENING`.** The absence of
that edge is Invariant I1 expressed structurally rather than as a check.

### `PromptRollout`

```
  DRAFT ──attach evaluation──▶ EVALUATED
                                   │
                    ┌──────────────┴──────────────┐
              gates passed              safety/injection regressed
                    │                              │
                    ▼                              ▼
              ROLLING_OUT                      BLOCKED «terminal»
              5% → 25% → 50% → 100%           no UI override exists
                    │
        ┌───────────┴───────────┐
        ▼                       ▼
   COMPLETED «terminal»   ROLLED_BACK «terminal»
                          (automatic trigger or manual)
```

### `Transcript`

```
  FINALISING ──▶ ACTIVE ──annotate──▶ ACTIVE  (append-only)
                    │
      ┌─────────────┼──────────────┬──────────────┐
      ▼             ▼              ▼              ▼
 RETENTION_    CALL_DELETED    ERASURE      CONSENT_WITHDRAWN
 EXPIRED           │              │              │
      └────────────┴──────────────┴──────────────┘
                          ▼
                     DELETED «terminal»
              reason retained; announcementVer
              survives under LEGAL_HOLD
```

---

## 14.14 Ownership

| Aspect | Value |
|---|---|
| Team | `callscreen/ai` |
| Services | `ai-orchestrator`, `transcript-service`, `prompt-registry` (Python 3.12) |
| Durable store | `content` Aurora, schema `content` |
| Ephemeral | Redis — conversation state, turn-taking context, pending tool calls |
| Objects | S3 — generated transcript artefacts |
| CODEOWNERS | `docs/domain/14-ai-orchestration.md`, `services/python/ai-orchestrator/**`, `services/python/transcript-service/**`, `services/python/prompt-registry/**` |

---

## 14.15 External Dependencies

| Dependency | Purpose | Guard |
|---|---|---|
| **Claude Haiku 4.5 / Sonnet 5 / Opus 5** | The four-tier ladder (ADR-0006) | **ACL.** The provider's message shape never reaches the domain. Tier ladder, tool protocol and thinking configuration are ours |
| Voice context | Utterances in, synthesis out | Customer–Supplier |
| Telephony context | Session lifetime, media path, handover | **Partnership** — neither may change the turn contract unilaterally |
| Consumer context | `AssistantProfile`, `DisclosureScope` | Read at start, held immutably |
| `tests/eval` fixtures | The six gates | Fixtures containing real transcript excerpts are **access-controlled as transcripts**, because that is what they are |

---

## 14.16 Security Constraints

| # | Constraint |
|---|---|
| 1 | **Caller speech is untrusted input, by design.** The agent talks to hostile strangers as its normal operating mode (I4) |
| 2 | **The Announcement path contains no model.** A model here could be suppressed by prompt injection, which is precisely why I1 exists |
| 3 | **Tools reachable by the Screening Agent are read-mostly and cannot disclose subscriber PII** beyond `DisclosureScope` (I4) |
| 4 | **Tool arguments are hashed, not stored** — they may echo caller speech verbatim |
| 5 | **The two agents' tool sets are disjoint by construction**, not by configuration (INV-AI-8) |
| 6 | **The Personal Assistant's scope is server-resolved.** No command accepts a scope parameter |
| 7 | **Chain-of-thought is never persisted, published or rendered** (INV-AI-10) |
| 8 | **`ai.injection.detected.v1` carries a pattern class, never the payload** |
| 9 | **Prompt bodies are `INTERNAL`, not `SECRET`** — they are reviewable artefacts, and treating them as credentials would prevent the review that keeps them safe |
| 10 | **Transcripts are `SENSITIVE`**: never exported to analytics, access individually audited, and readable through Administration only under a session-scoped `AccessGrant` |
| 11 | **A safety-layer outage ends conversations.** There is no degraded mode that proceeds without it (I11) |
| 12 | **Evaluation fixtures containing real transcript excerpts inherit transcript access controls** |
