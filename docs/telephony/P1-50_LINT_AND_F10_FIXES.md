# P1-50 — Lint cleanup (57 new findings) and F-10 test stabilisation

**Phase 19, P1-50.**
- **Branch:** `telephony/p1-fence-rds-iam` at `8cbfeda`, history untouched (`0b6172a`, `105c2e5`, `3f3e1aa`, `8cbfeda`).
- **State of the changes:** **unstaged and uncommitted**.
- **Not done:** no push, PR, deployment, AWS or carrier contact; the only database used was a disposable local container.

**Legend:**
- **[V] verified:** observed in this session.
- **[U] unavailable or unverified.**

**Final status: READY FOR REVIEW.** *(Audited and amended in P1-51: see `P1-51_FINAL_DIFF_AUDIT.md`.)*
- All 57 new lint findings are resolved: gateway **0**; `platform` back to exactly its 16 baseline findings.
- **No runtime behaviour change:** the refactors were proven output-identical on a 101-case golden corpus, and the `pg_locks` key split by a unit test and the real-PostgreSQL suite.
- **F-10 is deterministic:** the test passed 20/20 alone and 5/5 within the package under CPU saturation, and three mutations show it still proves its guarantees.
- **Still unrun here:** `-race` and Go 1.25 (§6).

---

## 1. Files changed (15; all previously committed files; nothing staged) [V]

| File | Change | Rationale |
|---|---|---|
| `internal/app/fence.go` | `LoadFence` split into `readFenceVariables`, `requiredFenceProblems`, `whitespaceProblems`, `authModeProblems`, `parseFenceTimings`, `fenceConnection`; `err` shadowing renamed (`serr`, `cerr`); IRSA `Validate` result checked directly; doc comments on `notConfigured.Name`/`Check`; 2 targeted `nolint` (§3) | gocyclo 33, govet `shadow` ×4, revive ×2, gosec G304, errcheck. **The same checks in the same order with the same messages** (golden-verified, §4.2). |
| `internal/app/iam.go` | `checkIAMEnvironment` split (`environmentRegionProblems`, `awsRegionProblems`, `setVariables`, `irsaProblems`); `validateIAMDSN` split (`dsnUserProblems`, `dsnHostPort`, `dsnQueryProblems`, `dsnQueryKeyProblems`, `dsnTLSProblems`); comment wording; 2 targeted `nolint` | gocyclo 20 and 35, nestif, godot, G304, errcheck. Golden-verified. |
| `internal/fence/fence.go` | `Run`: `_ = f.sleep(…)` → `if f.sleep(…) != nil { break }`. `release(ctx, sess)` uses `context.WithTimeout(context.WithoutCancel(ctx), T)`. Key split moved to `advisoryKeyParts` (bit operations, no integer conversions). Doc comments. 2 targeted `nolint` on the best-effort unlock and close. | errcheck, contextcheck, G115 ×2, revive ×5, godot ×2 |
| `internal/fence/timing.go` | outcome-constant comments in revive form; doc comments for `NewTimings`/`WithTimings`; `timingsFrom` reads the comma-ok `ok`; comment wording; 1 `nolint` (G101 on a field **name**) | revive ×7, errcheck, godot, G101 |
| `internal/rdsauth/aws.go` | doc comments for `tokenFile.GetIdentityToken` and `guardedProvider.Retrieve`; comma-ok `marked` read; comment wording; 1 `nolint` (contextcheck on the R5 dial binding) | revive ×2, errcheck, godot, contextcheck |
| `internal/rdsauth/rdsauth.go` | 2 `nolint` (G101 on the reason **labels** `sts_invalid_identity_token`, `sts_expired_token`) | gosec G101 false positives |
| `cmd/server/fence_startup_test.go`, `internal/app/fence_test.go` | listeners collected and closed by one deferred loop (same lifetime: until the helper returns) | gocritic `deferInLoop` |
| `internal/app/fence_test.go`, `internal/fence/fence_test.go` | `http.NoBody` for the readiness requests | gocritic `httpNoBody` |
| `internal/app/iam_test.go` | `iamDSNWith(ca, extra string)` | gocritic `paramTypeCombine` |
| `internal/fence/credentials_test.go`, `timing_test.go`, `tls_test.go` | new helper `sameError(got, want)`: `errors.Is` **and** identical `Error()` text. Replaces three `!=` comparisons, keeping their exactness (`redact` must return the bare category, never a wrapper with details). | errorlint ×3, without weakening |
| `internal/fence/fence_test.go` | **new test** `TestAdvisoryKeyParts_MatchesTheUnsignedSplit` (144 keys incl. 0, ±1, ±2^32, `MinInt64`, `MaxInt64`, every ±2^i) against the former `uint64` formula, plus the oid range | proves the G115 fix is exact |
| `internal/rdsauth/aws_test.go` | **F-10 fix** (§5). `startPassword(ctx, s)` parameter order. `refreshed++` out of the `if` init. The P1-46 hung-STS paragraph separated from the `tripContext` doc comment it had been glued to; godot. | F-10; revive `context-as-argument`; gocritic `initClause`; godot |
| `packages/go/platform/config_test.go` | the test's doc comment is now a sentence, not a bare name | gocritic `docStub` (the only new platform finding) |

