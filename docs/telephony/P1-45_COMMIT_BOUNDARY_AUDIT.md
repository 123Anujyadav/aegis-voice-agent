# P1-45 — Commit-boundary and Go-version compatibility audit

**Phase 19, P1-45.** Audit only; this report is the only file created.
- **Experiments:** all of them ran in a scratch copy of HEAD (`git archive HEAD`, extracted outside the repository).
- **Not done:** no staging, commit, push, deployment, AWS call, carrier contact, toolchain download or dependency download.
- **Not changed:** source, tests, CI, manifests and dependencies.
- **Not claimed:** **no CI run happened**, so this report claims none.

**Legend:**
- **[V] verified:** observed in this session.
- **[I] inferred:** concluded from verified facts, not executed.
- **[U] unresolved:** open or unverifiable here.

**Headline:**
1. The dependency boundary is confirmed: the platform `config.go` + `buildinfo.go` changes are required, and `buildinfo_services_test.go` must stay out. [V]
2. **Go 1.25.x was NOT tested:** no 1.25 toolchain exists locally. Only static evidence is available. [V]
3. **New finding F-9:** two `rdsauth` tests (and one more, once) fail **intermittently under CPU contention**. The cause is test timing, not the bounded behaviour. **They are a likely CI failure under `-race`, so they should be fixed before a commit.** [V failures; I CI impact]

---

## 1. Current repository state

| Item | Value | |
|---|---|---|
| Branch / HEAD | `main` / `06e276d8a78467360b9ed6d5554b445a88b94adc` | [V] |
| Staged | 0 | [V] |
| `infra/k8s/base/deployment.yaml` | ` D`, intentional, not restored | [V] |
| Status since P1-44 | `git status --porcelain=v1 -uall` identical to the snapshot taken after P1-44 (P1-45 adds only this report) | [V] |
| Protected files: `packages/go/telephony/{lifecycle,runtime}.go`, gateway `cmd/server/{main,main_test}.go` | `git diff HEAD` byte-identical to the recorded P1-31 baselines | [V] (baseline caveat as in P1-44 §1) |

**Platform changes re-verified [V]:**
- **`config.go`:** `ServiceConfig.Name` loses its `env:"SERVICE_NAME" required:"true"` tag, and `Validate` now rejects an empty `Name`.
  - All **16** HEAD `cmd/server/main.go` assign `cfg.Name = serviceName` **before** `cfg.Validate()` (checked by line order in each file).
  - No other code constructs `ServiceConfig`.
- **`config_test.go`:** 80 added lines, including `TestServiceConfig_NameIsNotReadFromTheEnvironment` and `TestServiceConfig_ValidateRejectsEmptyName`.
- **`buildinfo.go`** (153 lines): `BuildInfo`, `ReadBuildInfo`, `NewBuildInfo`, `WriteVersion`, `LogBuildInfo`. **`buildinfo_test.go`** (139 lines): 6 tests.

## 2. Dependency experiments (scratch copy of HEAD; `GOFLAGS=-mod=readonly`, `GOTOOLCHAIN=local`, `GOPROXY=off`; workspace mode unless noted)

