# Engineering Audit

**Purpose:** the actual engineering history of Phase 11E — including the defects,
the wrong turns and the corrections. Not a tidied narrative.

The value of this document is that the verification gates **found real
production defects**. A phase whose gates find nothing has usually not been
verified; it has been asserted.

---

## 1. Production defects found and fixed

Six. Every one was found by an executable gate, reproduced before being fixed,
and guarded by a mutation-verified regression test.

### D1 — `cmd.Wait()` truncated provider output (Task 7)

`providers/process` used `cmd.StdoutPipe()` with a background reaper. os/exec
documents that *"it is incorrect to call Wait before all reads from the pipe have
completed"* — `Wait` closes the pipes. A supervisor reaps in the background by
design, so a reader draining output is always racing it, and the child's **last
bytes** lose: the tail of an utterance.

**Found:** flaky "1 frame, want 3" in a concurrent piper test.
**Fixed:** the package owns explicit `os.Pipe()`s; `Wait` cannot touch them.
**Symptom before:** silently truncated audio — reads as a bad line, not a bug.

### D2 — Barge-in reported as a broken provider (Task 7)

`Close()` on a **healthy** piper stream returned `ErrSynthesisFailed`: teardown
closes the stdout pipe under the reader, and the resulting read error was
recorded as engine failure. A router counting failures would eventually open a
circuit breaker against a working voice.

**Fixed:** a `closing()` guard distinguishes deliberate teardown from a fault.

### D3 — A hung synthesiser held the turn until the call ended (Task 14)

`pumpAudio` watched only the *session* context, so a voice that accepted text and
produced nothing could not be reclaimed by the **turn** timeout.

**MEASURED: 20.25 s → 1.05 s.** One wedged provider now costs one answer.

### D4 — A blank final transcript became a question to the model (Task 14)

A recogniser can end a stream with a final segment whose text is empty — it
believes it succeeded, so nothing upstream reports an error. The pipeline passed
it to the planner, then generation, then synthesis: **the agent would speak,
unprompted, at someone who said nothing.**

**Fixed:** a blank final is handled as silence.

### D5 — A dead engine reported with no explanation (Task 19, Gate 4)

`reap()` closed `Exited()` immediately after `cmd.Wait()`, while `drainStderr`
runs as a separate goroutine unblocked by the same event. All three subprocess
adapters wait on `Exited()` then read `StderrTail()` — racing the drain.

**Captured under load** (seed `1786859252027421000`):

```
piper: synthesis failed: the engine exited with exit status 7; stderr:
```

Empty. The one case where the engine's own words are all an operator has.

**Fixed:** `reap()` sequences the drain before declaring the child gone, so
`Exited()` means *"gone, and everything it said is captured."* The wait is
bounded (2 s) because a grandchild inheriting the handle would otherwise mean
`Exited()` never closes; on expiry it degrades to the old behaviour, not a hang.

**Deterministic repro:** `0 bytes captured` under scheduler pressure. FAIL
without the fix, PASS with it, five runs each.

### D6 — Repeated barge-in terminated the call (Task 19, Gate 4)

Not one race but a **policy asymmetry**: every FSM transition in the pipeline is
non-fatal *except* the two in `beginTurn`, and `onFrame` treated every
non-backpressure write error as unrecoverable. Repeated barge-ins tear turns
down continuously, so two ordinary races became call-terminating:

- `p.sttOpen` is read under the mutex but written to **outside** it → teardown
  closes the recogniser in between → `ErrSpeechSessionClosed` → session failed;
- the FSM can move between `onFrame`'s check and `beginTurn`'s transition →
  `ErrInvalidTransition` → same fatal path.

**Deterministic repro** (before any production change):

```
a recognition stream closing mid-turn ended the call: state=failed
err=voice: provider failed: writing audio: speech: session closed
```

**Fixed:** a closed recogniser is a counted dropped frame. A refused transition
goes through `classifyTransitionRace`, which excuses it **only when the
observable state explains it** — if the session is still in the state the
transition assumed, the refusal means the table and the code disagree and stays
fatal. Tolerating a lost race did not become tolerating everything.

**Mutation-verified, both halves:** reverting the closed-stream handling fails the
repro; blind-swallowing in the classifier fails the invariant guard.

### D7 — Model output leaked into an error message (Task 18, SEC-1)

Covered in [SECURITY_AUDIT.md](SECURITY_AUDIT.md). MEDIUM, fixed.

## 2. Defect 3 of Gate 4 — a consequence, not a separate bug

A concurrency test timed out once under full load
(`TestConcurrency_BehaviourIsDeterministicAcrossRuns/tool_denied`,
*"the run never resolved"*).

