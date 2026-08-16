# Voice Architecture

**Status:** IMPLEMENTED · dependency direction VERIFIED by test.

---

## 1. What this layer owns

Exactly four things:

1. **Sequencing** — moving a turn through recognition, planning, authorisation,
   generation, synthesis and delivery.
2. **Session state** — the 11-state machine in [SESSION_LIFECYCLE.md](SESSION_LIFECYCLE.md).
3. **Bounded handover** — every queue between stages has a fixed capacity.
4. **Output validity** — the generation guard that stops audio from an
   abandoned turn reaching a caller.

## 2. What it delegates, and to whom

| Concern | Owner | Reached via |
|---|---|---|
| Voice activity, overlap, endpointing | `audiointel` (11D) | `Intelligence` port |
| Provider selection, health, breaker | `speech.ProviderRouter` (11C) | `ProviderRegistry` |
| Dialogue decisions | `conversation.Conversation` (10B) | `Planner` port |
| Authorisation | `governance.Engine.Decide` (10E) | `Governor` port + `ToolGateway` |
| Generation | `runtime.Kernel` (10A) | `Generator` port |
| Sentence boundaries | `speech.Chunker` (11C) | direct |
| Interruption of Phase 11C synthesis | `speech.SpeechSession` (11C) | `audiobridge.Adapter` |
| Media delivery | `media` (11B) | `FrameSink` port |

**This layer implements none of them.** No VAD, no router, no policy engine, no
model, no chunker.

## 3. Ports, and why they are ports

Each port is a narrow interface satisfied by the frozen type **with no adapter
on its side**, carrying a compile-time assertion (`pipeline.go`):

```go
var (
	_ Intelligence = (*audiointel.Session)(nil)
	_ Planner      = (*conversation.Conversation)(nil)
	_ Governor     = (*governance.Engine)(nil)
	_ Generator    = (*rt.Kernel)(nil)
)
```

If a frozen signature changes, **this package stops compiling** — rather than
the mismatch surfacing as a pipeline that quietly stopped calling governance.

They are not for mocking. Depending on the concrete types would drag whole
assemblies — a kernel with a scheduler and model registry, an engine with a
policy store — into constructing a pipeline, and a failure anywhere in that
assembly would present as a pipeline failure.

## 4. Dependency direction — VERIFIED

```
telephony (11A) → media (11B) → audiointel (11D) → speech (11C)
                                        ↓
                                 voice (11E)  ←→  audiobridge (11D)
                                        ↓
                        conversation (10B) → governance (10E) → runtime (10A)
```

`go list -deps` on the voice root returns exactly eight first-party modules:

```
audiobridge  audiointel  conversation  governance
media        metrics     runtime       speech
```

**Zero third-party dependencies** across the whole module (MEASURED, Task 19
gate 6). Only the Go standard library and first-party packages.

### Forbidden edges, enforced by test

| Edge | Status | Enforcement |
|---|---|---|
| voice → `toolruntime` | **absent** | `TestGoverned_VoiceCannotReachToolRuntime` parses `go.mod` **and** every file's AST imports |
| voice → `memory` | **absent** | same test, plus `TestGoverned_VoiceHasNoMemoryWritePath` |
| adapter → voice root | **absent** | per-package `go list -deps`; prevents an import cycle |

A package that cannot import a tool executor cannot call one, whatever anybody
forgets. That is the whole argument — see
[GOVERNANCE_INTEGRATION.md](GOVERNANCE_INTEGRATION.md).

`providers/process` imports **only the standard library**.

## 5. Module layout

```
packages/go/voice/
  doc.go, ids.go, errors.go, state.go, classifications.go   vocabulary
  config.go, defaults.go                                     configuration + path validation
  events.go, metrics.go                                      observability (bounded)
  registry.go        Task 9   descriptors over speech.ProviderRouter
  fsm.go             Task 10  the 11-state machine
  pipeline.go        Task 11  the streaming turn
  bargein.go         Task 12  interruption orchestration
  governed.go        Task 13  the governed-action gateway
  providers/process/     shared supervision (stdlib only)
  providers/whispercpp/  speech.STTProvider
  providers/whispercli/  speech.STTProvider  (batch — see PROVIDER_COMPATIBILITY)
  providers/piper/       speech.TTSProvider
  providers/ollama/      runtime.Provider    (DEVELOPMENT-ONLY)
```

## 6. Concurrency model

| Goroutine | Owns | Lifetime |
|---|---|---|
| `ingest` | inbound frames; calls `Analyze`; opens turns | session |
| `recognize` | one turn's transcript stream | turn |
| `runTurn` | plan → govern → generate → synthesise | turn |
| `pumpAudio` | synthesised frames → media | turn |
| synthesis sink | `runtime.Sink`, driven by the frozen dispatcher | turn |

**FSM ownership is explicit.** Any goroutine may request a transition, and the
machine refuses anything the table does not declare. Only `beginTurn`'s
transitions were ever treated as fatal on refusal, which caused Defect 2 — see
[ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md). A refusal is now classified: it is
fatal only when the observable state cannot explain it.

## 7. What was deliberately not built

No second router. No second policy engine. No second interruption mechanism. No
second state machine. No cloud SDK. No API-key handling. No SIP/RTP/WebRTC. No
memory writer. No fabricated provider output anywhere.
