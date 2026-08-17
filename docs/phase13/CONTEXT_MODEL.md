# Phase 13 — Context Model

**Phase 13 introduces no context or memory system.** It uses the frozen
`conversation.ContextEngine`, reached through `Bridge.Conversation(id).Context()`.
Neither `intent` nor `voiceintel` depends on `packages/go/memory` (VERIFIED
LOCALLY, `go list -deps -test`).

## Frozen configuration (source: `conversation/context.go:121-128`)

| Setting | Value | Line |
|---|---|---|
| `DefaultTTL` | 10 minutes | 123 |
| `TemporaryTTL` | 30 seconds | 124 |
| `MaxEntriesPerScope` | 256 | 125 |
| `MaxSnapshots` | 8 | 126 |

## Scopes and lookup precedence

Five scopes: `ScopeConversation`, `ScopeSession`, `ScopeBusiness`,
`ScopeTemporary`, `ScopeShared`.

`Lookup` searches in precedence order **Temporary → Conversation → Session →
Shared → Business** (`context.go:278`) — most specific first.

TTL by scope: `ScopeTemporary` gets `TemporaryTTL`; Conversation/Session/Shared
get `DefaultTTL`; `ScopeBusiness` does **not** expire (reference data —
expiring it mid-call would make the agent forget the opening hours it just used).

**Expiry is evaluated on read, not by a sweeper** (`context.go:250`) — a
per-conversation sweeper goroutine would be one goroutine per concurrent call
solely to delete entries nobody is reading.

## Per-session isolation — VERIFIED LOCALLY

Every `Conversation` gets its own `ContextEngine`. Verified across T7 (16
sessions), T9 (failure pressure), T10 (16 sessions with cancellation,
interruption, termination), T13 (12 fixtures on one shared bridge). Session A's
values are never observable in session B, in any scope.

## Bounded entries and eviction

At `MaxEntriesPerScope`, an insert of a **new** key evicts the oldest by
`Entry.SetAt` (`evictOldestLocked`, `context.go:233`). Replacing an existing key
skips the eviction check entirely (`context.go:221`).

VERIFIED LOCALLY: 768 inserts leave size at exactly 256; exactly one entry is
evicted per overflow insert; the newest always survives.

### FROZEN OBSERVATION 1 — tied timestamps make victim selection unspecified

`evictOldestLocked` selects with `e.SetAt.Before(oldest)`, which is **false for
equal timestamps**. When several entries share a `SetAt`, the victim is whichever
key Go's randomised map iteration yields first. This is reachable in production
wherever writes land within one clock tick.

**Not patched. Not a Phase 13 defect.** It is a property of frozen code.

What *is* guaranteed, and what Phase 13 asserts:

| Property | Tied timestamps | Distinct timestamps |
|---|---|---|
| Bound holds (≤256) | guaranteed | guaranteed |
| Exactly one eviction per overflow | guaranteed | guaranteed |
| Newest survives | guaranteed | guaranteed |
| **Which entry is evicted** | **UNSPECIFIED — not asserted** | guaranteed (oldest) |

Tests deliberately avoid asserting victim identity in the tied case
(`TestT11_EvictionDeterminismBoundary`,
`TestT13_ContextEvictionFixture/tied_timestamps_bound_only`). Phase 13's
determinism contract does not depend on it: the eviction scenario in T11's
golden advances the clock so every `SetAt` is distinct.

### FROZEN OBSERVATION 2 — `ScopeShared` does not share

`ScopeShared` is documented as *"visible across concurrent conversations for one
subject"*. That describes **intent, not a shared store**: because every
`Conversation` constructs its own `ContextEngine`, a `ScopeShared` entry remains
per-conversation.

VERIFIED LOCALLY by `TestContext_EveryScopeIsPerSessionIncludingShared` — session
B cannot see a `ScopeShared` value written by session A.

**Frozen observation, not a Phase 13 defect.** Pinned by a test because the name
invites the opposite assumption; a deployment that genuinely needs
cross-conversation sharing must build it deliberately.

## Turn persistence

Conversation-scoped context survives turn transitions
(`EventUtterance` → `EventSpeechComplete` → next turn). VERIFIED LOCALLY over a
4-turn dialogue (`TestT13_MultiTurnProgression`), including the engine's own
`last_intent` entry written at `engine.go:684`.

## Termination and session reuse

Nothing calls `ClearScope` in conversation's production code — context dies with
the object. On termination the frozen engine **removes** the conversation from
its active map (`engine.go:450`), so:

- a terminated session is no longer retrievable via `Engine.Get`;
- `Begin` with the same id necessarily creates a **fresh** conversation with a
  fresh, empty `ContextEngine`.

VERIFIED LOCALLY (`TestContext_TerminatedSessionCannotLeakIntoANewOne`,
`TestT10_TerminationDuringConcurrentWorkLeavesNoStaleState`). This is also why a
"reuse the stale session" defect is **not expressible** through that seam — T10's
first attempt at such a mutation was inert for exactly this reason.

## No persistence

No database, file, Redis or external store. Context is in-memory, per
conversation, and bounded. `persistence`, `redis` and `repository` are absent
from both modules' dependency closures.
