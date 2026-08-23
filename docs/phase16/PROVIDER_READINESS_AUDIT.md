# Phase 16 T8 — Provider Deployment & Real Model Readiness Audit

**Status: STOPPED AT THE APPROVAL BOUNDARY. Audit only — no production code
changed, nothing installed, nothing downloaded, no external host contacted, no
credential created.**

> **A STOP IS THE RESULT, not a failure.** Two of the three legs of the real
> path cannot be enabled on this machine without crossing a gate this task is
> forbidden to cross unilaterally.

---

## 1. Headline

| Gate | Answer | Why |
|---|---|---|
| **API KEY REQUIRED** | **NO** | No provider in this repository has an auth field. Ollama is a local daemon with no `Authorization` header; the three subprocess providers take paths, not credentials. |
| **MODEL DOWNLOAD REQUIRED** | **YES** — for generation and TTS | Ollama has **zero models pulled**; Piper has no voice model. Whisper's `tiny` model is **already cached**. |
| **NETWORK REQUIRED** | **YES** — to obtain those models | `ollama pull` and a Piper voice download. The runtime path itself is loopback/subprocess only. |
| **EXTERNAL BINARY REQUIRED** | **YES** — all three legs | `whisper.exe` (present), `piper` (**absent**), `ollama` (present, empty). |
| **THIRD-PARTY GO DEPENDENCY** | **NO** | Every provider package imports the standard library and first-party modules only. |

## 2. Provider inventory (traced, not inferred from filenames)

| Provider | Kind | Local/Cloud | API key | Model | Network | Binary | Deterministic | Used by `services/go/voice` |
|---|---|---|---|---|---|---|---|---|
| `speech.FakeSTTProvider` | STT | in-process | no | no | no | no | **yes** | yes (tests) |
| `speech.FakeTTSProvider` | TTS | in-process | no | no | no | no | **yes** | yes (tests) |
| `rt.FakeProvider` | generation | in-process | no | no | no | no | **yes** | yes (tests) |
| `providers/whispercpp` | STT | local subprocess | no | **yes** (GGML path) | no | **yes** (absent here) | no | no |
| `providers/whispercli` | STT | local subprocess | no | **yes** (`tiny`, **cached**) | no | **yes** (present) | no | no |
| `providers/piper` | TTS | local subprocess | no | **yes** (.onnx, absent) | no | **yes** (**absent**) | no | no |
| `providers/ollama` | generation | local HTTP daemon | **no** | **yes** (**none pulled**) | loopback | **yes** (present) | no | no |
| `providers/process` | supervision | — | no | no | no | — | n/a | no |

**Classification:** two deterministic fakes and one fake generator (class 1);
four local executable/model providers (class 2); **zero cloud APIs** (class 3);
**zero external services** (class 4); Piper and whisper.cpp are **unavailable**
on this machine (class 5).

**No cloud provider exists in this repository at all.** There is nothing to
point an API key at.

## 3. Measured machine state

From the repository's own probe (`packages/go/voice` `TestE2E_LocalIntegration`,
loopback only, nothing downloaded):

```
whisper CLI : available: …\Python312\Scripts\whisper.exe
Ollama model: NOT AVAILABLE — the daemon IS running but has ZERO models pulled
Piper       : NOT AVAILABLE — piper is not on PATH
```

That test executes stages 1–6 for real — including **real whisper inference**
(`model=tiny`, 55 characters from 4.12 s of audio, ~6.2 s) — and reports stages
7–8 as NOT RUN and 9–10 as BLOCKED. It fabricates nothing.

**So exactly one leg is runnable today: STT.** And even that invokes an external
binary, which Part 6 gates.

## 4. Where a real provider would plug in — no frozen change needed

```
service (non-frozen)
  └─ CallDriverConfig.Pipeline.Registry   ← voice.ProviderRegistry
       ├─ RegisterSTT(speech.STTProvider, ProviderSpec)
       ├─ RegisterTTS(speech.TTSProvider, ProviderSpec)
       └─ (generation is NOT routed here — Kernel owns the ladder)
  └─ CallDriverConfig.Pipeline.Generator  ← voice.Generator (an *rt.Kernel)
       └─ rt.Kernel.Providers().Register(rt.Provider)
```

`whispercli.New`, `whispercpp.New`, `piper.New` return values satisfying the
frozen `speech` ports; `ollama.New` satisfies `rt.Provider`. **The injection
points already exist and take interfaces.** Nothing in `conversation`,
`platform`, `intent`, `governance`, `voiceintel`, `speech`, `media` or the call
driver would change.

### The one real gap

`packages/go/voice/config.go` already defines `Config`, `STTProviderConfig`,
`TTSProviderConfig`, `ModelProviderConfig` — fully validated, with cross-section
invariants — and **nothing anywhere consumes them.** There is:

- **no `env:` tag** on any of them (`grep -c 'env:"' → 0`), and
- **no factory** turning a `voice.Config` into registered providers
  (`whispercli.New` etc. are called from **one test** and nowhere else).

