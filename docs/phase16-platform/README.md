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

**Pull requests.** Each was squash-merged at a verified head after
`ci-gate` and Build & Test passed. The merge commits were checked on GitHub.

| PR | Scope | Findings resolved | Merge commit |
|---|---|---|---|
| #67 | this exception record | — | `f7ff711` |
| #68 | conversation, memory, evalsubjects, runtime | 73 of 73 | `e1f0aec` |
| #69 | governance, media | 59 of 68 | `b5d919a` |
| #70 | telephony, speech | 86 of 88 | `fed0d45` |
| #71 | toolruntime | 59 of 64 | `527f993` |
| #72 | evaluation | 94 of 94 | `093c974` |
| #73 | audiointel | 104 of 105 | `c4f19e8` |

**Result, verified on `main` `c4f19e8`.**
- pr-security, the Aegis AI CI Pipeline and hardening passed.
- pr-go run 39784281675 passed 132 jobs and failed 5. All 5 are
  `golangci-lint` jobs, holding exactly the 17 findings that FE-1 left
  visible on purpose.
- FE-1 resolved 475 of the 492 findings. The other 17 are real defects, not
  lint. Fixing them changes behaviour, which FE-1 excludes, so they were left
  for the owner rather than suppressed.

| Lint job | Findings | Defect |
|---|---|---|
| governance | 7 errcheck: `emergency.go:301`, `:334`; `human.go:469`; `registry.go:128`, `:184`, `:206`, `:238` | `audit.Record` failures discarded uncounted, against the Auditor contract "An error is counted" |
| toolruntime | 5 errcheck: `compensation.go:239`; `permission.go:224`, `:389`; `registry.go:289`, `:316` | the same, in the tool runtime |
| media | 2 G115: `pipeline.go:344`, `:349` | `fillGaps` converts a `uint64` gap to `int`; a gap above 2^63 skips the `MaxGapFill` clamp |
| speech | 2 errcheck: `stt.go:268`, `tts.go:249` | refused turn transitions ignored on the live path; recorded with the concurrent-`Speak` race |
| audiointel | 1 G115: `continuity.go:107` | `MissingBefore` wraps negative for a gap above 2^63 |

Also reported, and suppressed with a `nolint` that names it:
`evalsubjects`' tool subject records a closed `ToolRuntime` (`ErrClosed` with
an empty `PlanResult`) as a successful step.

**Follow-up defect fixes (owner-authorised, outside FE-1).** These are
separate behaviour fixes with regression tests. Each test fails without its
fix.

| PR | Defect | Status when recorded |
|---|---|---|
| #74 | audit counting, governance and toolruntime: failures are counted, and operations still do not fail | merged as `ef4fd69` |
| #75 | media gap-fill overflow | open |
| #76 | speech: synthesis reserved before the stream opens; ended turns are refused (the concurrent-`Speak` race fix) | open |
| #77 | audiointel `MissingBefore` overflow | open |

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

## Objective 3 — decision records (2026-10-23)

Each task ran in its own worktree from `main` and landed as its own small PR.
The owner's uncommitted work in the primary worktree was read, never copied,
overwritten or reverted.

### M3 — shutdown-test helper rename

**Merged:** #80, as `fb40bc7`.

- **Decision:** rename `recordingRunner` to `shutdownCtxRunner` in
  `packages/go/platform/shutdown_test.go`; the type, its 3 methods and its 1
  use. Logic is unchanged.
- **Why:** the owner's untracked `server_test.go` declares its own, different
  `recordingRunner`. That would redeclare the one on `main` and stop the
  package's tests compiling once that work lands. Checked in a scratch copy,
  that file and the owner's `httprunner_test.go` compile and pass beside the
  renamed type. Neither file was copied into the branch.

### M2 — correlation IDs

**Merged:** #81, as `040cfba`.

- **Decision:** `CorrelationIDMiddleware` mints with `runtime.NewCorrelationID`,
  the single minter ADR-0014 requires. The old ID was `corr_` plus a
  one-second timestamp, so every request in the same second shared one ID.
- **Inbound values:** an inbound `X-Correlation-ID` is kept only if it has the
  platform shape (`corr_` + 26 Crockford base32 characters). Any other value is
  replaced, so arbitrary header text never reaches logs or the response.