| # | Tree | Result [V] |
|---|---|---|
| X0 | pristine HEAD | gateway build ok, tests 2 pass; platform vet ok, 19 pass |
| X1 | HEAD + gateway files only | **build fails:** `cmd\server\buildinfo.go:25: undefined: platform.BuildInfo`, `platform.ReadBuildInfo` |
| X2 | X1 + `platform/buildinfo.go` | build ok; **tests 77 pass / 8 fail / 15 skip.** 6 fail with `CS_TELEPHONY_GATEWAY_SERVICE_NAME is required but not set`: the 3 `cmd/server` startup tests and the 3 P1-43 real-logger tests. The other 2 (`TestAWS_AHungSTS…`, `TestAWS_SubPhasesWhenTheDialTimesOut…`) are the F-9 timing flakes (§4); their output was not retained. |
| X3 | X1 + `platform/config.go`, `config_test.go` (no `buildinfo.go`) | **build fails:** the same `undefined: platform.BuildInfo` |
| C1 | HEAD + commit 1 only | platform vet ok, **21 pass**; HEAD gateway builds, 2 pass; **all 45 `go.work` modules build and vet** |
| C1+C2 | + commit 2 | platform **27 pass**; HEAD gateway builds, 2 pass; **45/45 build and vet** |
| C1+C2+C3 | + commit 3 (gateway) | build ok; tests **84 pass / 1 fail / 15 skip** + 25 subtests; the one failure is `TestAWS_RunningFenceLogsSubPhasesWithoutSecrets` (F-9). `GOWORK=off`: tidy-diff, verify, vet and test all rc=0; gofmt clean. Platform 27 pass; **45/45 build and vet**. |
| + `buildinfo_services_test.go` | C1+C2+C3 plus that file | **platform test fails:** `TestBuildInfo_EveryServiceUsesTheSharedContract`: `open …\services\go\analytics\cmd\server\buildinfo.go: The system cannot find the file specified.` |

**Earlier evidence (P1-44 §4, same files):** C1+C2+C3 gave 85/0/15 in workspace mode, and the 16 services' tests passed, except `voice`'s `TestT11_RealSpeech…`, which also fails on unmodified HEAD.

**`buildinfo_services_test.go` can be excluded safely [V]:**
- It is **untracked**, so it is not an existing check at HEAD; leaving it out weakens nothing that CI runs today.
- Including it without all 16 services' `cmd/server/buildinfo*.go` (all untracked) fails the platform module.
- It belongs with the owner's full cross-service build-info commit.

## 3. Go-version compatibility

**Supported version [V]:**
- `GO_VERSION: "1.25.x"` in `ci.yml`, `pr-go.yml` and `hardening.yml`, both at HEAD and in the worktree, and in the untracked `telephony-integration.yml`.
- `go.work`: `go 1.25.0`. Gateway `go.mod`: `go 1.25.0`.
- Image base: `docker/go/Dockerfile` is `golang:1.25.0-alpine3.21` in the worktree but **`golang:1.23.5-alpine3.21` at HEAD**. Images are not built by any committed CI job, and the Dockerfile is outside the commit scope.

**Toolchain availability [V]:**
- The only Go installed is **go1.26.5** (`C:\Users\abhik\go-sdk\Go`; a second copy under `.omnirush` is also 1.26.5).
- There is no `golang.org/toolchain` module in the cache (only a placeholder lock), and no `golang` Docker image.
- Getting 1.25 would need a download, which is forbidden here. **So the code was NOT built or tested with Go 1.25.x.**

**Static evidence:**

| Check | Result |
|---|---|
| Module versions | every module in the gateway's build list declares `go` ≤ **1.25.0**. The highest is `jackc/pgx/v5 v5.10.0` (1.25.0); aws-sdk-go-v2 and smithy-go are 1.24; `x/sync` and `x/text` are 1.24.0. So no dependency requires a newer toolchain. [V] |
| Language features | the 1.26.5 compiler compiles each package at the module's language version (`go 1.25.0`), and the build succeeds. [I: relies on the toolchain's documented `-lang` behaviour] |
| Standard-library symbols | `go vet`'s `stdversion` analyser is active for this module. A scratch-only probe using `errors.AsType` (added in Go 1.26) was reported: `errors.AsType requires go1.26 or later (file is go1.25)`. Vet is clean on the real code. So no post-1.25 standard-library API is used. [V] |
| The local `aegis-go-telephony-gateway:smoke` image | its binary reports **go1.25.0**, but its module list has only `platform`: **no pgx, no AWS SDK.** It predates the fence and IAM code, so it is **not** evidence for the current code. Read with `go version -m` from a container that was created, never started, then removed. [V] |
| **Not covered** | runtime or standard-library behaviour differences between 1.25 and 1.26, differences in vet analysers, and timing under 1.25. **[U]: compatibility is not claimed.** |

