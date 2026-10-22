# Phase 16 — Platform Hardening & Observability

Provisional name and path, as decided by the owner on 2026-10-21. The existing
`docs/phase16/` belongs to the unpublished voice-lifecycle work and is not this
phase. This file is the phase's execution log: what was merged, what was
verified, what is blocked and on what decision.

Phase 15 is **not closed** (`docs/phase15/READINESS_AUDIT.md` §10.20). Its
blockers are tracked there, not here.

## Scope

1. **M1** — CI security and supply chain.
2. **M2** — Observability.
3. **M3** — Reliability.
4. **M4** — AI evaluation and quality gates.

The full documented freeze applies (the 13 modules listed in
`docs/phase14/DEPENDENCY_MAP.md` §13). Nothing in this phase edits a frozen
module without an explicit exception. The one exception granted so far is
recorded under [Freeze exception FE-1](#freeze-exception-fe-1--lint-only-cleanup).

## Freeze exception FE-1 — lint-only cleanup

**Granted by the owner on 2026-10-22, before Phase 16's M2 work continues,**
so that `pr-go` can be fully green.

**Scope: these 11 frozen modules, and only these:** `audiointel`, `evaluation`,
`toolruntime`, `speech`, `telephony`, `governance`, `media`, `runtime`,
`evalsubjects`, `memory` and `conversation`. `audiobridge` and `metrics` are
frozen too but have no lint findings, so they are not in scope.

**Rationale.** On `main` `fcd4c99`, `pr-go` run 39731168851 failed 11 jobs. All
11 are the `golangci-lint` jobs of these modules, with 492 findings in total.
Every other job passed. #63, #64, #65 and #66 had already cleared every
non-frozen lint baseline. The freeze was the only remaining obstacle to a green
`pr-go`.

**What it permits.** Only the smallest source edits that resolve those verified
findings: comments, local renames, idiomatic rewrites with identical behaviour,
explicit handling of errors that were silently discarded, and one-line
`nolint` directives that each state a reason.

**What it does NOT permit.** It does not unfreeze any module. It excludes
changes to:
- business logic or runtime behaviour;
- exported APIs or interfaces;
- dependency versions;
- CI gates or the lint configuration;
- security controls;
- any file outside the 11 modules.

A finding that turns out to need any of those stops that fix, and the finding
is reported on its own rather than presented as lint cleanup.

**Error semantics.** A `%v` is not mechanically changed to `%w` where wrapping
would change what `errors.Is` or `errors.As` match.

**Pull requests.** Recorded here as they merge.

## M1 — CI security and supply chain

### Merged (each squashed at a verified head; `main` CI checked after each)

| PR | Change | Merge commit |
|---|---|---|
| #50 | Secret scanning in CI (`pr-security.yml`, pinned gitleaks); `.gitleaksignore` for three fake test fixtures by exact fingerprint | `847346c` |
| #52 | `pr-contracts`/`pr-python` defects (buf base ref, `uv lock --check`, formatting); `tools/ci/*_test.py` lint allowances | `73dbbc0` |
| #51 | Every GitHub Action pinned to a verified commit SHA; `tools/ci/check_action_pins.py` | `a0612fa` |
| #53 | pytest 9.0.3 for CVE-2025-71176 (with pytest-asyncio 1.3.0) | `c0e75dc` |
| #54 | Gradle wrapper verified against Gradle's checksums (`pr-android-wrapper.yml`) | `64717b8` |
| #55 | Python dependency audit: hash-pinned pip-audit on PRs and nightly (`python-deps-audit.yml`) | `37c3402` |
| #57 | Workflow lint: pinned actionlint (`workflow-lint.yml`) | `fb60a19` |
| #56 | Hardening audits updated: action pinning closed; Go third-party dependencies listed | `5d2e9c8` |
| #58 | Pinned shellcheck in the workflow lint; five existing notes fixed | `63cbed5` |
| #59 | Licence inventory of the shipped Go dependencies (`DEPENDENCY_AUDIT.md` §10) | `9201a62` |
| #60 | Licence inventory of the locked Python packages (`DEPENDENCY_AUDIT.md` §11) | `c7bddf7` |

Every workflow on `main` now pins its actions by SHA, and each downloaded tool
(gitleaks, actionlint, shellcheck, pip-audit and its dependencies) is verified
by checksum or hash before use.

The repository is **not lint-clean**: the 20 baseline golangci-lint jobs in
`pr-go` remain, and their policy questions are open (Phase 15 C-1 to C-6).

### Blocked, with the decision each needs

| Item | Blocked on | Owner |
|---|---|---|
| Real security contact in `SECURITY.md` | the address to publish. GitHub private vulnerability reporting is **disabled** on this public repository, so `SECURITY.md` currently names two channels that do not work | owner; enabling reporting is an admin setting |
| Dependabot security updates | disabled at repository level, although `dependabot.yml` asks for security-only updates | admin setting |
| Container image scanning, SBOMs, signing, base-image digests | which Dockerfile approach is kept: the owner's uncommitted `GOWORK=off` version or the merged workspace-manifests version (#49). The owner's image workflows (`pr-go-images.yml`, `pr-python-images.yml`) build on the former | owner |
| Kubernetes manifest hardening | ADR-0024 (#44) and the intentional deletion of `infra/k8s/base/deployment.yaml` | owner |
| Gradle dependency verification (`verification-metadata.xml`) | an Android SDK build environment to generate it | owner / environment |
| Licence notices in artefacts | how NOTICE files (aws-sdk-go-v2, smithy-go, structlog) and MIT/BSD notices reach shipped artefacts; depends on the image decision | owner |
| Dependabot PRs #2 to #5 | they bump tag pins (`@v7`) and predate SHA pinning; whether to close or regenerate them | owner |

## M2 — Observability: inventory (verified on `main` `9201a62`)

| Area | State | Evidence |
|---|---|---|
| Logging | `slog` JSON handler with redaction: a deny list of secret-like keys and HMAC pseudonyms for personal identifiers | `packages/go/platform/logging.go` |
| Log redaction of content | **not covered**: no `transcript`, `text`, `prompt`, `body`, `utterance` or `content` key is redacted, although the metric-label deny list has them | `logging.go` key list; `packages/go/metricsexport/export.go` |
| Metrics | custom registries in 10 modules; Prometheus text exporter; `Service.SetMetricsHandler` | `packages/go/metrics`, `packages/go/metricsexport`, `packages/go/platform/server.go` |
| `/metrics` served | **no service mounts it**: 0 non-test uses of `SetMetricsHandler` or `metricsexport` under `services/` | `git grep` |
| Tracing | a `runtime.Tracer` port with only `NoopTracer`; one span in production code; no OpenTelemetry dependency in any `go.mod` (the three mentions are comments) | `packages/go/runtime/kernel.go` |
| Correlation | `CorrelationIDMiddleware` and `platform.WithTraceID` exist; **0 callers** outside their packages | `packages/go/middleware`, `packages/go/platform` |
| Health | `/healthz` and `/readyz` on the health port (default 8081); only the telephony gateway registers a readiness check (the fence) | `platform/health.go`, `services/go/telephony-gateway/internal/app/fence.go` |
| Probe port | Kubernetes template probes **9090**; services default to **8081** | `infra/k8s/base/deployment.yaml`, `platform/config.go` |
| Alerts, SLO rules, dashboards | **none** in the repository; ADR-0011 defines the latency SLOs | `docs/adr/0011-end-to-end-latency-budget.md` |

## M2 — Proposed changes and what each depends on

Every code change below overlaps work the owner has in progress but not
committed, or needs a decision. None has been started.

| Change | Files | Depends on |
|---|---|---|
| Redact content keys in logs (enforces invariant I7 in logs, not only metrics) | `packages/go/platform/logging.go` + test | the owner's untracked `packages/go/platform/logging_test.go` covers the same code: coordinate first |
| Unique correlation IDs; propagate them into logs | `packages/go/middleware/middleware.go` | the owner has modified `middleware.go` and added `middleware`/`ratelimit` tests: coordinate first |
| Serve `/metrics` from each Go service | `services/go/*/cmd/server/main.go` | the owner has modified all 15 services' `main.go` (build-info `version` subcommand): coordinate first |
| Alert and SLO rules, checked by a pinned `promtool` in CI | new rules directory + workflow | where the rules live (the infrastructure tree is the ADR-0024 decision) |
| Tracing export | new OpenTelemetry dependency; `runtime` holds the tracer port | an ADR (no free ADR number is verified: 0024 is taken by #44 and by an untracked owner document, 0025 by another untracked owner document, and 0026 is cited in the owner's untracked phase 18/19 documents) and the `runtime` freeze |
| Probe port alignment | `infra/k8s` | ADR-0024 |

**Credentials and cost.** None of the M2 changes needs an external credential
or paid service. A tracing backend or hosted metrics service would; none is
proposed.
