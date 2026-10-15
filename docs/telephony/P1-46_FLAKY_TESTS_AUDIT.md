# P1-46 — F-9: timing-flaky `rdsauth` tests made deterministic (test-only)

**Phase 19, P1-46.** Test code only.
- **Files changed:** `services/go/telephony-gateway/internal/rdsauth/aws_test.go` and `internal/rdsauth/logging_test.go`.
- **Unchanged:** production code, dependencies, configuration, CI, manifests and ADRs.
- **Not done:** nothing staged or committed; no AWS, staging or carrier contact.

**Legend:**
- **[V] verified:** observed in this session.
- **[I] inferred.**
- **[U] unresolved.**

**Outcome:**
- **Fixed:** F-9's four tests no longer depend on scheduling. Under the CPU contention that reproduced F-9 in P1-45, they passed **150/150** (5 tests × 30), and the whole `rdsauth` package passed **140/140** (5 runs). [V]
- **Assertions still prove the intended behaviour:** each of five production-code mutations, made only in a scratch copy, was caught by the expected test. [V]
- **Not shown:** `-race` could not be run here, so this report does **not** claim the tests are race-clean. [U]

---

## 1. Root causes (from the code, confirmed by P1-45's stress evidence)

| Test | Scheduling-dependent assumption | What the product actually guarantees |
|---|---|---|
| `TestAWS_AHungSTSIsBoundedByTheDialAndDoesNotBlockTheNext` (P1-40) | A **300 ms timer** deadline would fire **after** the STS request reached the fake server. Under load it sometimes fired first: P1-45 diagnostic `handlerStarted=false requests=0`. The "server saw cancellation" signal then never fired. | When the dial ends **while** the exchange hangs: `Password` returns `DeadlineExceeded`, the hung HTTP request is cancelled (`guardedProvider` binds it with `context.AfterFunc`), and later dials are not blocked. |
| `TestAWS_SubPhasesWhenTheDialTimesOutDuringARefresh` (P1-41 §9) | The same **200 ms timer** race. If the dial ended before the cache started the provider, the outcome is legitimately `unavailable`, or nothing is recorded (`Password` had already returned). The test accepted only `refresh_incomplete`/`refresh_failed`. | A dial that ends during a **running** refresh reports `refresh_incomplete` or `refresh_failed`, never `refreshed`, and never signs. |
| `TestAWS_RunningFenceLogsSubPhasesWithoutSecrets` (P1-41 §9) | The **first** dial of a fence with a 200 ms test dial bound completes credential preparation. Under load it did not (`login_prepare` 206 ms; `refresh_incomplete`). | Every record follows the outcome rules; records of dials that obtained credentials carry both sub-phases; no secrets appear. |
| `TestAWS_SubPhasesSurviveTheGatewayLogger` (P1-43) | The same as above, for the first **two** dials (`refreshed` then `reused`, exactly 1 STS request). | The same, plus: at most one refresh succeeds, later dials reuse the cache, and no STS request follows once the cache is filled. |

None of the failures showed a product defect. In every P1-45 failure sample, `Password` still returned `DeadlineExceeded` on time.

## 2. Changes (test code only)

**Two hung-STS tests: explicit synchronisation instead of a timer race.**
- **`tripContext`:** a dial context whose deadline the test triggers. `trip()` closes `Done`, and `Err` then returns `context.DeadlineExceeded`, exactly what a timer deadline does. The code under test observes a dial's end only through `Done`/`Err` (`Password`'s `select`, `guardedProvider`'s `context.AfterFunc`, the cache's wait).
- **`hungSTS`:** the fake STS signals on channels when a request **arrives** and when that request's context is **cancelled**.
- **Procedure:**
  1. start `Password` in the background;
  2. wait (on a channel) until the fake STS holds the request, failing if `Password` returns while the dial is live;
  3. trip the deadline;
  4. assert `DeadlineExceeded`;
  5. wait on the "abandoned" channel.

  Bounded waits (10–30 s) are liveness limits only, not product timings.