- **Dependencies:** `middleware` now imports `runtime`. `runtime` depends only
  on `metrics`, so no cycle is possible, and importing a frozen module does not
  edit it.
- **Trade-off:** a client that sends its own ID in a foreign format receives a
  platform ID instead. No HTTP caller uses the middleware today.
- **Tests:** 1000 sequential requests yield distinct IDs; 32 concurrent
  goroutines × 100 requests yield distinct IDs; valid inbound IDs propagate
  unchanged; 8 invalid inbound forms are replaced; the validator accepts 1000
  runtime mints.
- **Overlap:** the owner's uncommitted `middleware.go` takes the same approach,
  so a textual conflict is expected when that work is rebased.

### M2 — auth stubs

**Merged:** #83, as `af51394`.

- **Decision:** delete `AuthMiddleware`, `RequireRole` and `UserContextKey`.
  Keep `UserClaims`, because `services/go/identity` serialises it.
- **Evidence it is safe:**
  - **0 callers** on `main`, on every local and remote branch, in every
    service, and in the owner's working tree.
  - No contract, generated code or API depends on these symbols.
  - The owner's untracked Phase 18 D11 record reaches the same conclusion
    independently, with D11.1 authorising "Delete it, along with
    `RequireRole`".
- **Guard:** `no_auth_stub_test.go` reads declared identifiers and string
  literals from the syntax tree. It caught all 5 mutations tried, including
  reintroducing a stub.

### M2 — `/metrics` and the probe port

**Decision: no code change, deferred with evidence.**

- **The port is 8081, set by the project's own contracts:**
  - ADR-0013 mounts `GET /metrics` "on the existing `HealthPort` mux";
  - `platform.ServiceConfig.HealthPort` defaults to 8081;
  - `platform/metrics_route_test.go` asserts that `/metrics` shares the health
    listener on `:8081`.
- **The 9090 in `infra/k8s/base/deployment.yaml` is the stale side.** The
  port question is therefore **decided, but not applied** to the tracked
  manifest. That file is not changed here:
  - the owner deliberately deleted it in their working tree, and their
    untracked per-service manifests probe 8081;
  - their P1-37 record says `deployment.yaml` "is never restored";
  - the manifest tree itself is PR #44's pending ADR.
- **Mounting `/metrics` now would publish nothing.**
  - No service exports a metric today. The 15 service binaries other than
    `voice` import only `platform`, plus `middleware` in `identity`'s case, and
    own no instruments at all. The one instrument set reachable from a service
    binary is `voice`'s `conversation` engine, described below.
  - The only mount points are the 15 `cmd/server/main.go` files, and the owner
    has uncommitted changes in all of them (build-info `version` subcommand).
- **Not exposed publicly, now or when mounted.** The health listener has no
  Kubernetes Service in the owner's manifests and no ingress, so it is
  reachable only at the pod IP inside the cluster.
- **First meaningful owner: `voice`.** It is the only service that registers a
  runner. That runner owns a `voiceintel.Bridge`, whose
  `Engine().Metrics().Registry()` is the `conversation` engine's real
  instrument set, built on the production path.
- **Not wired yet, for two reasons:**
  - **No traffic source on `main`.** No call driver exists on `main`, and the
    owner's untracked ADR-0022 records that "the production build registers
    no driver".
    The engine therefore never runs a conversation in production.
    `metrics.Counter.samples` emits only label sets that have been observed,
    so an unincremented counter, labelled or not, produces **no series at
    all**. Only gauges would appear, each reading zero. That is an empty
    endpoint in effect, and this phase does not ship one to claim
    completion.
  - **The owner's uncommitted work.** It rewrites `services/go/voice/go.mod`'s
    require block, which is exactly where `metricsexport` would be added. It
    also modifies `cmd/server/main.go` and 17 of its tests, and carries
    `cmd/server/driver.go` and `internal/`, neither of which is on `main`. Landing a change there first would force the
    owner into a merge in the middle of their work.
- **Unblocks when** the owner's voice work, including its call driver, has
  landed on `main`. Then mount `/metrics` in `voice`'s `buildService`, using
  `SetMetricsHandler(metricsexport.New(...).Handler())` over the bridge
  engine's registry. A test should assert that a driven turn moves a series.

### M2 — tracing

**Merged:** #82, as `c17177d`. ADR-0026 has status **Proposed**: merging it
records the proposal and does not accept it.

