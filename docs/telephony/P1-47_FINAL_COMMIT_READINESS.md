# P1-47 — Final commit readiness: exact four-commit plan

**Phase 19, P1-47.** A read-only verification; this report is the only file created.
- **Not done:** nothing was staged, committed or pushed. **The commands in §5 were NOT executed.**
- **Not claimed:** no CI result.

**Legend:**
- **[V] verified:** observed in this session.
- **[P] verified earlier:** a cited report, on byte-identical files.
- **[U] unverified.**

---

## 1. Worktree state [V]

| Item | Value |
|---|---|
| Branch | `main` (the default branch; §5 creates a topic branch first) |
| HEAD | `06e276d8a78467360b9ed6d5554b445a88b94adc` |
| Staged | 0 |
| Status entry set | identical to the end of P1-46, plus this report |
| `infra/k8s/base/deployment.yaml` | ` D`: intentionally deleted; **in no commit**; never restored |
| Protected files `packages/go/telephony/{lifecycle,runtime}.go`, gateway `cmd/server/{main,main_test}.go` | `git diff HEAD` byte-identical to the recorded P1-31 baselines |
| Git config | `core.fileMode=false` (executable bits are not taken from disk), `core.autocrlf=true` |
| Toolchain | go1.26.5 windows/amd64; `CGO_ENABLED=0`, no C compiler; golangci-lint not installed |

## 2. Exact commit groups

Each row gives git status, the first 12 hex characters of the SHA-256 at verification time, and the line count. [V]

### Commit 1: platform `ServiceConfig.Name` from the binary (2 files)

| St | SHA-256 | Lines | Path |
|---|---|---|---|
| M | 7d2d73f5d23c | 406 | `packages/go/platform/config.go` |
| M | b3a11139c616 | 517 | `packages/go/platform/config_test.go` |

### Commit 2: platform build-info API (2 files)

| St | SHA-256 | Lines | Path |
|---|---|---|---|
| ?? | 3b455788dfd2 | 153 | `packages/go/platform/buildinfo.go` |
| ?? | d12537487c60 | 139 | `packages/go/platform/buildinfo_test.go` |

### Commit 3: telephony-gateway fence, RDS IAM, phase timing, F-8/F-9 (30 files)

| St | SHA-256 | Lines | Path (under `services/go/telephony-gateway/`) |
|---|---|---|---|
| M | 2be0b3aae6b5 | 46 | `go.mod` |
| ?? | 4e652f9f4966 | 46 | `go.sum` |
| M | ad6945dadfad | 105 | `cmd/server/main.go` (protected, user-owned) |
| M | 8ee5b827dda7 | 43 | `cmd/server/main_test.go` (protected; trailing-newline change) |
| ?? | 1e5f795bb013 | 32 | `cmd/server/buildinfo.go` |
| ?? | 2348981f4938 | 164 | `cmd/server/buildinfo_test.go` |
| ?? | 6c4c15f89922 | 47 | `cmd/server/buildinfo_hooks_test.go` |
| ?? | 828cde669b6f | 194 | `cmd/server/fence_startup_test.go` |
| ?? | 5d5452cf7468 | 368 | `internal/app/fence.go` |
| ?? | 6f981c11efa9 | 182 | `internal/app/fence_postgres_test.go` |
| ?? | 983bd08f80fd | 213 | `internal/app/fence_test.go` |
| ?? | bd62c30441fa | 335 | `internal/app/iam.go` |
| ?? | 51e914357086 | 581 | `internal/app/iam_test.go` |
| ?? | 9b1b7f45d991 | 70 | `internal/fence/audit_postgres_test.go` |
| ?? | 221aee873717 | 102 | `internal/fence/audit_test.go` |
| ?? | 2d064ac1efc9 | 129 | `internal/fence/credentials_postgres_test.go` |
| ?? | 534953de5be7 | 300 | `internal/fence/credentials_test.go` |
| ?? | 7cac7c01574d | 553 | `internal/fence/fence.go` |
| ?? | b4be86c26211 | 401 | `internal/fence/fence_postgres_test.go` |
| ?? | a9fce1b53f6b | 410 | `internal/fence/fence_test.go` |
| ?? | 8dd9059337e5 | 206 | `internal/fence/logging_test.go` (F-8) |
| ?? | c3ae4af1af37 | 311 | `internal/fence/timing.go` |
| ?? | c98641b8ea5f | 45 | `internal/fence/timing_postgres_test.go` |
| ?? | d52a48d59b25 | 431 | `internal/fence/timing_test.go` |
| ?? | a87284f1f5e5 | 174 | `internal/fence/tls_test.go` |
| ?? | 1e8e44decb4e | 249 | `internal/rdsauth/aws.go` |
| ?? | 14ca6891e243 | 1032 | `internal/rdsauth/aws_test.go` (**F-9 fix**) |
| ?? | b6f845c691fc | 80 | `internal/rdsauth/logging_test.go` (F-8, **F-9 fix**) |
| ?? | d47c5689b2fd | 184 | `internal/rdsauth/rdsauth.go` |
| ?? | 928a9d04cc02 | 288 | `internal/rdsauth/rdsauth_test.go` |