**Not touched:**
- the four protected files (byte-identical to their P1-31 baselines; SHA-256 unchanged);
- `go.mod`/`go.sum`, workflows, manifests, `.golangci.yml`, ADRs;
- every other `platform` file.

## 2. Lint: before and after (golangci-lint **v1.64.8** = the version `pr-go.yml` installs; the repository `.golangci.yml`; `golangci-lint run --timeout=10m` per module, as CI)

| Module | Base `06e276d` | Branch `8cbfeda` (before) | **After P1-50** | New (branch-introduced) before → after |
|---|---|---|---|---|
| `services/go/telephony-gateway` | 1 (old `main.go`; fixed by commit 3) | 56 | **0** (exit 0) | 56 → **0** |
| `packages/go/platform` (clean HEAD export + this change) | 16 | 17 | **16** | 1 → **0** |

- **Gateway** was linted in the worktree, which equals HEAD plus these edits; the gateway has no untracked files.
- **`platform`** was linted on a `git archive HEAD` export with only `config_test.go` overlaid, because the worktree's untracked user test files there would distort it.
- **The 16 remaining `platform` findings are the pre-existing baseline**, identical to `06e276d` by file and message (line-independent comparison): `config.go` 2, `health.go` 2, `logging.go` 4, `metrics_route_test.go` 7, `server.go` 1. They were not touched, hidden or excluded. **`platform`'s lint job therefore stays red until that debt is addressed separately.**
- **No linter disabled, no rule weakened, no exclusion added, no blanket `nolint`.**

## 3. The 10 `nolint` directives (each names one linter and its reason) [V]

| Location | Linter | Reason |
|---|---|---|
| `internal/app/fence.go:309` | gosec G304 | opening the operator-configured DSN file is the purpose |
| `internal/app/fence.go:318` | errcheck | `defer f.Close()` on a read-only file already fully read |
| `internal/app/iam.go:322` | gosec G304 | probing the operator-configured CA bundle or IRSA token file is the purpose |
| `internal/app/iam.go:326` | errcheck | `defer f.Close()` of a read-only probe |
| `internal/fence/fence.go:338` | errcheck | best-effort `Unlock` **after** `lose()`: ownership is already dropped (fail closed; unchanged) |
| `internal/fence/fence.go:339` | errcheck | best-effort `Close`: an unreachable session is released by the server (unchanged) |
| `internal/fence/timing.go:76` | gosec G101 | `login_prepare_sts` is a log-field **name** |
| `internal/rdsauth/aws.go:228` | contextcheck | R5: the STS refresh must end with the **dial's** context, not the cache's suppressed one |
| `internal/rdsauth/rdsauth.go:46,47` | gosec G101 | reason **labels** containing "token" |

Every other finding was fixed in code.

## 4. Behaviour preservation

