# Phase 13 — Security Properties

Status labels are used strictly: **IMPLEMENTED** (the code does it),
**VERIFIED** (a test proves it, and a mutation proves the test has teeth),
**NOT RUN** (not executed here).

**This document does not claim complete security assurance.** It records
specific properties that were verified, and is explicit about what was not.

## Credentials, network, dependencies

| Property | Status | Evidence |
|---|---|---|
| No API key required or present | VERIFIED | source scan: 0 hits for api-key/secret/bearer/password/openai/anthropic/URL in Phase 13 non-test source |
| No network dependency | VERIFIED | `go list -deps -test`: `net/http`, `net`, `net/url`, `crypto/tls` all absent |
| No model download, no external service | VERIFIED | no provider dependency in the closure; no runtime model inference exists |
| No third-party Go dependency | VERIFIED | `go list -deps -test`: **0** non-stdlib, non-callscreen packages in both modules |
| No process execution | VERIFIED | `os/exec` absent |
| No database or persistence | VERIFIED | `database/sql`, `persistence`, `redis`, `repository` absent |

Note on `go list -m all`: it reports the **workspace-wide** module union that
`go.work` creates and looks alarming. The meaningful measure is
`go list -deps -test`, which reports what the packages actually compile — 4
first-party packages plus stdlib for `intent`.

## Structural isolation

| Property | Status | Evidence |
|---|---|---|
| Classifier cannot execute tools | VERIFIED | no `toolruntime` dependency; no persistence/execution-shaped exported API (guard bans `Save/Store/Execute/Run/Invoke/...` prefixes) |
| No governance reference in `intent` | VERIFIED | AST import guard; `governance` absent from `intent` closure |
| No `memory` dependency | VERIFIED | absent from both modules |
| Governance/tool path not bypassed | VERIFIED | Phase 13 makes no governance decision; frozen refusal vocabulary preserved and distinct (T9) |

`voiceintel` does reach `governance` **transitively via `voice`** — pre-existing,
declared `// indirect` in its `go.mod` since T6, present in the non-test build.
Phase 13 did not introduce it and does not call it.

## Bounded inputs and state

| Bound | Value | Source |
|---|---|---|
| Tokens per utterance | 512 | `lexicon.go:217`, early return at `:247` |
| Intent vocabulary | 11 names, closed | `lexicon.go` `Vocabulary()` |
| Candidates returned | `DefaultMaxCandidates = 5` | `classifier.go` |
| Slots per intent | 4 | `slots.go` `MaxSlotsPerIntent` |
| Slot name length | 32 | `slots.go` `MaxSlotNameLen` |
| Slot value tokens | 6 | `slots.go` |
| Context entries per scope | 256 | `context.go:125` |

MEASURED: evidence is counted **per cue, not per occurrence** — 600 repetitions
of a cue score identically to one, so repetition cannot inflate confidence or
work.

## Sensitive data handling

**Transcript and slot values are treated as sensitive.**

| Property | Status | Evidence |
|---|---|---|
| Transcript never becomes a metric label, event id, intent name or slot name | VERIFIED | T5 AST guards + mutation |
| Slot **values** cannot cross the classifier port | IMPLEMENTED (frozen) | `conversation.Slot` has no value field; only shape (name/filled/confidence/required) is returned |
| Credential-shaped input does not leak into operational fields | VERIFIED | T9 canary `sk-live-AKIA-CANARY-9f3d` checked against `Plan.Reason`, `Plan.Intent`, `Plan.Escalation`, clarification slot/candidates, returned errors and every `TransitionRecord` |
| Raw PCM / byte payloads cannot enter classifier state | VERIFIED | T5 guards; T9 byte-payload fixtures produce bounded typed outcomes |
| Unbounded user strings never become operational labels | VERIFIED | reason codes bounded (<= 64 chars asserted); 200 000-char payload produces no leak |
| Intent names confined to the closed vocabulary | VERIFIED | asserted on every fixture outcome |

The classifier holds **no per-utterance state**: `Classify` has no receiver
mutation (AST-guarded), so one caller's words cannot influence another's
classification. The one package-level map (`timeWords`) is a read-only lookup
table, never ranged over and never written (T11 guards).

## Mutation verification

Security and structural guards were mutation-tested, not merely written.

This table covers the **security and structural** guards only and deliberately
omits T6's 4 bridge-wiring mutations, so it sums to 40 rather than the
**44** total recorded in [ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md). It is a
subset, not a competing count.

| Task | Mutations | Result |
|---|---|---|
| T5 security guards | 6 | 6 CAUGHT |
| T8 turn semantics | 5 | 5 CAUGHT |
| T9 failure handling | 7 | 7 CAUGHT |
| T10 concurrency/isolation | 5 | 5 CAUGHT |
| T11 determinism | 6 | 6 CAUGHT |
| T13 evaluation fixtures | 6 | 6 CAUGHT |
| T14 CI coverage | 5 | 5 CAUGHT |

Several mutations were initially **inert or non-compiling** and were corrected
rather than counted — see [ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md).

## NOT RUN / not claimed

- **`-race`: NOT RUN** locally (no C compiler). No memory-safety or
  race-freedom claim is made. See [CONCURRENCY.md](CONCURRENCY.md).
- **No penetration testing, fuzzing campaign, or third-party security review.**
- **No threat model** for a deployed system is claimed here.
- **No production traffic approval.** See
  [FINAL_REPORT.md](FINAL_REPORT.md) §16.
- Guards prove specific structural properties; they are not proof of overall
  system security.