**Completeness [V]:** the gateway module has exactly **27 untracked files**, and all 27 are listed. The other 3 rows are its tracked, modified files. `go.mod` and `go.sum` are included.

### Commit 4: docs and the overlap-monitor script (8 files)

| St | SHA-256 | Lines | Path |
|---|---|---|---|
| ?? | 13c8a5b6436f | 265 | `docs/telephony/P1-40_A1_DECISIONS_AND_IMPLEMENTATION.md` |
| ?? | b7068bcbd053 | 488 | `docs/telephony/P1-41_INFRA_AUTH_READINESS_AUDIT.md` (includes P1-43 as §10) |
| ?? | 0aba3b362bd9 | 444 | `docs/telephony/P1-42_STAGING_D10_MEASUREMENT_RUNBOOK.md` |
| ?? | 440325034a13 | 199 | `docs/telephony/P1-44_PRECOMMIT_CI_READINESS_AUDIT.md` |
| ?? | 5f90880db8d8 | 154 | `docs/telephony/P1-45_COMMIT_BOUNDARY_AUDIT.md` |
| ?? | d9832d9da78c | 137 | `docs/telephony/P1-46_FLAKY_TESTS_AUDIT.md` (F-9 report) |
| ?? | — | — | `docs/telephony/P1-47_FINAL_COMMIT_READINESS.md` (this report) |
| ?? | d615d17cf4c1 | 151 | `tools/scripts/fence-validation/readiness-overlap-monitor.sh` (`bash -n` ok; `#!/usr/bin/env bash`; LF line endings) |

- **No P1-43 file exists:** P1-43's record is P1-41 §10 and the P1-42 updates.
- **The earlier telephony docs** (`P1-10` … `P1-39`, contract and evidence documents) are untracked and **not** in this plan. Committing them is your call.

### Excluded (no dependency evidence requires them)

- `go.work`;
- every workflow (`.github/workflows/*`);
- `infra/**` manifests, `configmap.yaml` and the `deployment.yaml` deletion;
- `docker/**`, `Taskfile.yml`;
- other services, including their `cmd/server/buildinfo*.go`;
- `packages/go/platform/buildinfo_services_test.go` and the other untracked platform tests;
- Python, Android and other changes.

## 3. Dependency order and what each commit passes

| Commit | Depends on | Independently passes | Evidence |
|---|---|---|---|
| 1 | — | platform vet + 21 tests; HEAD gateway builds and tests; all 45 `go.work` modules build and vet | [P] P1-45 §2 (C1). Files byte-identical to those tested ([V] `cmp`) |
| 2 | 1 (tested only on top of 1) | platform vet + 27 tests; 45/45 build and vet | [P] P1-45 §2 (C1+C2), byte-identical. Commit 2 **without** 1 was not tested [U] |
| 3 | 1 and 2 | **build requires 2** (`platform.BuildInfo`; without it: P1-45 X1/X3 build failure). **Tests require 1** (without it: 6 failures, `SERVICE_NAME is required but not set`; P1-45 X2) | §4 below [V] |
| 4 | — | no Go; `bash -n` on the script | [V] |