**4.1 Semantics-sensitive changes:**
- **`release`:** the context is still fresh for cancellation (`WithoutCancel`) and still bounded by `HeartbeatTimeout`. It now also inherits ctx's values, which pgx does not read. Unlock and close errors are still deliberately ignored after ownership is dropped.
- **`Run`:** `sleep` returns an error only when ctx has ended, which already ends the loop; `break` is equivalent.
- **Comma-ok reads:** `timingsFrom` returns nil exactly when the value is absent; `guardedProvider` binds the dial exactly when a marker is present.
- **Key split:** `advisoryKeyParts` equals the former `uint64` split for 144 keys (new unit test) and on a real server (`TestPostgresFence_KeysAcrossTheInt64RangeAreConfirmed`, passed ×2, §6).

**4.2 Golden equivalence of the refactored validators [V].** A scratch-only harness (never in the repository) recorded, for a fixed corpus, the exact outputs of:
- `validateIAMDSN`: 31 DSNs (users, passwords, multi-host, ports, databases, fragments, every query rule, CA-file cases);
- `checkIAMEnvironment`: 11 environments × 3 regions;
- `LoadFence`: 27 configurations, including the IAM success path with a real CA, IRSA identity checks and password mode;
- `readDSNText`: 10 files.

The 101 records were byte-identical before and after the refactor, and deterministic across repeated runs. **Sensitivity check:** swapping just two problem groups in a scratch copy changed 4 records, so the harness detects reordering. The scratch copy was then restored identical.

## 5. F-10: `TestAWS_TokensReachPostgresOnlyOverVerifiedTLS`

**Reproduction:**
- **P1-49 [V]:** 1/5 package runs and 1/10 isolated runs failed under 32 busy-loops. The diagnostic showed the first dial hitting the **harness's** 5 s bound (`context deadline exceeded`) after connecting but before sending a token.
- **This session, before editing:** not reproduced in 15 isolated runs nor 5 package runs under the same load. The failure is intermittent.

**Fix (test only):**
1. **Trusted server:** every dial must end with either the server's refusal (`*pgconn.PgError` SQLSTATE `28P01`, so the token reached PostgreSQL over verified TLS) or the harness deadline (`DeadlineExceeded`, fail closed). **Any other outcome fails.** Dials repeat, at most 8, until **two dials were refused**, with the SigV4 1.1 s spacing. The server answers only after recording the token, so a refusal proves that dial's token arrived. *(Amended in the P1-51 audit: the first version stopped at two **recorded tokens**, which could flake when a starved client sent its token but hit the harness deadline before reading the refusal.)*
2. **Assertions on the trusted server:**
   - at most one token per connection (passwords are read before connections, which makes it a strict invariant);
   - at least one recorded token per refused dial;
   - every token parses for the right host and user;
   - all tokens are distinct.
3. **Untrusted server:** the test requires a dial that **reached certificate verification** (`*tls.CertificateVerificationError`). A dial that only hit the harness deadline is retried (bounded). Any other error fails. **Zero passwords** is checked after every attempt, and the server must have seen a connection. **The test therefore cannot pass without exercising TLS verification.**

**Mutation checks** (production code mutated only in a scratch copy, then restored identical) [V]:

| Mutation | Result |
|---|---|
| baseline | pass |
| M-a TLS verification disabled on the dial (`InsecureSkipVerify`) | **FAIL**: "untrusted server: neither a verification failure nor the harness deadline" (the untrusted server received a login) |
| M-b one token reused for every connection | **FAIL**: "a connection reused an earlier token" |
| M-c every dial ends at the deadline without reaching the server | **FAIL**: "only 0 of 8 dials were refused by the server (8 at the harness deadline, 0 tokens recorded)", **not** a vacuous pass (message as re-run after the P1-51 amendment) |

**Stress [V]:**

| Run | Result |
|---|---|
| normal load, `-count=10` | 10/10 |
| 32 busy-loops, isolated, `-count=20` (bounded; trap cleanup) | **20/20** |
| 32 busy-loops, whole `rdsauth` package, `-count=5` | F-10 **5/5**; F-9 tests 5/5 each (`AHungSTS…`, `ATimerDeadline…`, `…WhenTheDialTimesOut…`, `RunningFence…`, `SubPhasesSurvive…`) |