- **Recommendation:** W3C Trace Context, implementing the existing frozen
  `runtime.Tracer` / `runtime.Span` port in the non-frozen `telemetry`, with a
  log exporter first and the backend deferred.
- **Reuse without a frozen edit:** a type in `telemetry` satisfies the exported
  interfaces structurally, and `runtime.WithTracer` already exists to inject
  it. No production code constructs a `runtime.Kernel` today, so no production
  span exists yet.
- **Status today:** the ADR exists. No tracing instrumentation exists, and no
  backend is chosen.
- **No tracing code is written** until the ADR is accepted.
- **Numbering:** 0026 is the first number used or claimed nowhere:
  - `main` has 0000–0017;
  - the owner's tree has 0018–0025;
  - #44 claims a second 0024;
  - no file, branch, commit or open PR uses 0026 or above.

### M2 — content redaction in logs

**Merged:** #85, as `f6fdb46`.

- **Decision:** `RedactingHandler` replaces values under conversation-content
  keys with `[REDACTED]`. This enforces ADR-0006 §10: prompts and transcripts
  are "never logged verbatim".
- **Matching:**
  - `transcript`, `utterance` and `prompt` match as substrings;
  - `text`, `content` and `body` must be the key's last word, so `context`,
    `content_type` and `body_size` are kept.
- **Effect today:** no production log call uses a content key, so no current
  log output changes.
- **Tests:** 20 content-key spellings suppressed and 10 lookalikes preserved;
  groups, `WithAttrs`, allow-list precedence and the tokeniser are covered.

**Correction to the M2 table above:** it said "0026 is cited in the owner's
untracked phase 18/19 documents". The only such citation,
`PHASE18_FINAL_CLOSURE_AUDIT.md:225`, records "**no ADR-0026**", which states
that the number is absent rather than claiming it.

## Objective 3 continuation — real metrics, tracing adapter, manifests (2026-10-23)

### M2 — `/metrics` with real measurements

**Merged:** #86, as `83f0f40`.

- **Owner:** the telephony gateway is the first service to own and expose real
  metrics.
- **What it measures:** every readiness evaluation of its single-active-process
  fence, which is the gateway's real, recurring production work. Each kubelet
  `/readyz` probe evaluates the fence, and the answer decides whether the
  instance receives traffic.
  - `telephony_gateway_fence_readiness_checks_total{result}` counts
    evaluations by `owned`, `not_owner` or `error`.
  - `telephony_gateway_fence_owned` is 1 while the latest evaluation found
    this process the owner, otherwise 0.
- **How:** `app.WireFenceWithMetrics` wraps the checker the fence registers.
  - The `fence` package is not touched: acquisition, heartbeats and the open
    flake area are unchanged.
  - The readiness check keeps its name, criticality, timeout and result.
- **Where:** `/metrics` is mounted with `SetMetricsHandler` on the health
  listener (8081), never on the application port.
  `docker/compose/prometheus.yml` already scrapes `telephony-gateway:8081`.
- **Why this service:**
  - `platform` is standard-library-only at `go 1.23.0` (D8), and importing
    `metrics` would raise that directive.
  - `voice` has no production traffic source. There is no call driver on
    `main`, and the owner's local driver is registered only when a test
    supplies its options (ADR-0022), so its instruments would never move.
  - The gateway's `main.go` is the one service entry point with no uncommitted
    owner changes.
- **Tests:**
  - **Unit:** outcome classification, concurrency, duplicate registration and
    shutdown; plus a real `fence.Fence`, driven by a fake session, gaining and
    losing ownership as seen through the exporter.
  - **Binary:** the real binary's probes appear on `/metrics` over HTTP.
  - **Real PostgreSQL:** a single-owner test, run in gateway-postgres, asserts
    `owned`=1. That suite went from 141 to 157 passing tests, with 0 skipped,
    under `-race`.
- **Still without metrics:** `voice`. It unblocks when a production call driver
  and providers exist. Then `voice` exposes
  `Bridge.Engine().Metrics().Registry()` the same way.

### M2 — tracing: implemented adapter, decision still open

**Merged:** #87, as `c9c2e65`. ADR-0026 stays **Proposed**.

- **What exists now:** `packages/go/telemetry` implements the frozen
  `runtime.Tracer` / `runtime.Span` port with:
  - W3C Trace Context identity and a strict `traceparent` codec;
  - `crypto/rand` ids;
  - an attribute allowlist;
  - a log exporter that writes through the platform's redacting logger.