## 4. F-9 (new): timing-sensitive `rdsauth` tests are flaky under CPU contention

| Test (origin) | Observed failures [V] | Cause |
|---|---|---|
| `TestAWS_RunningFenceLogsSubPhasesWithoutSecrets` (P1-41 §9) | C1+C2+C3 run: 1/1. Under CPU burners: **4/5**, then 2 of a partial run. 0/5 with `-cpu=1` and 0/5 in a normal 5× run. | The test uses a 200 ms **test** dial timeout and asserts sub-phases on the **first** dial record. Under load the first preparation took longer (`login_prepare` 206 ms observed), so it ended as `refresh_incomplete` or without sub-phases. This is the documented "timed-out dial" limitation; **the test assumes it cannot happen.** |
| `TestAWS_AHungSTSIsBoundedByTheDialAndDoesNotBlockTheNext` (P1-40 A2/A3) | X2: 1. Under burners: **3/5**, then 1 of 2. | **Scratch-only diagnostic:** of 3 samples under load, one had `deadlineErr=true returnedAfter=303ms handlerStarted=false requests=0`. The 300 ms dial ended **before the STS request reached the fake server**, so the test's "server saw cancellation" signal never fired. In every sample, `Password` returned `DeadlineExceeded` on time (≤ 440 ms). **No bound violation was observed.** The diagnostic run was then stopped by the session for low system memory and was not restarted, so the sample is small. |
| `TestAWS_SubPhasesWhenTheDialTimesOutDuringARefresh` (P1-41 §9) | X2: 1 (output not retained); passed in the later runs | probably the same class [I] |
| `TestAWS_SubPhasesSurviveTheGatewayLogger` (P1-43) | not observed failing (it runs non-parallel) | same 200 ms design: **at risk** [I] |

**Impact [I]:** CI runs `go test -race`, which slows execution substantially, on shared runners. These tests are therefore likely to fail **intermittently** in `pr-go`, and a red first CI run would obscure real results.

**Recommended fix (test-only; not done here; needs your go-ahead as a code change):**
- assert sub-phases on the first record **that has them**, or accept the documented `refresh_incomplete` / absent case;
- in the hung-STS test, wait until the fake server has received the request before letting the dial deadline matter. For example, start the dial and let the deadline elapse only after `requests ≥ 1`, or assert the bound (`DeadlineExceeded` within the limit) separately from the cancellation signal.

The tests' safety intent must be kept, with no product-code change.

## 5. Proposed commits (exact files; nothing staged or committed)

| # | Files | Passes independently on its predecessor? [V] |
|---|---|---|
| **1** platform: `Name` from the binary, not the environment | `packages/go/platform/config.go`, `packages/go/platform/config_test.go` | **yes:** platform vet and 21 tests pass; HEAD gateway builds and tests; 45/45 build and vet |
| **2** platform: build-info API (gateway subset) | `packages/go/platform/buildinfo.go`, `packages/go/platform/buildinfo_test.go` | **yes** on 1: platform 27 pass; 45/45 build and vet |
| **3** telephony-gateway: fence, RDS IAM, phase timing (F-6, F-8) | `services/go/telephony-gateway/go.mod`, `go.sum`; `cmd/server/main.go`*, `cmd/server/main_test.go`*, `cmd/server/buildinfo.go`, `cmd/server/buildinfo_test.go`, `cmd/server/buildinfo_hooks_test.go`, `cmd/server/fence_startup_test.go`; `internal/app/{fence.go, fence_test.go, fence_postgres_test.go, iam.go, iam_test.go}`; `internal/fence/{fence.go, timing.go, audit_test.go, audit_postgres_test.go, credentials_test.go, credentials_postgres_test.go, fence_test.go, fence_postgres_test.go, logging_test.go, timing_test.go, timing_postgres_test.go, tls_test.go}`; `internal/rdsauth/{rdsauth.go, aws.go, rdsauth_test.go, aws_test.go, logging_test.go}` | **only on 1+2:** build, vet, `GOWORK=off` tidy, verify and test pass. The workspace test run is green **except F-9 flakes**, so **fix F-9 first** |
| **4** docs | `docs/telephony/P1-40_A1_DECISIONS_AND_IMPLEMENTATION.md`, `P1-41_INFRA_AUTH_READINESS_AUDIT.md`, `P1-42_STAGING_D10_MEASUREMENT_RUNBOOK.md`, `P1-44_PRECOMMIT_CI_READINESS_AUDIT.md`, `P1-45_COMMIT_BOUNDARY_AUDIT.md`; `tools/scripts/fence-validation/readiness-overlap-monitor.sh` | independent (no Go) |

