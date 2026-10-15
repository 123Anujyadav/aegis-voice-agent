# P1-44 — Pre-commit audit and CI readiness: gateway IAM work (F-6, F-8)

**Phase 19, P1-44.** A read-only audit; this report is the only file created.
- **Not done:** no staging, commit, push, deployment, migration, AWS call or carrier contact.
- **Not changed:** source, tests, dependencies, manifests, CI and ADRs.
- **Not claimed:** **no CI run was performed or authorised**, so this report claims no CI result.

**Legend:**
- **[V] verified:** observed in this session by a command or by reading the file.
- **[I] inferred:** a conclusion drawn from verified facts but not executed.
- **[U] unresolved:** an open decision or an unverifiable item.

---

## 1. Repository state (2026-10-15)

| Item | Value | |
|---|---|---|
| Branch / HEAD | `main` / `06e276d8a78467360b9ed6d5554b445a88b94adc` | [V] |
| Staged files | 0 | [V] |
| `infra/k8s/base/deployment.yaml` | ` D` (deleted, intentionally; not restored) | [V] |
| Protected files: `packages/go/telephony/{lifecycle,runtime}.go`, gateway `cmd/server/{main,main_test}.go` | `git diff HEAD -- <file>` is byte-identical to the baseline patches recorded at P1-31 (`cmp` on all four) | [V] |
| Worktree size | 601 status entries (tracked modifications, deletions, untracked files), almost all unrelated to this work | [V] |

The protected-file baseline is the recorded diff, not file hashes from before P1-31. Edits made before that baseline was taken cannot be ruled out by this check. [U]

## 2. Change inventory relevant to the gateway IAM work

### 2.1 Gateway module `services/go/telephony-gateway`

**Tracked in HEAD:** only `cmd/server/main.go`, `cmd/server/main_test.go` and `go.mod`. [V]

| Path | Status | Lines | Notes |
|---|---|---|---|
| `go.mod` | M | — | `go 1.23.0` → `1.25.0`; adds aws-sdk-go-v2 v1.47.1, `credentials` v1.20.6, `feature/rds/auth` v1.7.4, `service/sts` v1.51.1, pgx/v5 v5.10.0 and 11 indirect modules [V] |
| `go.sum` | **??** | 46 | **untracked**; required by the `go.mod` above [V] |
| `cmd/server/main.go` | M (protected, user-owned) | — | `version` command, build info, `app.WireFence` call [V] |
| `cmd/server/main_test.go` | M (protected) | — | 1 line changed [V] |
| `cmd/server/buildinfo.go`, `buildinfo_test.go`, `buildinfo_hooks_test.go` | ?? | 32 / 164 / 47 | part of a cross-service build-info contract (§2.2) [V] |
| `cmd/server/fence_startup_test.go` | ?? | 194 | binary-level startup tests; one real-PG test [V] |
| `internal/app/{fence,iam}.go` + `fence_test.go`, `iam_test.go`, `fence_postgres_test.go` | ?? | 368, 335 + 213, 581, 182 | configuration, IAM static rules, wiring [V] |
| `internal/fence/{fence,timing}.go` + 10 test files (incl. `logging_test.go` from P1-43) | ?? | 553, 311 + 2,268 | fence, timing, TLS, audit, real-PG suites [V] |
| `internal/rdsauth/{rdsauth,aws}.go` + `rdsauth_test.go`, `aws_test.go`, `logging_test.go` | ?? | 184, 249 + 288, 832, 102 | IRSA → STS → RDS token [V] |

### 2.2 Dependencies outside the module (found by simulating the commit, §4)