- **The tested path:** a real `runtime.Kernel` built with the existing
  `runtime.WithTracer` records its own `runtime.generate` span as a child of
  the caller's span. No frozen module changes.
- **What does not exist, and why:**
  - **No production span:** no production code constructs a `runtime.Kernel`.
  - **No `traceparent` propagation at a service edge (ADR-0026 step 2):** no
    service binds an application HTTP listener. `platform.HTTPRunner` has no
    constructor call in any service, and `middleware` has no HTTP callers.
  - **No backend:** none is chosen.
- **Left for acceptance:** the `telemetry.go` stub declarations are untouched.
  Replacing them is part of accepting the ADR.

### Owner decision note — ADR-0026 acceptance and the tracing backend

| | Option | Credential | Trade-off |
|---|---|---|---|
| **A (recommended)** | Services export OTLP to an **in-cluster OpenTelemetry Collector** (for example ADOT on EKS), which forwards to **AWS X-Ray in `ap-south-1`** | **None in any service.** Only the Collector's Kubernetes ServiceAccount needs an IAM role through **IRSA** (`xray:PutTraceSegments`, `xray:PutTelemetryRecords`), configured in infrastructure, never in the repository or in chat | Matches ADR-0008 ("IRSA for every workload. No static cloud credentials anywhere") and ADR-0012 residency. Adds one OpenTelemetry SDK dependency, behind the step-4 adapter, with its own ADR |
| B | The same Collector forwarding to **self-hosted Grafana Tempo**, with S3 storage reached through IRSA | IRSA for Tempo's S3 bucket only | No vendor, and data stays in-account. But SRE operates Tempo |
| C | Vendor SaaS (Datadog, Honeycomb) | An ingest API key in the secret manager | Least to operate. But it needs a credential, egresses operational data (ADR-0012), and creates vendor lock-in |

**Recommendation:**
1. Accept ADR-0026 as written, which authorises replacing the `telemetry` stub.
2. Choose option A.
3. Then implement step 2 when the first service binds an HTTP edge, and step 4
   as a Collector adapter with its own ADR.

No credential is needed until step 4. When it is, it is an IRSA role on the
Collector's ServiceAccount, provisioned by SRE.

### Kubernetes manifests and the probe port

- **The only mismatch is the template.** Of everything tracked on `main`, the
  9090 port appears only in `infra/k8s/base/deployment.yaml`. That is the
  template the owner deliberately deleted, and that P1-37 says is never
  restored. It is **documented and not resurrected**.
- **No other tracked manifest exists:** only a ConfigMap and a Namespace.
- **Everything else agrees on 8081:**
  - ADR-0013 and the `HealthPort` default;
  - `EXPOSE 8081` in both Dockerfiles;
  - `docker/compose/prometheus.yml`, which scrapes 8081;
  - the owner's untracked per-service manifests.
  - The two compose 9090 entries are the Prometheus server's own port, not a
    service port.
- **No Kubernetes Service or Ingress exists**, tracked or local, so `/metrics`
  is not publicly reachable.