\* Protected, user-owned. Commit 3 cannot build without the `main.go` edits (`app.WireFence`, `buildInfo`). The `main_test.go` diff is a trailing-newline change only.

**Kept out (no dependency evidence requires them) [V]:**
- `go.work` (HEAD already lists the gateway and platform; 45/45 build without the worktree edit);
- workflows, manifests, `configmap.yaml`, Dockerfiles, `Taskfile.yml`;
- other services and the other untracked platform tests;
- `buildinfo_services_test.go` (§2);
- `infra/k8s/base/deployment.yaml` stays **deleted** and is not part of any commit.

**Alternative:** you may commit the full 16-service build-info contract instead of commit 2's subset. That larger set was **not** simulated. [U]

## 6. CI coverage and gaps (committed workflows; unchanged from P1-44 §7)

| Coverage | |
|---|---|
| `pr-go` build, vet, `go test -race -count=1`, lint; `ci.yml` build and test | include the gateway (discovered from `go.work`) [V] |
| Real-PostgreSQL suites (15 tests) | skipped in every committed workflow (`CS_TELEPHONY_GATEWAY_FENCE_TEST_DSN` unset) [V] |
| Nightly `hardening.yml` race | excludes the gateway (hard-coded list) [V] |
| `-race`, golangci-lint, Go 1.25 compile | never run on this code [U] |
| F-9 | likely intermittent CI failure until fixed [I] |

## 7. Security and test limitations

- **Security:** no new security finding in this pass. The P1-44 §3 review stands, and the experiments changed no product code.
- **Offline only:**
  - the STS is a local fake;
  - TLS and PostgreSQL are local, and real PG runs only in the disposable container (last full run: P1-44 §5, 200/0/0, 10/10 "no overlap");
  - there is no AWS, Aurora, IRSA or staging evidence.
- **Under load:** the F-9 diagnostic sample is small (3 samples; the run was stopped for low system memory and not restarted).

## 8. Remaining blockers

| Item | State |
|---|---|
| **F-9** flaky timing tests | **new**; test-only fix recommended before commit 3 |
| **D10** | unresolved; E1/E2 unmeasured |
| **`prod-global`** | unresolved |
| **F-3** early refresh | D10-dependent |
| **F-4** `HTTPS_PROXY` | owner decision |
| **F-7** two-replica RollingUpdate manifest vs A/B `Recreate` | A5 |
| **INFRA-AUTH (A7)** and A5/A6/A8/A9, SEP | unapproved |
| **D8** CI | `-race` never run; real-PG suites not in CI; gateway not in nightly race; lint unknown; **Go 1.25 untested** |
| Exotel AgentStream production media | **NO-GO** |

## 9. Safest next step

**Fix F-9 in the tests only:** make the two (and the two at-risk) `rdsauth` tests robust to a slow first dial without weakening what they prove. Then verify under the same CPU-contention stress used here.

After that, the commit order 1 → 2 → 3 → 4 is ready for the owner. The first `pr-go` run then gives the first real `-race`, lint and Go 1.25.x results.