- **Next dial:** a dial that starts while the abandoned refresh is still unwinding may **join** it and fail immediately. That is not blocking, so the test retries (at most 5). It **fails if any attempt hits its own deadline** (blocked) or none succeeds.
- **The timer path is still covered** by the new `TestAWS_ATimerDeadlineEndsAPasswordOnAHungSTS`: a real 300 ms timer against a hung STS must yield `DeadlineExceeded` within the liveness bound. It asserts nothing that depends on where the exchange was when the timer fired.

**Two running-fence tests: wait for observable records and check them against the outcome rules.**
- **`dialFailurePhases`** parses every `cannot open a database session` record.
- **`checkDialRecords`** applies these rules to all records in log order:
  - `login_prepare` is integer nanoseconds;
  - the outcome is one of the five values (or absent);
  - records with `refreshed`/`reused` have both sub-phases, integer and nested within `login_prepare`;
  - records without credentials were **never signed**;
  - **at most one** refresh succeeds (the fake STS issues hour-long credentials);
  - `reused` before any `refreshed` requires an earlier dial that gave up (its refresh then filled the cache).
- **`awaitCredentialedDials(n)`** polls the captured log until `n` dials obtained credentials (liveness bound 60 s), re-checking every record each time.
- **`TestAWS_RunningFence…`** waits for 1 credentialed dial; the secret-absence and not-owner assertions are unchanged.
- **`TestAWS_SubPhasesSurviveTheGatewayLogger`** waits for 2 credentialed dials (the second must be `reused`). It then requires that the STS request count does not rise while a third credentialed dial happens. This replaces the scheduling-dependent "exactly 1 request". The no-`[REDACTED]` and secret-absence checks are unchanged.

**Not changed:**
- the test dial bounds (`HeartbeatTimeout` 200 ms, interval 300 ms) and every other timing value;
- every test outside these four (plus the one added);
- all production code.

## 3. Guarantees still tested, and the proof

**Mutation checks:** the production code was mutated **only in a scratch copy** of the gateway (synced from the worktree, then restored and compared identical). The five affected tests were run against each mutation. [V]

| Mutation | Caught by (message) |
|---|---|
| M1 `guardedProvider` no longer binds the STS exchange to the dial (`context.AfterFunc` removed) | `TestAWS_AHungSTSIsBoundedByTheDialAndDoesNotBlockTheNext`: "the hung STS request was not abandoned when the dial ended" |
| M2 a refresh still running when the dial ends is reported `refreshed` | `TestAWS_SubPhasesWhenTheDialTimesOutDuringARefresh`: `outcome = "refreshed", want refresh_incomplete or refresh_failed` |
| M3 a signing duration recorded although retrieval failed | the same test: "signed after a timed-out refresh" |
| M4 credential cache removed (every dial refreshes) | `TestAWS_SubPhasesSurviveTheGatewayLogger`: "record 1: a second successful refresh of hour-long credentials" |
| M5 sign field renamed back into the redactor's reach (F-8) | the same test: `record 0 (refreshed): sub-phases missing or not integer nanoseconds: … credentials_sign:[REDACTED]` |
| baseline (no mutation) | all pass |

**Guarantees and the tests that hold them:**

| Guarantee | Held by |
|---|---|
| deadline bound | the trip and timer tests |
| abandonment | M1 |
| no false `refreshed` | M2 |
| no signing without credentials | M3, plus `checkDialRecords` |
| cache reuse | M4 |
| redaction-safe names | M5 |
| secret absence | unchanged leak lists |
| ownership (`Owned()` false without a database) | unchanged |

## 4. Results