- **The decision still open:** the manifest tree itself (PR #44). PR #44's
  proposal was **renumbered from 0024 to ADR-0027** on its branch.
  - The owner's Accepted VAD ADR holds 0024, and 0026 is on `main`.
  - 0027 is the first number used or claimed nowhere.
  - The proposal stays Proposed and unmerged, because it amends the
    owner-approved P1-37 staging path.

## Objective 3 — decision packet and integration plans (2026-10-23)

Everything below is analysis and planning. **No ADR status changes, no backend
is chosen, and nothing is provisioned or deployed.**

### Decision A — ADR-0026 acceptance

- **Not ready as merged.** A review against the implementation (#87), the
  frozen `runtime.Span` contract and ADR-0008/0012 found six unspecified
  points:
  1. `tracestate`;
  2. inbound `traceparent` trust at a public edge;
  3. the meaning of the inbound `sampled` flag;
  4. error text in spans;
  5. exporter blocking, drop and flush behaviour;
  6. how the attribute allowlist grows.
- **PR #90** adds those rules and corrects stale facts. **The status stays
  Proposed.** Its description carries the exact status-only diff to apply on
  acceptance.
- **Recommendation:** merge #90, then accept with that diff.
- **Implementation status:**
  - **Implemented and tested** (#87, and #89 test-only): the `runtime.Tracer`
    adapter, the W3C codec, the log exporter, and a real kernel recording
    `runtime.generate`.
  - **Absent from production:**
    - any production span, because no service constructs a `runtime.Kernel`;
    - `traceparent` at any edge, because no service on `main` binds an
      application listener;
    - any backend.

### Decision B — tracing backend (recommendation, not an approval)

| Criterion | A. OTel Collector to AWS X-Ray (`ap-south-1`), recommended | B. OTel Collector to self-hosted Grafana Tempo | C. Vendor SaaS |
|---|---|---|---|
| Operation and ownership | SRE runs the Collector; AWS runs storage and query | SRE runs the Collector, Tempo and its object storage, compaction and upgrades | Least to run; the vendor runs everything after the agent |
| Cost model | Per trace recorded and per trace scanned. **No price is recorded in this repository; SRE must check current `ap-south-1` pricing.** | Compute and S3 storage only; scales with retained bytes | Per span or per GB ingested, usually the highest at volume |
| Residency and retention | In-region. X-Ray's fixed **30-day** retention matches ADR-0012's 30 days for "Telemetry, traces" | In-region S3. Retention is configurable and must be set to 30 days | Depends on vendor regions; needs ADR-0012 review. ADR-0008's region-lock SCPs do not cover a third party |
| IAM and least privilege | IRSA role on the Collector's ServiceAccount only: `xray:PutTraceSegments` and `xray:PutTelemetryRecords` | IRSA role for Tempo's bucket only | An ingest API key in the secret manager, which is a static credential that ADR-0008 avoids |
| Network path | Service to Collector (ClusterIP, OTLP) to the X-Ray **VPC interface endpoint**, never the internet (ADR-0008) | Service to Collector to Tempo, all in-cluster or in-VPC | Collector or agent to the internet through NAT egress |
| Failure behaviour | Collector down: the exporter drops and counts spans; turns never wait (ADR-0026 rule 5). X-Ray down: the Collector's bounded retry, then drop | The same, plus Tempo's own availability | The same, plus internet and vendor availability |
| Sampling and loss | Always-on in process. The Collector may tail-sample before export to control cost | The same | The same, or vendor-side sampling |
| Local development and testing | The log exporter needs nothing. A local Collector with its `debug` exporter, or Jaeger, in compose; no AWS | Tempo and Grafana run locally in compose | Needs the vendor's account or a mock |
| Fit with the tracer and frozen interfaces | One step-4 OTLP adapter behind `telemetry.Exporter`; no frozen change. **Check:** X-Ray must accept random W3C trace ids. Older X-Ray ingestion required an epoch-prefixed id; SRE confirms on the account. If it is unsupported, add an epoch-prefixed id option, which is still valid W3C | Same adapter; W3C ids are native | Same adapter |

**Traffic and cost assumption.** The repository records no call volumes.
- **Unit:** one trace per call, with about 12 spans per turn (11 ADR-0011 hops
  plus the turn).
- **Monthly traces:** about 30 × the daily call count.
- **Cost:** X-Ray cost scales with traces, Tempo with stored bytes, and SaaS
  with spans.
- **Before step 4:** SRE prices option A at the expected call volume.

**Recommendation: A.** It is the only option that is in-region, fixed at
ADR-0012's retention, reached over a VPC endpoint, and credential-free for
every service, using IRSA as ADR-0008 requires. B is the fallback if X-Ray's
trace-id or pricing check fails.

**SRE-owned prerequisites for A.** None of these is performed by this phase.
1. **IAM role** for the Collector, trusted only by the EKS OIDC provider for
   `system:serviceaccount:<namespace>:otel-collector`.
   - Policy: `xray:PutTraceSegments` and `xray:PutTelemetryRecords`.
   - Add the X-Ray sampling-rule reads only if remote sampling is ever used;
     always-on recording does not need them.
2. **The Collector's ServiceAccount** annotated `eks.amazonaws.com/role-arn`, in
   the manifest tree ADR-0027 decides.
3. **A VPC interface endpoint** `com.amazonaws.ap-south-1.xray` with private
   DNS, and a security group allowing 443 from the node security group.
   Mirror it in `ap-south-2` for DR.
4. **The Collector workload:**
   - an image pinned by digest (`infra/README.md` policy);
   - at least 2 replicas with a PodDisruptionBudget;
   - `memory_limiter` and `batch` processors;
   - a **ClusterIP** Service only, with no Ingress;
   - a NetworkPolicy admitting OTLP from `aegis-system` only.
5. **Region-lock SCPs** that allow X-Ray in `ap-south-1` and `ap-south-2`.
6. **Optional KMS encryption** for X-Ray, and a budget alarm on X-Ray spend.

No service needs a credential, and none is ever pasted into chat or committed.

### Decision C — PR #44 (ADR-0027) and the manifest tree

- **Numbering is correct now.** ADR-0027 is the first number used or claimed
  nowhere, while 0024 is the owner's Accepted VAD ADR and 0026 is on `main`.
  The PR changes only the ADR file and one index row.
- **Compatibility with P1-37:**
  - **The deletion:** ADR-0027 keeps "`infra/k8s/base/deployment.yaml` is never
    restored", so the deletion is respected.
  - **The service manifests:** it keeps "`infra/k8s/base/services/` left
    untouched".
  - **The one change:** it amends the **staging path** from
    `infra/kubernetes/overlays/staging/` to `infra/k8s/overlays/staging/`, by a
    recorded amendment that leaves the originals untouched.
  - **Agreement with existing work:** the owner's untracked per-service
    manifests and tooling already live under `infra/k8s`.
- **Accept:** one tree, `infra/k8s`.
  - **Records:** amend P1-37/P1-39 by the appendix, and correct
    `docker-compose.yml:8`.
  - **Helm:** resolve the dead references (remove them, or open a Helm ADR).
  - **Then:** commit the owner's manifests and validation workflow.
  - **Retiring 9090:** the template `deployment.yaml` is still tracked on
    `main`, and the owner's deletion exists only in their working tree.
    Committing that deletion is what finally retires the 9090 probe port.
- **Revise:** keep `infra/kubernetes/overlays/staging/` and move `base/` there
  instead. That means moving every tracked and untracked file and its tooling
  (ADR-0027 Option 2).
- **Close:** two trees remain, `infra/k8s` and `infra/kubernetes`. The 9090
  template stays tracked alongside 8081 manifests, so the probe contract stays
  ambiguous.
- **Port, probe, exposure and rollback implications:**
  - ADR-0027 decides paths only, not ports.
  - The owner's manifests probe 8081, which is the bound health port.
  - No manifest declares a Service or Ingress for the health or metrics port.
  - Rollback of accepting is a documentation revert, because nothing is
    deployed.
- **Recommendation:** accept ADR-0027 (Option 1), then:
  1. record the P1-37 amendment;
  2. land the owner's manifests;
  3. commit the deletion of the tracked template.

### Integration plans, prepared but not applied

**Production tracing (after ADR-0026 is accepted):**
1. **First HTTP edge, `edge-api`.** It exists in the owner's uncommitted work:
   `cmd/server/auth.go` registers `platform.NewHTTPRunner`, with handler
   `SecurityHeaders(CorrelationID(auth(mux)))`.
   - The safe hook is a server-span middleware between `CorrelationID` and
     `auth`, so the span carries the correlation ID and covers authentication.
   - As a public edge, it **always starts a new root** (rule 2).
   - It lands after the owner's `edge-api` work lands, and is exercised by that
     service's real requests.
2. **`identity`.** It also registers an HTTP runner in the owner's work. It
   honours an inbound `traceparent` only if it is reached solely from internal
   peers; that is to be confirmed when its exposure is decided.
3. **Kernel.** The first service to call `runtime.New` passes
   `runtime.WithTracer(telemetry.NewRecordingTracer(telemetry.NewLogExporter(logger)))`.
   No service does today.
4. **Backend (step 4):** an OTLP adapter behind `telemetry.Exporter`, with its
   own ADR, after Decision B.

**Voice metrics prerequisites:**
- **Providers.** A production call driver needs providers. The owner's ADR-0022
  §6 says "the production build registers no driver"; ADR-0018 is still
  Proposed.
- **ADR-0018's revisit trigger:** "a real-time-capable STT provider … without a
  model download, or an approved credential and egress path for a cloud
  provider".
- **When both are met:** mount `voice`'s
  `Bridge.Engine().Metrics().Registry()` the same way as #86, with a test that
  a driven turn moves a series.