It was **not** called flaky. The classification is backed by a code-level causal
chain: `pipeline.go` returns from `runTurn` when the session context is done
**without notifying the observer**. So D6's race → `stop(StateFailed)` → context
cancelled → in-flight turn returns silently → the observer's `turnDone` never
closes → the test waits its full 40 s deadline.

Same root cause, different symptom. Both appeared only under full load, and both
vanished together after the D6 fix; five clean Gate 4 runs followed.

**Residual, not a defect:** that early return is correct for a genuine hang-up —
the call is over and nobody is waiting — but a test blocking on `turnDone` will
wait out its deadline. A harness consideration for future tests.

## 3. Test-harness defects — my own mistakes

Recorded because a bad test double costs as much as a bad implementation, and
because two of these nearly produced false conclusions about frozen code.

| # | Defect | Consequence |
|---|---|---|
| H1 | A stub TTS stream never closed its `Audio()` channel | `pumpAudio` blocked forever; looked like a pipeline hang |
| H2 | A stub incremented `inflight` before an early return, leaking the slot | 20 s "hang" that I first blamed on the frozen dispatcher |
| H3 | A benchmark measured `recordingGovernor`'s slice growth | Governance reported ~420 ns / 1.3 KB against **0 allocs** — a contradiction; truth was ~139 ns / 0 B |
| H4 | A repro test used non-yielding spinners | Starved a neighbour: `ReadinessTimeoutKillsTheChild` 250 ms → 3.4 s |
| H5 | Frame counting could not distinguish old from new turns | Barge-in tests "failed" on the new turn's legitimate audio |
| H6 | An import guard grepped file text | Failed on its own documentation, which names the forbidden paths |
| H7 | A determinism test's own load contributed to a timeout | Diagnostic added rather than the deadline raised |

**H2 produced a wrong public statement.** I wrote that `runtime.Dispatcher` does
not call `Close` on a detached sink. **That was incorrect and is corrected here:**
on a `Write` error `live[i]` stays true, so `Close` **is** called; both detach
paths close the sink. I also reverted a pipeline change I had made on that wrong
theory, because no failing test justified keeping it.

## 4. Measurement methodology corrections

| Correction | Why it mattered |
|---|---|
| Only default-`benchtime` figures are quoted | At 200 iterations, governance swung 157–466 ns (~3×) |
| Allocation stability is tracked separately from wall-clock | `B/op` was identical across repeats while `ns/op` moved — and the contradiction in H3 is what exposed a test double being measured |
| Clock granularity (~950 µs) is published | Single-shot sub-millisecond timings are reported as *"below measurable resolution"*, never as numbers |
| Lint findings compared against frozen packages | 172 findings is meaningless without knowing speech has 47 and audiointel 114 |
| Full output captured to disk for Gate 4 | An earlier `tail -8` **destroyed** a failure's seed and test name, costing a full re-run |

That last one is the most useful lesson in this document: truncating a failing
gate's output loses the only evidence that matters.

## 5. Deviations from the plan

| Planned | Actual | Why |
|---|---|---|
| Ollama "present, working, 12B model pulled" (design §4) | Daemon present, **zero models** | Environment changed; recorded in [PROVIDER_COMPATIBILITY.md](PROVIDER_COMPATIBILITY.md), design left unedited |
| Full local E2E | Stages 1–6 only | Piper absent, no LLM model — nothing fabricated |
| `-race` in the gate set | **NOT RUN** | No C compiler; recorded as an open risk, never as a pass |
| Registry as a subpackage | Placed in the `voice` root | A subpackage would have created an import cycle when the pipeline arrived |

## 6. What was deliberately not done

No second router, policy engine, interruption mechanism or state machine. No
memory writer in voice. No cloud SDK or credential handling. No model downloaded,
no binary installed. No production code changed to make a benchmark look better.
No frozen phase modified — and where a frozen contract looked insufficient, it
was reported rather than edited (both Task 17 governance findings turned out to
be the frozen engine being *correct*).

## 7. Why "production ready" is not claimed

1. **The race detector has never run.** Behavioural evidence is strong but is not
   a substitute.
2. **The complete voice loop has never executed** — no TTS runtime, no LLM model.
3. **Real provider inference is largely unmeasured** — one STT run on a `tiny`
   model is not a production characterisation.
4. **Load testing stops at 64 concurrent sessions** on a laptop.
5. **No SAST beyond `golangci-lint`**; `govulncheck` never ran.

Implementation is complete and well-verified within those bounds. That is a
different statement from approving production traffic, and the two are kept
separate in [PLATFORM_READINESS.md](PLATFORM_READINESS.md).