| Path | Status | Why the gateway needs it |
|---|---|---|
| `packages/go/platform/buildinfo.go` | ?? (user-owned) | `cmd/server/buildinfo.go` uses `platform.BuildInfo`, `ReadBuildInfo` and `LogBuildInfo`. Without it: **build fails** [V] |
| `packages/go/platform/config.go` (+ `config_test.go`) | M (user-owned) | HEAD's `ServiceConfig` requires `CS_<SERVICE>_SERVICE_NAME`. Without the change: **6 gateway tests fail**, including all three P1-43 real-logger tests and three `cmd/server` startup tests [V] |
| `packages/go/platform/buildinfo_services_test.go` | ?? (user-owned) | **must not be committed with the gateway alone.** It checks that **all 16** services carry identical `cmd/server/buildinfo*.go`, and those are untracked in every service. Committing it early would fail the platform module [I: from its source; not executed in isolation] |
| Other untracked platform tests (`buildinfo_test.go`, `health_test.go`, `httprunner_test.go`, `logging_test.go`, `server_test.go`) | ?? | not needed by the gateway; not audited here |

### 2.3 Documentation and tools

| Path | Status |
|---|---|
| `docs/telephony/P1-40_…`, `P1-41_…`, `P1-42_…`, this report | ?? |
| `docs/telephony/P1-10_…` through `P1-39_…` and other telephony docs | ?? (earlier phases; owner's grouping) |
| `tools/scripts/fence-validation/readiness-overlap-monitor.sh` | ?? (P1-32/33 scaffold) |

### 2.4 Related but outside the commit scope

These are not needed for the gateway build. They are the owner's work and are listed so they are not committed by accident:
- `go.work` (M): adds `accesstoken`, `dpop`, `pgshared`, `telephonyredis` and `telephonystore`. HEAD's `go.work` already lists the gateway. [V]
- Workflows:
  - `ci.yml`, `pr-go.yml`, `pr-python.yml` (M);
  - five untracked workflows, including `telephony-integration.yml`, which does not reference the gateway. [V]
- `infra/k8s/base/services/telephony-gateway.yaml` (??; F-7), `configmap.yaml` (M), `docker/go/Dockerfile` (M), `Taskfile.yml` (M).

## 3. Security review (gateway module as one change set)

No regression or new defect was found. Each row cites the evidence read in this session.

| Area | Finding | Evidence |
|---|---|---|
| Mode selection, fail closed | Unconfigured → starts but never ready. Partial or invalid configuration → startup error. The auth mode must be exactly `iam` or `password`; there is no default. | `internal/app/fence.go` `LoadFence`, `WireFence` (lines 128-222, 319-368) [V] |
| Password mode | Only with `CS_TELEPHONY_GATEWAY_ENVIRONMENT` explicitly `development` (R2). The region must be absent (M3). | `fence.go` LoadFence [V] |
| IAM mode: no fallback | IAM never falls back; a nil adapter → `ErrIAMAdapterUnavailable` (R6). | `app/fence.go:339-340`, `iam.go` [V] |
| Static AWS keys | `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` and `AWS_SESSION_TOKEN` refused at startup. The STS client is built with `sts.New`, credentials nil, no shared config. | `iam.go` `staticAWSCredentials`; `rdsauth/aws.go` `newAWS` [V] |
| Environment and region | Explicit `staging`/`production` only. Region allow-list (staging: `ap-south-1`; production: `ap-south-1`, `ap-south-2`). `AWS_REGION`/`AWS_DEFAULT_REGION` must match. 24 `PG*` variables refused. | `iam.go:42-149` [V]. Whether the production list is final, and `prod-global`, are **[U]** (comment in `iam.go` says so) |
| DSN, TLS | Raw and parsed DSN validation; verify-full, explicit CA, query allow-list; DSN file size and line checks; errors never echo values. | `iam.go` `validateIAMDSN`/`validateIAMParsed`; `fence.go` `readDSNText`; `internal/fence/tls_test.go` [V] (re-run; not re-read line by line) |
| Writer endpoint | `<cluster>.cluster-<id>.<region>.rds.amazonaws.com` required (C1/C3); host never echoed. | `fence.go` `readIAMDSN` → `isWriterEndpoint` [V] |
| Token generation | A fresh token per dial; STS credentials cached until expiry (`ExpiryWindow=0`, F-3 **[U]**). Incomplete or expired credentials refused. | `aws.go` `guardedProvider` (lines 216-249) [V] |
| Timeouts | The dial context (T) bounds STS (re-bound inside the cache via `retrieval.dial`), signing and connect. `Source.Password` returns at `ctx.Done()`; a late builder's result is discarded. | `aws.go:219-227`, `rdsauth.go:141-184` [V] |
| Panic recovery | Provider panic → `errMalformedCredentials`; builder panic → `token_builder_panic`. | `aws.go` defer/recover, `rdsauth.go:156-159` [V] |
| Sanitised errors | Fixed reason vocabulary from error types and AWS codes only. `redact` emits only context errors, SQLSTATE, TLS/network/credential categories, or a fixed message. | `aws.go` `classify`; `fence.go:517-546` [V] |
| Ownership, readiness, heartbeat | Unchanged since P1-41 §8 (timing is measurement only). Real-PG overlap probes 10/10 "no overlap" (§5). | `fence.go`; §5 [V] |
| F-8 field names | `login_prepare`, `login_prepare_sts`, `login_prepare_sign`, `sts_refresh` | `timing.go:72-91` [V] |
| All gateway log keys vs the platform redactor | Every attribute key in non-test gateway code (`acquisition_grace`, `auth_mode`, `error`, `grace`, `heartbeat_interval`, `heartbeat_timeout`, `lock_key`, `lock_missing`, `missing`, `phase`, plus the 12 phase names and `sts_refresh`) was checked against all deny-list and pseudonymised substrings in `platform/logging.go`: **no collision**, so nothing else is silently blanked. | scripted check [V] |

**Observations (no action proposed in this audit):**
- **OBS-1:** a builder still running after its dial timed out finishes in the background and may write durations into that attempt's collector after the line was logged. That is harmless: the collector is per attempt and is not reused. [V by reading]
- **OBS-2:** `classify` maps any `net.Error` to `sts_unreachable`, and a context deadline also satisfies `net.Error`. In practice it is masked: `credentialDialer` returns the context error first when the dial context has ended (`fence.go:448-451`). [V by reading]
- **OBS-3:** P1-41 §3.1 and §2.1 cite line numbers from before the §9 and P1-43 edits; some are now stale. P1-42's references were re-checked at P1-43. This is documentation only. [V for P1-42; P1-41 not re-checked line by line]
- **OBS-4:** local verification used **Go 1.26.5**; CI pins **1.25.x**. `go vet` passed, and its `stdversion` analyser reports standard-library symbols newer than the module's `go 1.25.0` directive, but a compile under 1.25 was not run. [V vet; U compile]

## 4. Commit-set simulation (offline, outside the repository)

**Method:** `git archive HEAD` was extracted into the session scratchpad and the candidate files were copied on top. Go ran with `GOFLAGS=-mod=readonly`, `GOTOOLCHAIN=local` and `GOPROXY=off`, in **workspace mode** (as `pr-go.yml` runs) and with `GOWORK=off`. The repository was not modified. [V]

| Candidate set on top of HEAD | Result |
|---|---|
| Gateway files of §2.1 only | **`go build` fails:** `undefined: platform.BuildInfo / ReadBuildInfo / LogBuildInfo` [V] |
| + `platform/buildinfo.go` | build and vet pass; **tests: 79 pass, 6 fail** (`CS_TELEPHONY_GATEWAY_SERVICE_NAME is required but not set`) [V] |
| + `platform/config.go`, `config_test.go` (**set A**) | gateway: **85 pass, 0 fail, 15 skip** (top-level) + 25 subtests, in workspace mode; `GOWORK=off` tests pass; `GOWORK=off go mod tidy -diff` clean; `go mod verify` "all modules verified". Platform: vet and tests pass. [V] |
| Set A, whole workspace | all **45** `go.work` modules: `go build` and `go vet` pass. `go test` for all 16 services: 15 pass; `services/go/voice` fails `TestT11_RealSpeech…` ("the real recogniser produced no final transcript"). **The same failure occurs on unmodified HEAD**, so it is pre-existing and unrelated (a local speech-model environment issue). [V] |

## 5. Checks on the actual worktree (this session)

| Check | Result |
|---|---|
| `gofmt -l` (gateway) | empty [V] |
| `go vet ./...` (workspace and `GOWORK=off`) | clean [V] |
| `GOWORK=off go mod tidy -diff` / `go mod verify` | clean / all modules verified [V] |
| Offline `go test -count=1 -v ./...` | **85 pass, 0 fail, 15 skip** + **25 subtests** [V] |
| Disposable PostgreSQL (pinned digest), `-count=2` | **200 pass, 0 fail, 0 skip** + **50 subtests**; 10/10 overlap probes "no overlap"; container removed [V] |
| `-race` | **not run** (no cgo/GCC locally) [U] |
| golangci-lint (`.golangci.yml`) | **not run** (not installed; installing is out of scope) [U] |

**Skipped tests:** all 15 skips are real-PostgreSQL suites gated by `CS_TELEPHONY_GATEWAY_FENCE_TEST_DSN`. They are in `internal/fence` (11), `internal/app` (3) and `cmd/server` (1). [V]

**Network:** no gateway test needs the internet, AWS or staging. STS is a local `httptest` fake; dials target `127.0.0.1`. Since P1-41, the IAM tests fail before resolving the test writer hostname. [V by reading and the offline run]

## 6. F-8 status

- **Resolved in code [V]:**
  - the four fields were renamed;
  - three tests go through `platform.LoadConfig → Validate → platform.NewLogger`;
  - mutation checks showed that the old names, or a name containing `ip`, fail them (P1-43).
- **Commit dependency [V]:** those tests **require the platform `config.go` change** (§4). Committed without it, they fail.
- **Unverified [U]:**
  - the staging log pipeline (collector and sink redaction) leaving the fields intact;
  - any E1/E2 measurement. E1/E2 remain **unmeasured**.

## 7. Which CI jobs would include the gateway (committed workflow definitions at HEAD)

CI runs the workflow files **as committed**. The working-tree edits to `ci.yml` and `pr-go.yml` are not in effect until committed. [V]

| Job (HEAD) | Gateway included? | What it would run | Notes |
|---|---|---|---|
| `pr-go.yml` `discover` → `build-test` | **yes**: modules come from `go.work`, which lists `./services/go/telephony-gateway` [V] | `go build`, `go vet`, `go test -race -count=1 -coverprofile` (workspace mode, `-mod=readonly`, Go 1.25.x) | first `-race` run of this code [I]. The 15 real-PG tests **skip**: no workflow sets `CS_TELEPHONY_GATEWAY_FENCE_TEST_DSN` [V] |
| `pr-go.yml` `lint` | **yes** (same module list) | golangci-lint | result unknown; lint never run locally [U] |
| `pr-go.yml` triggers | `services/go/**`, `packages/go/**`, `go.work` | — | a gateway commit triggers it [V] |
| `ci.yml` `backend-test` | **yes** (iterates `go.work`) | `go build`, `go test -count=1` (no race) | also runs `go work sync` (restores `go.work`; not a gate) [V] |
| `hardening.yml` race (nightly, `-count=5 -shuffle=on`) | **no** | — | hard-coded `AI_MODULES` list; the gateway is not in it [V] |
| Working-tree-only `pr-go.yml` additions (tidy check with `GOWORK=off`, toolchain checks) | only if the owner commits them | — | the gateway passes `GOWORK=off go mod tidy -diff` locally [V] |
| Real-PostgreSQL fence suites | **no** CI job | — | they skip in every committed workflow [V] |
| `pr-contracts.yml`, `pr-python*.yml` | no | — | unrelated [V] |

**Consequence [I]:** after a commit, CI would cover build, vet, unit/offline tests, one `-race` pass and lint. It would **not** cover the real-PostgreSQL ownership, overlap and timing suites, nor repeated shuffled race runs. Covering them needs a CI decision (D8): a PostgreSQL service container setting the DSN variable, and/or adding the gateway to `hardening.yml`. Neither is changed here.

## 8. Required checks before a future commit (owner)

1. The commit contains set A (§4) **and** the gateway's `go.sum`. Without `go.sum`, `-mod=readonly` builds cannot verify the new modules [I; the simulation always included it].
2. `buildinfo_services_test.go` is **not** in the same commit, unless all 16 services' `cmd/server/buildinfo*.go` (and their `main.go` contract edits) are committed with it.
3. Re-run on the exact staged tree: gofmt, `go vet`, `GOWORK=off go mod tidy -diff`, `go mod verify`, offline tests (expect 85/0/15), and the disposable-PG `-count=2` run (expect 0 fail, 10/10 "no overlap").
4. Secret scan of the staged diff: no keys, tokens, ARNs, hostnames or DSNs. The test fixtures use fake markers only.
5. Confirm that `deployment.yaml`, workflows, manifests and `go.work` are **not** staged unless intended separately.
6. Confirm the protected files are committed **as the owner intends**. `main.go`/`main_test.go` are user-owned, and the gateway cannot build without the `main.go` edits.
7. After pushing: read the actual `pr-go` result, especially `-race` and lint. Do not infer it.

## 9. Proposed minimal commit grouping (nothing staged or committed)

| # | Commit | Files | Depends on |
|---|---|---|---|
| 1 | platform: `ServiceConfig` no longer requires `SERVICE_NAME` | `packages/go/platform/config.go`, `config_test.go` | — (whole workspace builds, vets and tests with it; §4) |
| 2 | platform: shared build-info contract (gateway subset) | `packages/go/platform/buildinfo.go`, `buildinfo_test.go` | 1. The owner may instead commit the **whole** 16-service contract, including `buildinfo_services_test.go` and every service's `cmd/server/buildinfo*.go` and `main.go` edits; that larger set was **not** simulated. |
| 3 | telephony-gateway: PostgreSQL fence, RDS IAM authentication, phase timing (F-6, F-8) | `services/go/telephony-gateway/{go.mod,go.sum}`, `cmd/server/{main.go,main_test.go,buildinfo.go,buildinfo_test.go,buildinfo_hooks_test.go,fence_startup_test.go}`, `internal/**` | 1, 2 |
| 4 | docs: telephony P1-40 to P1-44 and the overlap-monitor scaffold | `docs/telephony/P1-40…P1-44`, `tools/scripts/fence-validation/readiness-overlap-monitor.sh` (earlier P1 docs at the owner's discretion) | — |

**Excluded:** `go.work`, workflows, manifests, `configmap.yaml`, Dockerfiles, `Taskfile.yml`, other services, `deployment.yaml` (stays deleted), the Python and Android changes.

## 10. Remaining blockers and decisions

| Item | State |
|---|---|
| **D10** (I, T, V) | unresolved; evidence per P1-42; E1/E2 unmeasured [U] |
| **`prod-global`** region policy | unresolved [U] |
| **F-3** `ExpiryWindow=0` / early refresh | D10-dependent decision [U] |
| **F-4** `HTTPS_PROXY` honoured by STS | owner decision (a)/(b)/(c) [U] |
| **F-7** base manifest: 2 replicas, RollingUpdate | vs the A/B `Recreate` staging design; A5 [U] |
| **F-6** untracked code | resolved only by the owner's commit (§9) [U] |
| **INFRA-AUTH (A7)**, A5, A6, A8, A9, SEP | unapproved [U] |
| **D8** CI / `-race` | unapproved: `-race` never run; real-PG suites not in CI; gateway absent from nightly race [U] |
| golangci-lint result | unknown [U] |
| Compile under Go 1.25.x | not run (local Go 1.26.5) [U] |
| Exotel AgentStream production media | **NO-GO** |

## 11. Summary

- **Verified:**
  - the gateway change set is coherent and passes every available offline check, including the real-PostgreSQL suite;
  - F-8 is fixed in code;
  - the minimal buildable commit set is the gateway module **plus** two user-owned platform changes (`config.go`, `buildinfo.go`), and with it all 45 workspace modules build and vet.
- **Inferred:**
  - once committed, `pr-go` and `ci.yml` would build and test the gateway, including its first `-race` run;
  - the real-PG suites, the nightly shuffled race job and staging would still not cover it.
- **Unresolved:** D10, `prod-global`, F-3, F-4, F-7, INFRA-AUTH, D8, lint and the Go 1.25 compile.
