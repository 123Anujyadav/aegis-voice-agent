# Phase 11E — Security Audit

> Part of the Phase 11E documentation set — see [README.md](README.md).
> Readiness implications are in [PLATFORM_READINESS.md](PLATFORM_READINESS.md);
> the defect history is in [ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md).

**Date:** 2026-08-16 · **Task 18** · **Scope:** `packages/go/voice` and its five
provider subpackages.

Every claim below is backed by a source inspection, an executable test or a
command run during this audit. Nothing is asserted from memory.

---

## 1. Scope and methodology

**In scope:** `packages/go/voice` (orchestration, registry, FSM, pipeline,
barge-in, governance gateway) and `providers/{process,whispercpp,whispercli,piper,ollama}`.

**Out of scope:** frozen phases 10A–10F, 10.5, 11A–11D. They were *inspected*
where Phase 11E depends on them; none was modified, and no defect was found in
them during this audit.

**Method, in order:**

1. **Inventory** — enumerated the 51 existing security-relevant tests so the
   audit looked for gaps rather than re-proving covered ground.
2. **Source inspection** — read path validation, argv construction, the
   environment allowlist, temp-file lifecycle, error construction and the
   persistence boundary.
3. **Structural analysis** — reflection over event/descriptor/authorization
   types; **AST parsing** of every package for error constructions that
   interpolate content.
4. **Behavioural testing** — hostile payloads driven through the live pipeline.
5. **Mutation verification** — reintroduced the one real defect and confirmed
   both new guards fail.

---

## 2. Findings

One genuine defect was found and fixed. Everything else verified clean.

### SEC-1 — Model output leaked into an error message · **MEDIUM** · **FIXED**

| | |
|---|---|
| **Component** | `providers/ollama/ollama.go` (Task 8), streaming `Recv` path |
| **Category** | Sensitive data in errors (area 26) |

**Evidence.** The malformed-response branch read:

```go
return rt.Chunk{}, s.fail(rt.KindTransport, fmt.Errorf(
    "%w: %v (line: %s)", ErrMalformedResponse, err, truncate(line, 200)))
```

A test that scripts a truncated response produced this error verbatim:

```
ollama: malformed response: unexpected end of JSON input
(line: {"model":"m","message":{"role":"assistant","content":"your balance is
4111 1111 1111 1111 and the sort code is)
```

**Failure scenario.** A streamed line is `{"message":{"content":"…"}}`. A
connection cut mid-line, or a partial flush, yields unparseable JSON that still
contains the generated text. Model output is downstream of whatever the caller
said; `runtime.Chunk.Text` documents it as SENSITIVE — *"never logged, never a
metric label, never a span attribute"*. Errors **are** logged. Simulated card
digits reached the error string.

**Risk.** Real but bounded: requires a malformed/truncated response, caps at
200 bytes, and reaches a log rather than an external party. Not remotely
triggerable by a caller on its own — hence MEDIUM, not HIGH.

**Remediation (applied).** The offending bytes are no longer quoted. The error
now reports the parser's complaint and the line *length*, which carries the
actionable fact ("the daemon sent something that is not our wire format")
without the payload. The now-unused `truncate` helper was deleted rather than
left to invite reuse.

**Verification.** `TestSecurity_MalformedResponseDoesNotLeakModelOutput`
(behavioural) and `TestSecurity_ErrorsDoNotEmbedContent` (AST). Both fail when
the defect is reintroduced — see §7.

### SEC-2 — Temp audio directory persists if a stream is never closed · **LOW** · **ACCEPTED**

| | |
|---|---|
| **Component** | `providers/whispercli/whispercli.go` |

**Evidence.** `stream.cleanup()` (`os.RemoveAll(workDir)`) is called from
`Close` and from the open-error path. A caller that abandons the stream without
calling `Close` leaves the directory.

**Risk.** Low. `Close` is the documented contract, the pipeline always calls it
(`pumpAudio`/`runTurn` both defer it), and the directory is under the system
temp root. No caller in this module leaks a stream.

**Not fixed.** A finalizer would be the alternative and is worse: Go finalizers
have no ordering or timing guarantee, so it would convert a deterministic
cleanup into an unpredictable one. Recorded rather than papered over.

### SEC-3 — Non-200 response body is embedded in a transport error · **INFO** · **ACCEPTED**

**Evidence.** `Generate` embeds up to 4 KiB of a non-200 body.

**Assessment.** Distinct from SEC-1: a non-200 means generation did **not**
happen, so the body is the daemon's own error object, not model output. It is
bounded (`TestSecurity_TransportErrorBodyIsBounded` confirms the error stays
under 8 KiB when a 64 KiB body is sent). Kept, because "the daemon said no and
here is why" is the whole diagnostic.

---

## 3. Area-by-area results