| Run | Result [V] |
|---|---|
| 5 affected tests (4 fixed + 1 new), `-count=30`, normal load | **150/150 pass** (30 each) |
| Same tests, `-count=30`, **CPU contention** (32 busy-loop processes on 16 logical CPUs, the P1-45 reproduction; bounded by `timeout`; burners killed by a trap) | **150/150 pass** (30 each) |
| Whole `rdsauth` package, `-count=5`, **CPU contention** (P1-45: 4/5 and 3/5 failures under this load) | **140/140 pass**, 0 fail |
| Full gateway offline suite, `-p 1 -count=1 -v` | **86 pass, 0 fail, 15 skip** (top-level; +1 for the new test) + **25 subtests**; `GOWORK=off go test` ok for all 4 packages |
| Disposable PostgreSQL (pinned digest), `-p 1 -count=2` | **202 pass, 0 fail, 0 skip** + **50 subtests**; 10/10 overlap probes "no overlap"; container removed |
| gofmt / `go vet` (workspace and `GOWORK=off`) / `GOWORK=off go mod tidy -diff` / `go mod verify` | clean / clean / clean / all modules verified |
| `-race` | **not run:** `CGO_ENABLED=0` and no C compiler on PATH (`go: -race requires cgo`). Installing one is out of scope. [U] |

**Resource note:** free physical memory was low during this task (448–757 MB of 16 GB, from load outside this session). The stress runs were kept to one package at a time (`-p 1`) and stopped after the samples above; no run was interrupted. (In P1-45 the session stopped one diagnostic run for low memory.)

## 5. Scope and repository safety [V]

- **Only change in the gateway:** `internal/rdsauth/aws_test.go` (310 diff lines: the four tests above, one new test and helpers) and `internal/rdsauth/logging_test.go` (56 diff lines). Both are untracked files, so they do not appear in `git diff`. Since the task started, these are the **only** modified files anywhere in the gateway module (modification-time check against the start-of-task snapshot).
- **Production code unchanged:** `internal/rdsauth/{aws,rdsauth}.go`, `internal/fence/{fence,timing}.go`, `internal/app/{fence,iam}.go`, `go.mod`, `go.sum`.
- **Repository:** the `git status` entry set is identical to the start of the task (plus this report). 0 staged; HEAD `06e276d`; `infra/k8s/base/deployment.yaml` still ` D`.
- **Protected files:** byte-identical to their P1-31 baselines.

## 6. Remaining gaps

- **`-race`:** never run on this code; the first run would be CI (`pr-go`), pending commit and D8. [U]
- **Other timing-based tests:** they were not stress-tested here, and the P1-45 contention runs exercised `rdsauth` only. That includes `internal/fence` (controllable clock, mostly), `cmd/server` startup tests (process-level, bounded waits) and the real-PG suites. [U]
- **Go 1.25.x:** still not tested (P1-45 §3). [U]
- **Real-PG suites:** not in any CI job; the gateway is not in the nightly race job (P1-44/45). [U]

## 7. Commit plan impact

The P1-45 §5 plan is unchanged in structure. Commit 3's file list already contains `internal/rdsauth/aws_test.go` and `logging_test.go`; they now carry the F-9 fix. This report joins commit 4 (docs).

**F-9 status:** resolved in test code. The four tests are deterministic under the contention that reproduced F-9, and the mutations show they still prove the intended guarantees. A CI `-race` run remains the outstanding confirmation.

## 8. Remaining blockers (unchanged)

D10 (E1/E2 unmeasured), `prod-global`, F-3, F-4, F-7, INFRA-AUTH (A7) and A5/A6/A8/A9, D8 (`-race`, real-PG suites not in CI, nightly race excludes the gateway, lint unknown, Go 1.25 untested). Exotel AgentStream production media: **NO-GO**.

## 9. Safest next step

**Your decision:** commit in the P1-45 order:
1. platform `config.go` + `config_test.go`;
2. platform `buildinfo.go` + `buildinfo_test.go`;
3. the gateway module (now including the F-9 test fix);
4. docs, including P1-46.

Then read the first `pr-go` run: its `-race`, lint and Go 1.25.x results are the evidence this environment cannot produce. Nothing is staged or committed by this task.
