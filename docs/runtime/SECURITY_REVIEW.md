# AI Runtime Core — Security Review

**Phase 10A** · `packages/go/runtime` · Reviewed 2026-08-08

---

## 1 · Why this component is security-critical

The runtime sits **directly on the path between a hostile caller's speech and a
language model**. ADR-0006 invariant I4 states the threat plainly: *the agent
talks to hostile strangers by design.* Every chunk this package moves originated
from an untrusted party, and every message it assembles may contain an attempt
to manipulate the model or exfiltrate the subscriber.

It also carries `SENSITIVE` data — caller speech, model output — as its primary
payload.

---

## 2 · Supply chain

**Zero external dependencies.** `go.mod` contains no `require` directive. The
attack surface of the most security-sensitive component in the platform is
exactly the Go standard library.

This is a deliberate control, not an accident of scope:

| Risk removed | How |
|---|---|
| Dependency confusion / typosquat | Nothing to resolve |
| Transitive compromise reaching every AI call | No transitives |
| A vendor SDK's own dependency graph | Adapters live outside the kernel |
| Version-pinning drift | No versions to drift |

**Verified:** `go.mod` has no `require` block. Adapters (gRPC, Redis, OTel) are
sibling modules that services opt into, and their compromise cannot reach the
kernel's logic — only the interface boundary.

---

## 3 · Data classification and handling

Per `contracts/proto/callscreen/common/v1/annotations.proto`.

| Field | Classification | Handling in this package |
|---|---|---|
| `Message.Content` | **SENSITIVE** | Never logged, never a metric label, never a span attribute |
| `Chunk.Text` | **SENSITIVE** | Same |
| `PromptRecord.Body` | INTERNAL | Reviewable, not a credential — see §6 |
| `Session.metadata` | INTERNAL | Documented as correlation identifiers only |
| Identifiers | INTERNAL | Opaque, non-sequential |
| Durations, counts, enums | PUBLIC | Logged and exported freely |

### Verified: no content reaches an observability sink

Audited every logging, metric and tracing call site.