So the smallest change to enable a real provider is a **service-local
configuration struct plus a registry-building function** — both non-frozen, both
in `services/go/voice`. `platform.LoadConfig(target any, prefix string)` is
generic, so the environment path needs no `platform` change either.

**This is a wiring gap, not an architectural one.** No new provider abstraction
is required and none is proposed.

## 5. Deployment-mode gate (found, and it matters)

`ProviderRegistry.describe` refuses a `ClassDevelopment` provider when the
registry is in `ModeProduction`:

> *"ADR-0006 freezes the production ladder and rejected self-hosted open-weight
> models; a development provider does not become a tier by being registered."*

**All four real providers are development-class.** A production-mode deployment
therefore cannot register any of them, by design. Enabling a real provider in
production requires either an ADR-0006 amendment or an explicit decision to run
the voice service in development mode — a **product decision**, not a code
change, and not one this task makes.

## 6. Failure semantics — one asymmetry to fix before real STT

| Real-provider failure | Existing expression | Adequate? |
|---|---|---|
| generation failure | `ErrProviderFailed` → `EventResponseAbandoned` | **yes** |
| TTS open/synthesis failure | `ErrProviderUnavailable` → `EventResponseAbandoned` | **yes** |
| governance denial | `ErrGovernanceDenied` → `EventResponseAbandoned` | **yes** |
| provider timeout | `ErrProviderTimeout` + the turn deadline | **yes** |
| cancellation | turn context; **no** lifecycle event | **yes** (T6/T7) |
| subprocess death | `ErrProcessFailed` | typed, but see below |
| **STT open failure** | `p.note(err)` → `stop(StateFailed, ReasonCrashed)` | **NO** |

**The gap:** an STT failure **ends the whole session** (`onFrame` → `beginTurn`
error → `stop(StateFailed)`), and **sends the conversation nothing**. No
`EventResponseAbandoned` — correctly, since no response was ever accepted — but
also no `EventFault` and no `EventHangup`, so the conversation is left open
while the voice session is dead. Nothing calls `Call.End` automatically.

With deterministic fakes this never fires. With a real subprocess recogniser —
which crashes, restarts and times out — it is a live path, and one provider
hiccup would end a call. **This must be settled before a real STT provider is
wired**, and it is not settled here: choosing between "end the call", "recover
the turn" and "tell the conversation" is a product decision, and inventing one
would be inventing policy.

## 7. Security audit — no defect found

| Check | Result |
|---|---|
| API keys logged | **n/a** — no provider accepts one |
| authorization headers logged | **n/a** — none are set anywhere |
| transcripts logged | no |
| raw PCM logged | no |
| environment dumped to a child | **no** — `ProcessConfig.BuildEnv` inherits **nothing** unless `InheritPathVars`, and then only 10 named variables (`PATH`, `SystemRoot`, `TEMP`, `HOME`, …). Wholesale inheritance is impossible. |
| credentials in child env | **actively refused** — `looksLikeCredential` rejects any `Env` entry containing `api_key`, `secret`, `token`, `password`, `credential`, `private_key`, `access_key` |
| credentials in published descriptors | **actively refused** — the same heuristic runs over `Engine`, `Version`, `Model`, `Revision`, `Quantization` at registration |
| shell injection | **structurally impossible** — `exec.Command(path, args...)`, an argv vector; nothing builds a command string |
| executable path safety | validated: absolute, no shell metacharacters, `.bat/.cmd/.ps1/.sh/.vbs` refused |
| secrets persisted or committed | none exist |

Two positive findings worth naming: the environment allowlist and the
credential heuristic are **pre-existing and enforced at validation time**, so a
future provider wiring cannot regress them silently.

## 8. Determinism boundary

If a real provider is ever approved, assertions must target **provider reached ·
valid response received · response contract valid · lifecycle completed · no
credential leakage · timeout and cancellation honoured** — never exact wording.

No NLU accuracy, precision, recall, F1, model quality or recognition-accuracy
claim is made anywhere in this audit. The one real inference measured (whisper
`tiny`, 55 characters) is reported as evidence that the **path executes**, not
as a quality result.

## 9. What would unblock each leg

| Leg | Blocker | Approval needed |
|---|---|---|
| **STT** | external binary invocation (`whisper.exe`, already installed; `tiny` already cached) | **invoke an external binary** — no download, no network, no key |
| **Generation** | zero models pulled | **`ollama pull <model>`: a multi-GB download over the network**, plus an operator-chosen model name |
| **TTS** | binary and voice both absent | **install the Piper binary and download a voice model** |
| **Any, in production mode** | development-class refusal | **ADR-0006 amendment, or run development mode** |

## 10. Boundary statement

**DETERMINISTIC INTERNAL CALL DRIVER ≠ REAL TELEPHONY**, and nothing in this
audit changes that. No claim is made about production telephony, AI accuracy,
compliance, SLA, customer readiness, real-world latency or provider reliability.