**CI note [I]:** a PR's CI runs on the branch head, so it tests the combined state of commits 1–3. A push of commit 1 or 2 alone to `main` would trigger `pr-go` on that state. Per P1-45, those states build and vet; their tests passed locally.

## 4. Final local checks (this session) [V]

**Fresh simulation:** `git archive HEAD` was extracted to the scratchpad, commits 1–3 were overlaid, and Go ran with `GOFLAGS=-mod=readonly`, `GOTOOLCHAIN=local` and `GOPROXY=off`.

| Check | Result |
|---|---|
| gofmt | gateway: no files; platform commit-1/2 files: no files |
| `go build ./...` / `go vet ./...` (workspace mode) | ok / ok |
| `GOWORK=off go mod tidy -diff` / `go mod verify` | clean / all modules verified |
| `go test -p 1 -count=1 -v ./...` (gateway) | **86 pass, 0 fail, 15 skip** (top-level) + **25 subtests** pass |
| `GOWORK=off go test -p 1 -count=1 ./...` | ok in `cmd/server`, `internal/app`, `internal/fence`, `internal/rdsauth` |
| platform `go vet` + `go test` | ok |

**On the real worktree:** gofmt is clean (gateway and the 4 platform files); `go vet`, `GOWORK=off go mod tidy -diff` and `go mod verify` are clean.

**Not re-run here (byte-identical inputs were run earlier):**
- Workspace-wide 45-module build and vet: [P] P1-45, last run on these exact commit-1/2 files. Since then only gateway **test** files changed, and those cannot affect other modules. It was skipped this time because only about 346 MB of memory was free.
- Disposable-PostgreSQL suite: [P] P1-46, **202 pass, 0 fail, 0 skip** + 50 subtests, with 10/10 "no overlap", on the current files (status unchanged since).
- F-9 contention runs: [P] P1-46, 150/150 and 140/140.

**The 15 skips** are real-PostgreSQL tests needing `CS_TELEPHONY_GATEWAY_FENCE_TEST_DSN`. No workflow sets it.

## 5. Exact commands (NOT executed; for the owner)

Run from the repository root in Git Bash. Each commit stages **only** its listed paths, then shows the staged set for review before committing. The commit messages end with the attribution trailer this environment requires.