| # | Area | Result | Evidence |
|---|---|---|---|
| 1 | Executable path validation | **PASS** | `validateExecutablePath`: rejects empty, shell metacharacters, relative paths, `.bat`/`.cmd`/`.ps1`, missing files, directories. `TestConfig_RefusesUnsafeExecutablePaths` |
| 2 | Model path validation | **PASS** | `validateModelPath`, same rules. `TestConfig_RefusesUnsafeModelPaths` |
| 3 | argv construction | **PASS** | `exec.Command(path, args...)` only; every adapter builds `[]string`. `TestConfig_ArgvIsDataNotCommandLine` |
| 4 | Shell injection resistance | **PASS** | No shell is ever invoked (§4). 15 hostile payloads driven through the live pipeline |
| 5 | Environment sanitisation | **PASS** | `InheritEnv` allowlist; `TestProcess_EnvironmentIsAllowlistedNotEmptied`, `TestProcess_UnlistedVariablesAreNotInherited` |
| 6 | Credential exposure | **PASS** | Full-source sweep: no keys, tokens, passwords or auth headers. Only the *detection* wordlist in `config.go` |
| 7 | Credential-shaped env vars | **PASS** | `looksLikeCredential` + `TestConfig_RefusesCredentialShapedEnvironment` |
| 8 | Transcript logging | **PASS** | Events carry `CharCount`, never text. `TestVoiceEvent_CarriesNoContent` (reflection) |
| 9 | Raw PCM persistence | **PASS** | `TestSecurity_NoModuleWritesAudioToDisk` across all six packages |
| 10 | Temporary audio files | **PASS** (see SEC-2) | Per-stream temp dir, removed by `cleanup`; `TestAdapter_RemovesBufferedAudioOnClose` |
| 11 | Session isolation | **PASS** | Task 15 shared-fleet: 12 sessions, one router/recogniser/voice/metric set |
| 12 | Provider isolation | **PASS** | Per-session streams; `TestConcurrency_SessionsSharingProvidersStayIsolated` |
| 13 | Process lifecycle / orphans | **PASS** | Heartbeat-file observation, not a signal probe. `TestProcess_StopLeavesNoOrphan`, `TestFailure_CrashedProviderLeavesNoOrphanProcess` |
| 14 | Process privilege | **PASS** | `TestSecurity_NoShellAndNoPrivilegeEscalation`: no `SysProcAttr`, `Credential{}`, `Setuid`, `runas` or interpreter invocation |
| 15 | Unbounded stderr/stdout | **PASS** | Fixed-size ring; `TestProcess_StderrIsBoundedAndNeverBlocksTheChild` |
| 16 | Queue exhaustion | **PASS** | All queues fixed-capacity; zero is *refused* at config (`TestSecurity_AnUnboundedQueueIsRefusedAtConfiguration`) |
| 17 | Session/resource exhaustion | **PASS** | `TestSecurity_EveryRetainedCollectionIsBounded` |
| 18 | Governance bypass | **PASS** | Three structural defences; `TestSecurity_GovernanceCannotBeBypassed` |
| 19 | Authorization replay / cross-session | **PASS** | Unforgeable + operation/resource-bound; `TestConcurrency_AuthorizationDoesNotLeakBetweenSessions` |
| 20 | Barge-in stale audio | **PASS** | Generation guard + turn-context guard; measured at the media sink |
| 21 | Generation-counter safety | **PASS** | Atomic; bumped before any blocking work |
| 22 | Provider switching | **PASS** | Frozen `speech.ProviderRouter`; no state from A leaks into B |
| 23 | Failure recovery | **PASS** | Task 14: all 17 cases |
| 24 | Event payload safety | **PASS** | Reflection ban on content/byte-slice fields |
| 25 | Metrics label cardinality | **PASS** | `TestConcurrency_SessionIdentifiersNeverReachALabel`; hostile payloads checked against every label |
| 26 | Sensitive data in errors | **FIXED** | **SEC-1** |
| 27 | Sensitive data in audit records | **PASS** | `ToolIntent` attributes are bounded codes; frozen validator caps at 256 chars. `TestGoverned_IntentCannotCarryContent` |
| 28 | Model/provider config boundaries | **PASS** | No hardcoded model; Ollama development-only, guarded by source scan |

---

## 4. Shell injection: why there is no attack surface

No package in this module ever constructs a command string. Children are started
with `exec.Command(path, args...)`, which passes an argv vector to the operating
system — there is no shell to interpret metacharacters.

`TestSecurity_HostileTranscriptNeverEscapesAsCodeOrContent` drives **15
payloads** (shell metacharacters, command substitution, SQL, Windows paths,
traversal, Devanagari, currency, 4 KiB, control bytes, embedded newlines)
through the live pipeline. All 15 subtests pass: the session stays valid, and no
fragment reaches an event field or metric label.

Model output — the one place text from a model reaches an external program — is
sent to Piper on **stdin**, never as an argument
(`TestAdapter_ModelTextNeverReachesTheCommandLine`).

---

## 5. Privacy assessment

- **Raw PCM is never persisted.** The single exception is documented and
  enforced: `providers/whispercli` writes a temporary WAV because the Python
  tool accepts a path and not a pipe. Per-stream temp dir, removed on `Close`.
- **Transcripts are never logged.** Events carry `CharCount`; the observer
  receives the segment because live captions need it, and nothing writes it.
- **Errors carry no content** after SEC-1, enforced by AST scan across all six
  packages.