- **Logging.** One statement on the generation path (`kernel.go`, "generation
  complete"). Fields: request ID, model ID, chunk count, aborted flag, TTFT,
  duration. **No content.**
- **Metrics.** All labels are enumerations (`class`, `reason`, `kind`,
  `provider`, `model`, `to`) or fixed strings. **No label derives from user
  input**, which also bounds cardinality — an unbounded label set is both a
  privacy leak and a metrics-system outage.
- **Tracing.** Span attributes are identifiers and enumerations. `Span.SetAttribute`
  carries a doc comment stating the prohibition.
- **`ToolInvocation.argsHash`** stores a hash, not arguments — tool arguments may
  echo caller speech verbatim.
- **`ai.injection.detected`-style reporting** carries a pattern class, never the
  payload. Publishing an attack string to every consumer would be an odd way to
  handle a security event.

**Residual risk:** these are conventions enforced by review, not by the type
system. A future contributor can log `msg.Content`. **Recommendation:** a CI lint
forbidding `Content` and `Text` in `slog`/span calls within this package. Tracked
as R1.

---

## 4 · Untrusted input handling

The runtime **transports** caller speech; it does not interpret it. That is the
correct posture for a runtime, and it is worth being explicit about what that
does and does not buy.

| Property | Status |
|---|---|
| Caller text is never parsed by the runtime | ✅ carried as opaque `string` |
| Caller text never becomes a control decision | ✅ no branch reads `Content` or `Text` |
| Caller text never becomes a metric label or log field | ✅ §3 |
| Caller text never reaches a filesystem path, command or query | ✅ none exist in this package |
| **Prompt-injection defence** | ❌ **not here** — orchestration layer |

**Stated plainly:** this package provides no injection defence and should not.
Injection is a semantic attack on a prompt, and this runtime has no semantics.
The defence belongs in AI Orchestration (`docs/domain/14`,
`PromptInjectionDefenceService`). **A reviewer must not read "handles untrusted
input safely" as "defends against prompt injection".** It does not.

---

## 5 · Denial of service

The publicly-reachable path into this runtime is a screened call, and ADR-0002
§10 names toll fraud against publicly-dialable DIDs as a live threat.

| Vector | Control | Verified |
|---|---|---|
| Concurrency exhaustion | `Scheduler.MaxConcurrent` semaphore | `TestScheduler_ShedsStandardAboveThreshold` |
| Queue growth | `MaxQueued` per class | shed on full |
| Slow-loris on a queue slot | `QueueTimeout`, bounded by request deadline | `Admit` deadline check |
| Work that cannot finish | Refused at admission | `TestScheduler_RefusesExpiredDeadline` |
| Session exhaustion | `MaxSessions`, idle TTL, max lifetime | `TestSessionManager_ShedsAtCapacity` |
| Abandoned sessions | TTL sweep | `TestSessionManager_ExpiresIdleSessions` |
| Provider hammering during an outage | Circuit breaker | `TestBreaker_OpensAtThreshold` |
| Retry storms | Bounded attempts + **jitter** | `DefaultRetryPolicy` |
| Stalled provider holding a slot | `MaxChunkGap` stall detection | `TestDispatcher_DetectsStalledStream` |
| Slow consumer holding a stream | Bounded handover, detach | `TestDispatcher_SlowSinkIsDetachedNotBlocking` |
| Unbounded context growth | Token budget + eviction | `TestContextWindow_EvictsOldestUnpinned` |
| Goroutine leak per abort | Reader exits on stream close | `TestDispatcher_NoGoroutineLeakAfterAbort` |

**Retry jitter deserves a note.** Without it, every client that failed at the
same instant retries at the same instant, and a provider recovering from an
outage is immediately knocked over by the synchronised retry it caused. This is
a self-inflicted DoS and the jitter is the control.

### The safety-class overshoot

`ClassSafety` **deliberately exceeds `MaxConcurrent`** rather than queueing. This
is a considered trade under I11: queueing safety work behind ordinary work under
load is precisely the failure the invariant forbids.

**The residual risk is real and should be reviewed:** if safety-class volume were
ever large relative to total volume, the overshoot would become the primary
capacity risk. It is bounded in practice because safety work is a small fraction
of traffic, and it is **measured** (`runtime_scheduler_overshoot_total`) so the
assumption is checked rather than assumed. **Recommendation:** alert on sustained
non-zero overshoot.

---

## 6 · Secrets

| Item | Handling |
|---|---|
| Provider credentials | **Not in this package.** Adapters own authentication |
| Prompt bodies | INTERNAL, not SECRET — deliberate |
| Session/request identifiers | Opaque, non-sequential, unguessable |
| Metrics, logs, traces | No secret material by construction |

**Prompt bodies are deliberately not SECRET.** Treating a prompt as a credential
prevents the review that keeps it safe (`docs/domain/14 §14.16`). They are
reviewable artefacts under access control, not keys.

### Identifier design

`newID` produces: 6 bytes millisecond timestamp · 2 bytes counter · **8 bytes
`crypto/rand`**, Crockford base32.

- Non-sequential: a monotonic counter in a log leaks session volume, which is
  commercially sensitive.
- Type-prefixed (`ses_`, `req_`, `str_`): a category error is visible in a log
  line rather than silently accepted — matching the frozen `ResourceId`
  reasoning.
- Entropy failure **panics** rather than degrading. If the OS entropy source has
  failed, every subsequent identifier would be predictable, and continuing is
  worse than crashing.

**Note:** `math/rand` is used for retry jitter only, seeded from time, guarded by
a mutex, and annotated `//nolint:gosec`. It is not used for anything requiring
unpredictability.

---

## 7 · Invariant enforcement as a security control

| Invariant | Security consequence if violated | Enforcement |
|---|---|---|
| **I3** thinking on tool-calling tiers | Tool calls dropped **silently** — an invisible failure | Refused at registration *and* at request build |
| **I11** never skip safety | Fraud scoring skipped under load — the attack window | `Class.Sheddable()` has no config input |
| **INV-AI-10** no chain-of-thought leak | Model reasoning exposed to a caller or a log | Sinks must opt in; default is safe |
| **INV-AI-12** rollout needs evaluation | An unevaluated prompt reaches production | `Activate` refuses empty `EvaluationRef` |

**The pattern is enforcement by absence** — a missing state-machine edge, a
missing command, a missing config field. There is no `SkipAnnouncement`, no
`DisableSafetyLayer`, no `SetAssistantScope`, and no flag that makes
`ClassSafety` sheddable. Absent capabilities cannot be misconfigured during an
incident.

---

## 8 · Findings

| # | Finding | Severity | Action |
|---|---|---|---|
| **R1** | Content-in-logs prohibition is convention, not compile-enforced | **Medium** | CI lint forbidding `Content`/`Text` in `slog` and span calls |
| **R2** | Race detector not run (audit A2) | **High** | Run `-race` in CI before approval. A data race in a security-critical concurrent component is a vulnerability, not just a bug |
| **R3** | `ClassSafety` overshoot is unbounded in principle | **Medium** | Alert on sustained `runtime_scheduler_overshoot_total` |
| **R4** | No rate limiting per caller/session inside the runtime | **Low** | Correct placement is `telephony-gateway` (ADR-0002 §10). Confirm it exists there before launch |
| **R5** | `Session.metadata` is free-form `map[string]string` | **Low** | Documented as identifiers-only; consider a typed key set |
| **R6** | Panic on entropy failure | **Accepted** | Correct behaviour; noted so it is not mistaken for a crash bug |
| **R7** | No authn/authz in this package | **By design** | Enforced at `edge-api`. Confirm no path reaches the runtime unauthenticated |

**R2 is the one that blocks.** Everything else is tracked or correctly placed
elsewhere.

---

## 9 · Threat model summary

| Threat | Mitigated | Where |
|---|---|---|
| Malicious caller speech manipulating the model | ⚠️ **Partially** | Runtime transports opaquely; **injection defence is orchestration's**, §4 |
| Caller speech reaching logs/metrics/traces | ✅ | §3 — verified by audit, not compile-enforced (R1) |
| Toll-fraud resource exhaustion | ✅ | §5, plus `telephony-gateway` admission (R4) |
| Compromised dependency | ✅ | Zero dependencies |
| Compromised provider adapter | ⚠️ **Partially** | Confined to the `Provider` interface; a hostile adapter can still return arbitrary chunks. Downstream must not trust chunk content — it never could, since it originates with the caller |
| Session hijack by identifier guessing | ✅ | 8 bytes crypto-random per identifier |
| Chain-of-thought exposure | ✅ | INV-AI-10, opt-in sinks |
| Safety layer skipped under load | ✅ | I11, no config path |
| Data race corrupting session/scheduler state | ❌ **UNVERIFIED** | R2 — blocking |

---

## 10 · Conclusion

The runtime's security posture rests on three deliberate properties: **zero
dependencies**, **no interpretation of untrusted input**, and **enforcement by
absence** rather than by configuration.

Its principal limitation is one of scope and should not be misread: **this
package provides no prompt-injection defence, no authentication and no
authorisation.** All three are correctly placed elsewhere, and a reviewer
assuming otherwise would be assuming a control that does not exist here.

**Recommendation: not approved for production traffic until R2 is closed.** A
concurrent, security-critical component whose race detector has never run has
not been verified, and repeat-run stability is a much weaker signal than it
appears.