```bash
# 0. Safety: confirm nothing is staged and work on a topic branch (main is the default branch).
git diff --cached --quiet || { echo "index not empty; stop"; exit 1; }
git switch -c telephony/p1-fence-rds-iam

# ── Commit 1 ──────────────────────────────────────────────────────────────
git add -- packages/go/platform/config.go packages/go/platform/config_test.go
git diff --cached --name-status          # expect exactly: M config.go, M config_test.go
git commit -F - <<'EOF'
platform: take ServiceConfig.Name from the binary, not the environment

Name no longer carries env:"SERVICE_NAME" required:"true"; every service
assigns it from a compile-time constant before Validate, which now rejects
an empty Name. Required by the telephony-gateway fence work (P1-44/P1-45).

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
EOF

# ── Commit 2 ──────────────────────────────────────────────────────────────
git add -- packages/go/platform/buildinfo.go packages/go/platform/buildinfo_test.go
git diff --cached --name-status          # expect exactly: A buildinfo.go, A buildinfo_test.go
git commit -F - <<'EOF'
platform: add the shared build-info API

BuildInfo, ReadBuildInfo, NewBuildInfo, WriteVersion and LogBuildInfo, used
by cmd/server/buildinfo.go. The cross-service invariant test
(buildinfo_services_test.go) is deliberately NOT included: it requires every
service's buildinfo files, which are not yet committed (P1-45).

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
EOF

# ── Commit 3 ──────────────────────────────────────────────────────────────
G=services/go/telephony-gateway
git add -- "$G/go.mod" "$G/go.sum" \
  "$G/cmd/server/main.go" "$G/cmd/server/main_test.go" \
  "$G/cmd/server/buildinfo.go" "$G/cmd/server/buildinfo_test.go" \
  "$G/cmd/server/buildinfo_hooks_test.go" "$G/cmd/server/fence_startup_test.go" \
  "$G/internal/app" "$G/internal/fence" "$G/internal/rdsauth"
git diff --cached --name-status          # expect exactly 30 paths: 3 M + 27 A, all under $G
git diff --cached --name-only | grep -v "^$G/" && { echo "unexpected path staged; stop"; exit 1; }
git commit -F - <<'EOF'
telephony-gateway: PostgreSQL fence with RDS IAM authentication

Single-active-process fence (session advisory lock, fail-closed readiness),
wired at startup; IAM mode via IRSA web identity -> STS -> RDS IAM token,
with static validation (verify-full TLS, explicit CA, writer endpoint,
region allow-list, no static keys, no password fallback); per-phase timing
on the fence's log lines, with redaction-safe field names (F-8) and
scheduling-independent tests (F-9). No D10 timing values are defaulted.

Records: docs/telephony/P1-40 .. P1-47.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
EOF

# ── Commit 4 ──────────────────────────────────────────────────────────────
git add -- docs/telephony/P1-40_A1_DECISIONS_AND_IMPLEMENTATION.md \
  docs/telephony/P1-41_INFRA_AUTH_READINESS_AUDIT.md \
  docs/telephony/P1-42_STAGING_D10_MEASUREMENT_RUNBOOK.md \
  docs/telephony/P1-44_PRECOMMIT_CI_READINESS_AUDIT.md \
  docs/telephony/P1-45_COMMIT_BOUNDARY_AUDIT.md \
  docs/telephony/P1-46_FLAKY_TESTS_AUDIT.md \
  docs/telephony/P1-47_FINAL_COMMIT_READINESS.md
git add --chmod=+x -- tools/scripts/fence-validation/readiness-overlap-monitor.sh   # core.fileMode=false: set the bit explicitly
git diff --cached --name-status          # expect exactly 8 added paths
git commit -F - <<'EOF'
docs(telephony): P1-40..P1-47 fence/IAM decisions, audits and staging runbook

Includes the read-only readiness-overlap monitor used by the staging plan.
Staging, INFRA-AUTH and D10 remain unapproved/unresolved.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
EOF

# After all four: the remaining worktree must be untouched.
git status --short | grep -c . ; git status --short infra/k8s/base/deployment.yaml   # still " D"
```

**Notes:**
- **Whether to execute:** the commands, the branch name and the messages are proposals; running them is your decision. Pushing and opening a PR are separate steps; neither is executed or proposed here.
- **Commit 3 stages directories:** `internal/{app,fence,rdsauth}`, whose content is exactly the 22 listed files (no other files exist there). The `name-status` and path-guard lines are the check.
- **Line endings:** `core.autocrlf=true` normalises line endings on add. A CR scan of all 42 planned files found **no CRLF** [V]; `git diff --cached` shows any surprise.
- **A stray binary:** `services/go/telephony-gateway/server.exe` (9.4 MB, dated Oct 12) is in the module directory. It is **ignored** (`.gitignore:63: *.exe`) [V], and no command above adds the module root, so it cannot be staged. It is not deleted here.
- **The script bit:** adding the script with `--chmod=+x` sets its executable bit in the index. P1-32 invokes it directly. If you prefer `bash <script>` usage, drop the flag.

## 6. Remaining verification gaps

| Gap | State |
|---|---|
| `-race` | never run (no cgo or C compiler); the first evidence would be CI `pr-go` [U] |
| Go 1.25.x | never compiled or tested (only 1.26.5 local); static evidence only (P1-45 §3) [U] |
| golangci-lint | not installed; never run [U] |
| CI | not run; no result is claimed [U] |
| Real-PG suites in CI | none: no workflow sets the DSN; the gateway is not in the nightly race job [U] |
| Other timing-based tests under contention | only `rdsauth` was stress-tested (P1-46) [U] |
| Staging, AWS, Aurora, IRSA | none [U] |

**Blockers (unchanged):** D10 (E1/E2 unmeasured), `prod-global`, F-3, F-4, F-7, INFRA-AUTH (A7) and A5/A6/A8/A9, D8. Exotel AgentStream production media: **NO-GO**.

## 7. Statement

The repository remains **unstaged and uncommitted**: 0 staged, HEAD `06e276d`, no branch created. None of the §5 commands were run.
