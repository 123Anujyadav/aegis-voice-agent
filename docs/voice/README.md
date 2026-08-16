# Phase 11E — Local Voice AI Provider Integration

**Status:** implementation complete; **production traffic NOT approved.** See
[PLATFORM_READINESS.md](PLATFORM_READINESS.md) for the readiness matrix and the
open gaps.

`packages/go/voice` orchestrates a voice turn: caller audio in, answer out. It
owns orchestration and nothing else — voice activity detection, provider
routing, dialogue policy, authorisation and generation each belong to a frozen
module below it.

---

## How to read these documents

Every claim in this set is tagged. The tags are not decoration; they are the
difference between what was built, what was measured, and what nobody has run.

| Tag | Meaning |
|---|---|
| **IMPLEMENTED** | The code exists and compiles. |
| **MEASURED** | A number produced by a command recorded in this set. |
| **VERIFIED** | An executable test asserts it, and the test has been run. |
| **NOT RUN** | Nothing was executed. No inference is drawn from its absence. |
| **NOT AVAILABLE** | A runtime or tool is absent on this machine. |
| **DEVELOPMENT-ONLY** | Explicitly outside production routing. |
| **PRODUCTION REFERENCE** | A frozen ADR budget, quoted, never asserted here. |
| **KNOWN LIMITATION** | Understood, accepted, documented. |
| **OPEN RISK** | Unresolved, and a decision is owed before production. |

**NOT RUN never becomes PASS.** If you find a document implying otherwise, the
document is wrong.

---

## Document index

| # | Document | What it answers |
|---|---|---|
| 1 | [README.md](README.md) | This index and the reading rules |
| 2 | [VOICE_ARCHITECTURE.md](VOICE_ARCHITECTURE.md) | What this layer owns, what it delegates, dependency direction |
| 3 | [PROVIDER_ARCHITECTURE.md](PROVIDER_ARCHITECTURE.md) | Adapter design, ports, process supervision |
| 4 | [SESSION_LIFECYCLE.md](SESSION_LIFECYCLE.md) | The 11-state machine and its 38 declared edges |
| 5 | [STREAMING_PIPELINE.md](STREAMING_PIPELINE.md) | How a turn streams, and why it is not buffer-then-split |
| 6 | [BARGE_IN.md](BARGE_IN.md) | Interruption, the generation guard, stale-audio prevention |
| 7 | [GOVERNANCE_INTEGRATION.md](GOVERNANCE_INTEGRATION.md) | Why a governance bypass is structurally hard to write |
| 8 | [FAILURE_HANDLING.md](FAILURE_HANDLING.md) | The 17-case failure matrix and post-failure guarantees |
| 9 | [PERFORMANCE.md](PERFORMANCE.md) | Measured orchestration overhead, with methodology and limits |
| 10 | [SECURITY_AUDIT.md](SECURITY_AUDIT.md) | 28 audited areas, findings, fixes |
| 11 | [ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md) | The real defect history, including my own mistakes |
| 12 | [EVALUATION_REPORT.md](EVALUATION_REPORT.md) | Every gate, every result, every NOT RUN |
| 13 | [PROVIDER_COMPATIBILITY.md](PROVIDER_COMPATIBILITY.md) | What each adapter can do, and what actually ran here |
| 14 | [PLATFORM_READINESS.md](PLATFORM_READINESS.md) | Readiness matrix; implementation complete ≠ approved |

---

## The one-paragraph summary

The voice orchestration layer, four provider adapters and a shared process
supervisor are **IMPLEMENTED** and covered by **285 tests and 28 benchmarks**
across six packages, all passing under `go test -count=10 -shuffle=on ./...`
five consecutive times (**VERIFIED**). Orchestration overhead is **MEASURED**:
a whole turn costs ~95 µs and ~56 KB with no inference in it. Three production
defects were found by the verification gates themselves and fixed with
mutation-verified regression tests. Real speech was transcribed end-to-end
through the real openai-whisper CLI (**MEASURED**: 3.36 s inference for 4.12 s
of audio). The **LLM and TTS legs did not run** — Ollama has zero models pulled
and Piper is not installed on this machine — so the complete local voice loop
was **NOT** executable here. The Go race detector has **NOT RUN**: no C
compiler exists in this environment. That is an **OPEN RISK** and a
production-readiness blocker.

---

## Quick verification

```bash
cd packages/go/voice

gofmt -l . ./providers/*/     # expect no output
go vet ./...                  # expect exit 0
go test ./...                 # expect 6 packages ok
GOWORK=off go build ./...     # expect exit 0

# The gate that matters most, and the one that found three real defects:
go test -count=10 -shuffle=on ./...
```

**No API key, credential, cloud service, model download or internet access is
required for any of the above.**

---

## Scope boundaries

Phase 11E **does not** modify phases 10A–10F, 10.5 or 11A–11D. It adds no
second routing engine, no second policy engine, no second interruption
mechanism and no second state machine. `packages/go/voice` depends on neither
`toolruntime` nor `memory`, and a test enforces that by parsing imports.
