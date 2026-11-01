# F11 and F12: refused refresh proofs are invisible, and identity reports ready without configuration

- **Status: investigation only. Nothing here is implemented.**
  - F11's remedies touch identity's audit trail and session handling. Under `ARCHITECTURE_FREEZE.md` §6.1 they are treated as high-risk until the owner classifies them (rule 7); the theft-response option (§1.4, R3) is high-risk outright.
  - F12's remedy changes identity's documented activation contract, which is an owner decision (OD-15).
- **Origin:** findings F11 and F12 in [`PRODUCTION-READINESS.md`](PRODUCTION-READINESS.md); F11 is option E of [`F1-REFRESH-PROOF-REJECTION.md`](F1-REFRESH-PROOF-REJECTION.md).
- **Review:** this is an AI-performed, read-only investigation (§6.1 rule 9). It is not an independent human review.
- **As of:** `main` `bba198d`, 2026-11-01.

## 1. F11: what identity records when it refuses a refresh proof

### 1.1 Evidence

| What | Where | Finding |
|---|---|---|
| Logging | `httpapi.problem` | Logs a refusal **only when the status is 5xx**. A `400 ERR_INVALID_DPOP_PROOF` writes **no log line** |
| The reason | `httpapi.fail` | Maps `app.ErrInvalidDeviceProof` to the code and drops the wrapped reason ("proof key is not the enrolled key", "proof already used", "proof is stale", "htu does not match…") |
| Metrics | `identity_auth_requests_total{endpoint="token_refresh", outcome="invalid_dpop_proof"}` | One counter for every reason. No session, no key, no reason |
| Audit events | `app` (`SecurityEventType`) | OTP failures and lockout, refresh-token **reuse**, enrolment and re-enrolment, integrity, deletion. **No event for a refused refresh proof** |
| Rate limits | refresh path (`RefreshTokenWithDeviceProofAndIntegrity`) | **None.** OTP request and verification are limited per number; refresh is not |
| Replay reservation | `packages/go/dpop` `verify`; identity `acceptRefreshProof` | At refresh the verifier is not told the session's key, so a proof **by a foreign key reserves its `jti`** before identity compares the key. edge-api checks the binding first ("a proof made with any key but the token's reserves nothing") |

Identity checks the session and the refresh token before the proof (F1 §2.1). So every case below needs the **current** refresh token of a live session; without it, the answer is `401 ERR_INVALID_TOKEN`.

### 1.2 Threats

| # | Scenario | What happens | Visible today? |
|---|---|---|---|
| T1 | **A stolen refresh token, used without the device key** (backup leak, malware, logs) | Refused for its key. The token stays current. Once the real phone refreshes, the stolen copy is superseded, and its next use is **reuse**: the session is revoked and `identity.refresh.token_reused` is audited | **No**, until the real phone rotates. Up to 15 minutes, or longer while the phone is off; the attacker may try without limit |
| T2 | **A captured proof replayed**, for example by an intercepting proxy, together with the current token | Refused as replayed (`jti` reserved) | **No.** It is a strong sign of interception |
| T3 | **A server misconfiguration:** the canonical refresh URL differs from the app's origin | Every refresh refused; users signed out (F1) | **Only as a counter spike**, with no reason |
| T4 | **Phone clocks out of the window** | That user refused at every refresh (F1) | As T3, mixed into the same counter |
| T5 | **Storage cost:** T1 repeated | Each attempt reserves a replay row until it expires | No; rows are pruned by housekeeping, so the cost is bounded by the window |

None of these lets an attacker obtain a token: the proof check holds. The gap is **detection and response**. T1 and T2 are the signals ADR-0010 calls "the exfiltration alarm" (reuse detection), seen earlier than reuse detection sees them.

### 1.3 Constraints any remedy must keep

- No subscriber identifier, MSISDN, token, `jti` or key material in a log line or metric label. Labels come from a closed set (as `MetricSecurityEvents` already does).
- An attacker who can send refreshes must not be able to flood the durable audit log.
- No change to which answer identity gives or which answer ends a session, unless decided under F1 (H7).

### 1.4 Remedies

| # | Change | Risk | Behaviour change |
|---|---|---|---|
| **R1** | A `reason` label on refused refresh proofs, from a closed set: `shape`, `signature`, `stale`, `target` (htm/htu), `replayed`, `foreign_key` | Low | None. Distinguishes T3/T4 (`stale`, `target`) from T1/T2 (`foreign_key`, `replayed`) on a dashboard |
| **R2** | Audit events `identity.refresh.proof_foreign_key` and `identity.refresh.proof_replayed`, with the session id (already known at that point), **at most one per session per window** | Low to medium (audit volume, bounded) | None. Gives T1 and T2 a durable record |
| **R3** | **Revoke the session** on T1 (current token, foreign key) | High: it ends a session, and is H7's scope | Yes. Strong response; makes a leaked token useless at once |
| **R4** | Tell the refresh verifier the session's key, so a foreign key is refused **before** the replay reservation (as edge-api does) | Medium: changes `dpop`'s verification interface on an auth path | No answer changes; removes T5 |
| **R5** | A per-session limit on refused refreshes | Medium | Yes: answers 429 after the limit |