- **Metric labels are a closed vocabulary** (`classifications.go`), and no
  per-call identifier reaches one.

---

## 6. Credential and API-key status

**No credential of any kind is present, required or introduced.**

- Source sweep: no API keys, tokens, passwords, authorization headers or secret
  env vars. The only credential-shaped strings are the *detection* wordlist.
- **No cloud credential is required** for any Phase 11E path. Every provider is
  local: whisper CLI (subprocess), Piper (subprocess), Ollama (localhost HTTP).
- `go.mod` has **zero third-party requires**; `go list -deps` shows stdlib and
  first-party modules only. Supply-chain surface is minimal — which also bounds
  how much `govulncheck` (NOT RUN) could have found.

**Forward-looking (not blocking):** cloud STT/TTS/LLM adapters in a later phase
would require vendor credentials. They are out of scope here and no
placeholder, fake key or credential field was added in anticipation.

---

## 7. Mutation verification

The one real defect was reintroduced to prove the new guards are not decorative:

| Guard | Result with defect reintroduced |
|---|---|
| `TestSecurity_ErrorsDoNotEmbedContent` (AST) | **FAIL** — `providers\ollama\ollama.go:619: an error interpolates a content-bearing value (line)` |
| `TestSecurity_MalformedResponseDoesNotLeakModelOutput` (behavioural) | **FAIL** — `leaked: "your balance is 4111 1111 1111 1111 …"` |

Mutation removed; both pass.

**A gap in my own test was found this way.** The AST scan initially parsed only
the orchestration package — it would *not* have caught SEC-1, which lives in
`providers/ollama`. It now scans all six packages, which is what made the
mutation above fail correctly.

---

## 8. Verification gates

| Gate | Result |
|---|---|
| `gofmt -l` (all packages) | clean |
| `go vet ./...` | clean |
| `go test ./...` | all 6 packages ok |
| `go test -count=10 -shuffle=on ./...` | ok (all 6; 225 s worst) |
| Targeted security tests | 8 new + 51 pre-existing, all PASS |
| Frozen phases modified | none |
| **`-race`** | **NOT RUN** — `cgo: C compiler "gcc" not found: exec: "gcc": executable file not found in %PATH%` |
| **`golangci-lint`** | **RAN** (v1.64.8) — see the correction below |
| **`gosec`** | **RAN via golangci-lint** — 11 findings, all assessed, none a genuine defect |
| **`staticcheck`** | **RAN via golangci-lint** |
| **`gosec` standalone** | **NOT RUN** — not installed |
| **`staticcheck` standalone** | **NOT RUN** — not installed |
| **`govulncheck`** | **NOT RUN** — not installed |

### Correction (Task 19, recorded rather than silently applied)

This section originally recorded gosec, staticcheck and golangci-lint as NOT
RUN. **That was incomplete.** `golangci-lint.exe` is present in `GOPATH/bin` — it
was simply not on `PATH` — and it bundles gosec and staticcheck. Task 19 fixed
the invocation rather than the code and ran it against the repository's own
`.golangci.yml`.

**gosec: 11 findings, every one assessed against the source, none a genuine
defect.**

| Finding | Assessment |
|---|---|
| G304 `whispercli.go` — file inclusion via variable | Constant filename inside an `os.MkdirTemp` directory at mode 0600. No attacker-controlled path component. |
| G115 `piper.go` — `int16(Uint16(...))` | Deliberate sign reinterpretation; that is what decoding signed PCM *is*. |
| G115 WAV header (`whispercli`, `whispercpp`) | `dataBytes` is bounded by `MaxAudio` (`maxBytes: BytesFor(MaxAudio)`); a `uint32` wrap needs ~4 GB of audio, which the bound forbids. |
| G115 `ids.go` — `uint64(UnixMilli())` | Positive for any real date. |
| G115 `piper.go` — sequence → duration | Would need a sequence above 9.2×10^18 in one call. |

**172 findings total**, largely style. Context that makes the number meaningful:
frozen, already-approved packages also have findings — **speech 47, runtime 41,
governance 41, audiointel 114**. Lint-clean is not a bar this repository
currently meets.

No unavailable tool is reported as passing. Installing them was out of scope
(this audit installs nothing).

---

## 9. Deferred risks and limitations

1. **SEC-2** accepted, not fixed — rationale in §2.
2. **No SAST ran.** All four configured/expected security tools are absent, so
   the audit is source inspection, AST analysis and behavioural testing only. A
   scanner may find issues this did not.
3. **No `-race`.** Concurrency safety rests on Tasks 14/15 behavioural tests, not
   the race detector. This is the most significant gap in the audit.
4. **Static analysis is heuristic.** The AST check catches bare identifiers and
   direct field selectors; content laundered through a helper before
   interpolation would evade it. Narrow by design — broadening it would flag
   `len(text)`, which is the remediation.
5. **Frozen phases were not audited**, only their contracts at the boundary.
6. **Privilege model unverified at runtime.** The audit proves the code makes no
   *attempt* to change privileges; it does not verify what privileges the
   deploying operator actually grants.
