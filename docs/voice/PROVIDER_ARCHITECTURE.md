# Provider Architecture

**Status:** IMPLEMENTED · contracts VERIFIED · real-runtime execution varies by
provider — see [PROVIDER_COMPATIBILITY.md](PROVIDER_COMPATIBILITY.md).

---

## 1. Four adapters, two shapes

| Adapter | Implements | Transport | Real runtime here |
|---|---|---|---|
| `whispercpp` | `speech.STTProvider` | supervised subprocess | **NOT AVAILABLE** |
| `whispercli` | `speech.STTProvider` | supervised subprocess | **EXECUTED** (Task 17) |
| `piper` | `speech.TTSProvider` | supervised subprocess | **NOT AVAILABLE** |
| `ollama` | `runtime.Provider` | localhost HTTP | daemon up, **zero models** |

Three spawn processes and share `providers/process`. One speaks HTTP and
deliberately does not.

## 2. No vendor type escapes — VERIFIED

Exported surface of every adapter (`go doc`, Task 19):

```
Config   Provider   UnavailableError   [+ helpers over media types]
```

No whisper.cpp struct, no Ollama JSON type, no Piper type appears in any
exported signature. A caller holds `speech.STTProvider` or `runtime.Provider`
and cannot tell what is behind it — which is what makes replacing a local
provider with a cloud one a configuration change rather than a code change.

## 3. Honest-failure contract

Every adapter refuses to pretend. A missing binary or model produces a typed
error naming **the exact path checked** and **the command that fixes it**:

```go
type UnavailableError struct {
	Component string   // "executable", "model", "voice configuration"
	Path      string
	Remedy    string
	Cause     error
}
```

`errors.Is(err, ErrUnavailable)` matches, so a router can classify it. No
adapter fabricates a transcript, audio or a generation. Where a runtime is
absent, its tests report:

```
LOCAL PROVIDER RUNTIME NOT AVAILABLE
  provider:  …
  missing:   …
  needs:     …
  effect:    …
```

## 4. Process supervision (`providers/process`)

Written once rather than three times, because the differences between three
hand-rolled versions live in the parts nobody exercises until production.

**Guarantees (VERIFIED):**

| Guarantee | Test |
|---|---|
| No orphan — `Stop` ends with the child reaped, by kill if needed | `TestProcess_StopLeavesNoOrphan` (heartbeat-file observation) |
| No goroutine leak — every goroutine owned by a WaitGroup `Stop` joins | `TestProcess_NoGoroutineLeak` |
| No unbounded buffer — stderr is a fixed ring | `TestProcess_StderrIsBoundedAndNeverBlocksTheChild` |
| No indefinite wait — every blocking operation is bounded | readiness/stop timeouts |
| Output survives reaping | `TestProcess_RawStdoutSurvivesTheChildBeingReaped` |
| `Exited()` implies stderr is complete | `TestProcess_ExitedImpliesStderrIsComplete` |

The orphan check watches a file the child appends to, **not** a signal probe:
`os.Process.Signal` returns `EWINDOWS` for anything but `Kill`, so a signal probe
reports "not alive" for every process including live ones — a check that passes
without checking.

### Two supervision defects found by the gates

Both are documented in full in [ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md):

- **`cmd.Wait()` closing the pipes under a live reader** — os/exec documents that
  "it is incorrect to call Wait before all reads from the pipe have completed",
  and a background reaper does exactly that. Fixed by owning explicit
  `os.Pipe()`s. Symptom before the fix: silently truncated audio.
- **`Exited()` closing before the stderr drain finished** — a provider that died
  mid-utterance was reported with an **empty** stderr, losing the only
  explanation of why. Fixed by sequencing the drain before `Exited()` closes.

## 5. No shell, ever

Children start with `exec.Command(path, args...)` — an argv vector, not a
command string. Nothing in this module builds a command line, so caller text
cannot become a command.

**Path validation** (`config.go`) refuses, before any process starts: empty
paths; shell metacharacters; relative paths (which would make *which* binary
runs depend on inherited `PATH`); `.bat`/`.cmd`/`.ps1` (whose arguments cannot
be escaped reliably); missing files; directories.

**Environment** is an allowlist (`DefaultInheritEnv`), never the operator's whole
shell — a child started with everything exported receives every API key they
happen to have. Credential-shaped variables are rejected outright.

VERIFIED by `TestConfig_ArgvIsDataNotCommandLine`,
`TestConfig_RefusesUnsafeExecutablePaths`, `TestProcess_UnlistedVariablesAreNotInherited`,
and 15 hostile payloads driven through the live pipeline
(`TestSecurity_HostileTranscriptNeverEscapesAsCodeOrContent`).

**Model output reaches an external program exactly once** — Piper's stdin — and
never as an argument (`TestAdapter_ModelTextNeverReachesTheCommandLine`).

## 6. Why Ollama is HTTP, and what follows

The daemon is spoken to over stdlib `net/http` at a configured base URL.

- **Argv injection surface is zero** on that path: caller text travels as a JSON
  string field.
- **Process supervision does not apply.** The daemon's lifetime is the
  operator's business; this package neither starts nor stops it. Enforced by
  `TestProvider_ProcessSupervisionDoesNotApply`, which fails if the adapter ever
  imports `providers/process` or `os/exec`.

## 7. No model name is a default — DEVELOPMENT-ONLY

`ollama.Config.Model` has **no fallback**, and `DefaultConfig()` leaves it empty.
A default would make some particular open-weight model an implicit part of the
platform, and the first caller who forgot to set it would generate against a
model nobody chose.

The Ollama adapter is **DEVELOPMENT-ONLY**. ADR-0006 freezes a four-tier ladder,
every tier on Claude, and explicitly rejected self-hosted open-weight models as
its Option 5. Phase 11E **does not amend ADR-0006**. Enforced by:

- `Capabilities()` reports `Thinking=false`, `ToolCalling=false` — Invariant I3
  binds tool calling to extended thinking, so this provider cannot serve a
  production tier's work;
- a source scan failing the build's tests if any production model or vendor name
  appears in the package;
- `ProviderRegistry` in `ModeProduction` **refuses** a development-class
  provider, naming ADR-0006 in the refusal;
- a model provider is **described but never routed** — `RoutingTier()` returns
  "not routed", because returning `speech.Tier`'s zero value would read as
  "primary tier".