**Recommended:** R1 and R2 first. They change no answer and no session, and they make T1–T4 visible and attributable. R4 next, as a hardening of the same path. R3 and R5 belong with the F1 decision (H7).

### 1.5 Regression tests for R1 and R2

| Test | Must catch |
|---|---|
| `httpapi/metrics_test.go`: one refused refresh of each reason increments exactly that `reason` label; a successful refresh increments none | A mislabelled or missing reason |
| Same, with an unknown wrapped error: the label is `other`, never free text | An unbounded label |
| `app` test: a current token with a foreign key's proof emits one `proof_foreign_key` event; the session stays live; the token is not rotated | A silent revocation, or no event |
| `app` test: a replayed proof emits one `proof_replayed` event | No event |
| `app` test: 100 foreign-key attempts within the window emit one event | Audit flooding |
| `pgstore` (real PostgreSQL): events are written by the durable auditor; the audit-failure counter stays at zero | Lost audit records |
| Log assertion: no refused-refresh log line contains the session id, token, `jti` or MSISDN | A personal-data leak into logs |

## 2. F12: identity reports ready with no configuration

### 2.1 Evidence

- **Contract:** with no `CS_IDENTITY_*` variable, identity boots **inactive** (`services/go/identity/cmd/server/identity.go`, `configureIdentity`). It binds no application port, serves no route, and registers one **non-critical** readiness check that fails with "identity inactive…". `identity_test.go` asserts exactly this.
- **Readiness semantics** (`packages/go/platform/health.go`): a failing non-critical check reports `degraded` with **HTTP 200**. Only a critical check returns 503.
- **Shown in CI** (`container-images.yml`, `tools/scripts/image-smoke.sh`, #206): the image started with no configuration answers `/healthz` 200 and `/readyz` 200 `degraded`.
- **Contrast:** a **partial** identity configuration refuses to start, and edge-api with routes but no authentication refuses to start.
- **The repository's only Kubernetes template** (`infra/k8s/base/deployment.yaml`) is generic. It probes port 9090, while the platform's health port defaults to 8081, and it names a floating `aegisai/service:latest` image. Per-service manifests are owner-held, uncommitted work. So nothing in the repository today would deploy identity; this finding concerns whatever does.

### 2.2 Threat and failure analysis

| Scenario | Result |
|---|---|
| A staging or production pod whose secret mount or environment is missing entirely | The pod is **Ready**. The rollout succeeds and no automatic rollback happens. Connections to port 8080 are refused. Sign-in fails for everyone routed to it |
| Security | **Fails closed:** an inactive identity serves nothing and holds no data. No token can be issued |
| The app | A refused connection is `Network`, so signed-in users keep their sessions (C17, F1 §2.2); new sign-ins fail |
| Detection | Only an error-rate alert on the client side or the load balancer. No alert rules exist (§3.8) |

So F12 is an **availability and detectability** defect, not a confidentiality one.

### 2.3 Remedies

| # | Change | Keeps the development contract? |
|---|---|---|
| **A** | When `Environment` (`packages/go/platform/config.go`) is `staging` or `production`, an inactive identity **refuses to start** | Yes |
| **B** | In `staging` and `production`, the inactive check is **critical**: `/readyz` answers 503, `/healthz` stays 200 | Yes. The pod runs for debugging but receives no traffic, and the rollout stalls |
| **C** | Keep the contract; add a deployment smoke test (enrol against the new pods) and an alert on `degraded` | Yes; depends on X5 and E5 |

**Recommended: B**, with C once there are hosts. B matches the platform's readiness model and keeps a pod inspectable. A matches edge-api's behaviour and is simpler. Either is a small, local change once OD-15 is decided.

### 2.4 Regression tests for A or B

| Test | Must catch |
|---|---|
| `cmd/server/identity_test.go`, a table over `Environment` × configuration: `development` + none gives inactive and 200 (today's contract); `staging` and `production` + none give a refusal to start (A) or a critical check and 503 (B); partial stays an error everywhere; full stays active everywhere | A production pod passing readiness while inactive; or development losing the inactive mode |
| `image-smoke.sh`: one more identity run with the production environment, expecting the A or B behaviour | The image not matching the code |
| Platform `health_test.go` (already exists): a failing critical check gives 503 | A change in readiness semantics |

## 3. Governance

| Question | F11 (R1, R2, R4) | F11 (R3, R5) | F12 (A, B) |
|---|---|---|---|
| Changes an Accepted ADR? | No | No; R3 changes session semantics under H7 | No |
| Changes a frozen invariant? | No | No | No |
| Moves a trust boundary? | No | No | No |
| Classification | Treated as high-risk until the owner classifies it (auth audit path) | **High-risk** (H7) | Owner decision (OD-15) |