## 6. Validation (commands as run; `GOFLAGS=-mod=readonly`, `GOTOOLCHAIN=local`, `GOPROXY=off`, Go 1.26.5) [V]

| Check | Result |
|---|---|
| `golangci-lint run --timeout=10m` (gateway / platform export) | **0** / **16** (= baseline) |
| `gofmt -l` (gateway; platform) | empty; empty |
| `go build ./...`; `go vet ./...`; `GOWORK=off go vet ./...` (gateway) | ok; ok; ok |
| `GOWORK=off go mod tidy -diff`; `GOWORK=off go mod verify` | clean; all modules verified |
| gateway `go test -p 1 -count=1 -v ./...` | **87 pass, 0 fail, 15 skip** (+1: the key-split test) + **25 subtests** |
| gateway `GOWORK=off go test -p 1 -count=1 ./...` | ok ×4 packages |
| platform (`git archive HEAD` + `config_test.go`) `go vet`, `go test -count=1 -v ./...` | ok; **27 pass, 0 fail** + 14 subtests |
| Disposable PostgreSQL (pinned `postgres@sha256:7e5d…`, `--pull never`, loopback, one-off password, trap removal), `go test -v -p 1 -count=2 ./...` | **204 pass, 0 fail, 0 skip** + **50 subtests**; 10/10 "no overlap"; container removed |
| `-race` | **not run:** `CGO_ENABLED=0`, no C compiler [U] |
| Go 1.25.x | **not run:** only go1.26.5 is available; nothing downloaded [U] |
| CI | **not run** (nothing pushed) [U] |

## 7. Remaining failures and limitations

**F-11 (new observation; pre-existing; not fixed).** Under the 32-busy-loop load with only about 500–800 MB of free memory, these `rdsauth` tests can exceed their **harness** bounds (20 s or 2 s; the failing subtests ran 20–29 s while the SDK retried):
- `TestAWS_STSAndCredentialFailuresFailClosedWithoutLeaking` (several subtests);
- `TestAWS_SubPhasesOnRefreshFailures`;
- `TestSource_FailureThroughTheFenceDialerIsWithheldAndNeverConnects`;
- and, before the edits, `TestAWS_CachesSTSCredentials…` and `TestAWS_TheReasonReachesTheFenceDialer`.

**They failed the same way before any P1-50 edit** (the pre-edit package run), so they are not caused by this change. Every result was a fail-closed `DeadlineExceeded`. They pass in every normal run, and they passed in P1-46/P1-49 contention runs on a less memory-starved host.
- **Proposed follow-up (test-only, separate):** the same "harness deadline is not the product's" treatment as F-9/F-10, if CI shows it.
- **The P1-50 status is not blocked by it:** it is outside this task's findings and is not a product defect.

**Other limits:**
- **Unverified here:** `-race`, Go 1.25.x and CI.
- **Lint toolchain:** the local golangci-lint binary was built with go1.26.5, while CI builds v1.64.8 with 1.25.x; minor differences are possible [U].
- **`platform` lint:** the job remains red on its 16 pre-existing findings.
- **Unchanged blockers:** D10, `prod-global`, F-3, F-4, F-7, INFRA-AUTH (A5–A9) and D8 (nightly race, PG-in-CI: P1-49 §3). Exotel AgentStream production media stays **NO-GO** until its mTLS blocker is resolved.

## 8. Repository safety [V]

- **Status:** the entry set equals the pre-task snapshot plus exactly the 15 files of §1 (now ` M`) and this report.
- **Unchanged:** 0 staged; HEAD `8cbfeda`; no upstream; `infra/k8s/base/deployment.yaml` still ` D`.
- **Protected files:** byte-identical to their baselines; SHA-256 unchanged.
- **Scratch only:** all experiments (the golden harness, mutations, lint exports) ran in the scratchpad.

## 9. Final status: **READY FOR REVIEW**

**Next step (yours):** review the 15-file diff. If accepted, commit it as **one** follow-up commit on the branch (`git add` of exactly those 15 paths plus this report). Then the PR's `pr-go` run supplies the first `-race`, Go 1.25.x and CI-lint evidence.
