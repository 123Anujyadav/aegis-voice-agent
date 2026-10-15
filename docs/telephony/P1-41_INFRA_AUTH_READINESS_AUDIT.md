# P1-41 — INFRA-AUTH readiness audit: RDS IAM authentication for the telephony-gateway fence

**Phase 19, P1-41.** Read-only audit, plus two code-only fixes (§3.2).

**Inputs:** P1-37 (C1–C3, O-5, gate table A1–A12), P1-38 (SEP evidence items, IAM/STS outage rule), P1-39 (confirmed and open decisions), P1-40 (R1–R6, B1, B2, H1–H3, M2, M3, and §7 for A2/A3), ADR-0008, ADR-0009 and SECURITY.md.

**Outcome:** **staging remains NO-GO.** No AWS resource, permission, deployment, migration or AWS API call was made. This record decides nothing open: D8 unapproved; D9 unresolved; **D10 unresolved**; D11 deferred; **`prod-global` unresolved**; INFRA-AUTH (A7) unapproved. **Exotel AgentStream production media NO-GO.**

---

## 1. Verified repository state (2026-10-15)

- **Git:** HEAD `06e276d8a78467360b9ed6d5554b445a88b94adc`; 0 staged. `infra/k8s/base/deployment.yaml` is still deleted (` D`).
- **User-owned files:** `packages/go/telephony/{lifecycle,runtime}.go` and the gateway's `cmd/server/{main,main_test}.go` are byte-identical to the recorded user diffs.
- **Gateway module:** `go mod verify` → all modules verified. `GOWORK=off go mod tidy -diff` is clean. The AWS SDK set matches P1-40 §7.1; this audit made **no dependency change**.
- **Wiring:** `cmd/server/main.go:100` calls `app.WireFence(…, os.LookupEnv, …)`, so the IAM path is reachable in the shipped binary. The default adapter is `rdsauth.NewAWS` (`internal/app/iam.go`).
- **Image:** `docker/go/Dockerfile:153-167` builds `FROM scratch` with `/etc/ssl/certs/ca-certificates.crt` copied in, so STS HTTPS has trust roots. It runs as `USER 65534:65534`.
- **Infrastructure:** none exists. `infra/terraform/envs/{dev,staging,prod-in,prod-global}` and `infra/kubernetes/overlays/*` are empty and untracked (P1-37:14, P1-38:69, `infra/README.md`). There is no IaC for any account, EKS, OIDC, IAM, Aurora, VPC endpoints, CloudTrail, KMS, budgets or ESO.
- **Manifest:** `infra/k8s/base/services/telephony-gateway.yaml` (untracked; not modified here) has:
  - no ServiceAccount or IRSA annotation;
  - no fence or IAM environment variables;
  - no DSN or CA volumes;
  - no NetworkPolicy (B4 undecided);
  - `replicas: 2` with RollingUpdate.

  The staging design is separate A/B single-replica workloads with `strategy: Recreate` (P1-39 O-1); the staging overlay does not exist (A5).
- **CI:** only `cmd/server/{main,main_test}.go` and `go.mod` of the gateway are tracked. `internal/{fence,app,rdsauth}` are untracked and **never reach CI** until the owner commits them. CI uses `go test -race` (D8).

## 2. Readiness checklist

**READY** = verified in this repository. **BLOCKED** = needs an absent artefact or an unapproved gate. **NOT VERIFIED** = can only be proven in AWS or staging.

### 2.1 Code (gateway)

| Item | Status | Evidence |
|---|---|---|
| IAM mode, explicit auth mode, no password fallback (R2, R6) | READY | `internal/app/fence.go` (LoadFence/WireFence); tests in `iam_test.go` |
| Static checks: environment, region allow-list, `AWS_REGION`/`AWS_DEFAULT_REGION` match, 24 `PG*` variables, static AWS keys, IRSA variables (B2, R4, H1–H3) | READY | `internal/app/iam.go:42,62,86,108` |
| Raw and parsed DSN validation; verify-full, explicit CA, no `sslrootcert=system` (B1, R1, M2) | READY | `iam.go:229,248`; `internal/fence/tls_test.go` |
| **Writer cluster endpoint in the configured region (C1 no CNAME, C3 direct writer)** | READY (**new, §3.2**) | `iam.go:303`, `fence.go:249` |
| Fresh RDS token per connection; STS credentials cached until expiry | READY | `internal/rdsauth/rdsauth.go:141`; `aws_test.go` (two connections → 1 STS call, 2 tokens) |
| STS exchange bounded by the dial (hung STS abandoned) | READY | `aws.go:151`; `fence.go:240`; mutation-tested |
| SDK panic → fail closed | READY | `aws.go:155`, `rdsauth.go:157`; mutation-tested (the SDK really panics) |
| Expired or incomplete STS credentials refused | READY | `aws.go:164`; mutation-tested |
| No secrets in errors, logs or readiness; **failure category kept** | READY (**improved, §3.2**) | `fence.go:372-385,440,504`; `aws.go:90` |
| No static keys, profiles or `AWS_ENDPOINT_URL*` | READY | `aws.go:54`; P1-40 §7.2 |
| Real STS, real token acceptance, real Aurora login | **NOT VERIFIED** | requires staging (A7, A8) |
| `-race` | **NOT VERIFIED** | no cgo locally; D8 |

### 2.2 Platform and infrastructure (staging)

| Item | Status | Needed |
|---|---|---|
| Dedicated staging account, distinct from production (SEP) | BLOCKED | A6/A7. SEP evidence items 1–3 (P1-38:40). The AWS Organization's existence and its verifier are UNRESOLVED (P1-39). |
| No network path to production | BLOCKED | SEP evidence items 4/5 (flow logs, route/peering/TGW/VPN listings) |
| EKS (Graviton) with an IAM OIDC provider | BLOCKED | A6. The arm64 build host is per C2. |
| IRSA role for the gateway service account | BLOCKED | §4.2 |
| `rds-db:connect` for the fence user only | BLOCKED | §4.2 |
| Aurora PostgreSQL with IAM database authentication enabled | BLOCKED | A6; the engine version is UNRESOLVED (P1-34A:40) |
| Fence DB user: `LOGIN`, `rds_iam`, no table privileges, no password | BLOCKED | no SQL exists in the repo; A7 |
| Server-side TLS enforced (`rds.force_ssl`) | NOT VERIFIED | Aurora parameter-group default by engine version; check at A6 |
| CA bundle provenance: the AWS RDS regional bundle, pinned by checksum, mounted read-only | BLOCKED | A5/A6 (O-5; P1-39 prerequisite "DB CA/SAN/rotation verified") |
| Server certificate SAN covers the cluster writer endpoint | NOT VERIFIED | staging (verify-full depends on it) |
| STS reachable from pods (an STS interface VPC endpoint with private DNS, per ADR-0008:68), and Aurora 5432 reachable only from gateway pods | BLOCKED | A6; NetworkPolicy undecided (B4) |
| Token file readable by UID 65534 (projected token; pod `fsGroup`) | NOT VERIFIED | staging. Startup refuses an unreadable file statically (`iam.go` IRSA check). |
| The IRSA webhook's injected `AWS_REGION`/`AWS_DEFAULT_REGION` equal `FENCE_AWS_REGION` | NOT VERIFIED | staging; a mismatch stops startup (H3) |
| CloudTrail (`AssumeRoleWithWebIdentity`, session `callscreen-telephony-gateway-fence`) to the log archive | BLOCKED | SEP evidence item 7 |
| Staging manifests (`infra/kubernetes/overlays/staging/`): ServiceAccount, ExternalSecret, A/B, Recreate, anti-affinity | BLOCKED | A5 |
| Image built for arm64 and pushed to the staging-account registry | BLOCKED | A5/A6/A8; the image build with the new modules was **not run** |
| D10 timing values | BLOCKED | §5; the values come only from staging measurements |
| `HTTPS_PROXY` policy for STS | OPEN | §3.3 F-4 |

## 3. Security review of the IAM implementation

### 3.1 Verified behaviour

| Area | Finding |
|---|---|
| Fresh token per connection | `Source.Password` builds on every call and caches nothing (`rdsauth.go:141`). The fence dials only on acquisition or re-acquisition (`fence.go:240`), so a live session is unaffected by token or credential expiry; that is to be confirmed in staging (§5, E7). |
| Credential refresh | `aws.CredentialsCache` with `ExpiryWindow=0`: refresh happens on the first dial at or after expiry (P1-40 §7.2). STS session duration is the SDK default (no `DurationSeconds` sent). |
| Timeout and cancellation | The dial context (`HeartbeatTimeout`) bounds the STS exchange (re-bound inside the cache, `aws.go:151`), signing and connect. No new timing value. |
| Panics and errors | Provider panics are recovered inside the cache (`aws.go:155`); builder panics are recovered in the core (`rdsauth.go:157`). Error texts are never returned. |
| Region and endpoint | Explicit region, allow-listed per environment; webhook-injected regions must match. STS goes to the SDK's regional endpoint for that region. The DB host must now be the writer endpoint in the same region (§3.2). |
| TLS | verify-full, explicit CA file, hostname verification, no fallbacks (parsed-config invariants plus on-the-wire tests). An untrusted server receives **no token** (`aws_test.go`). |
| Secrets | Tokens are never logged (the fence logs only `redact` output). Test output scanned: no secret markers, `X-Amz-Signature` or access-key patterns in the `-v` logs of either run. |
| SDK version coupling | P1-40 §7.2's findings (nil dereference, suppressed context, `NewFromConfig`-only endpoint overrides, token format) are specific to v1.47.1 / v1.51.1 / v1.20.6 / v1.7.4. The tests `TestAWS_AHungSTS…`, `…FailClosedWithoutLeaking` and `…IgnoresAWSEnvironmentOverrides` turn regressions into failures. |

### 3.2 Defects found and fixed (code only)

| ID | Priority | Defect | Fix |
|---|---|---|---|
| F-1 | **High** (blocked D10 evidence and staging diagnosis) | `redact` (`fence.go:504`, before this change) collapsed every non-SQL failure into "database operation failed". `rdsauth` replaced every STS error with one sentinel. Staging could not tell an IRSA/STS trust failure, a TLS failure, a network failure and a DB authentication failure apart, so the planned "acquisition failures by phase" evidence and the stop conditions were unobservable. | `rdsauth.TokenError` carries a reason from a **fixed vocabulary** (`sts_access_denied`, `sts_invalid_identity_token`, `sts_expired_token`, `sts_identity_provider_error`, `sts_region_disabled`, `sts_throttled`, `sts_error`, `sts_unreachable`, `web_identity_token_unreadable`, `sts_credentials_unusable`, `token_builder_panic`, `token_empty`, `unknown`). It is derived only from error types and AWS-defined codes (`aws.go:90`), never from messages. `fence.CredentialsReason` keeps a reason only if it is 1–40 characters of `[a-z0-9_]` (`fence.go:385,440`). `redact` now also names "TLS certificate verification failed" and "network error" with no details. A local `tokenFile` replaces `stscreds.IdentityTokenFile`, whose error embeds the file path (`aws.go:77`); it also refuses an empty token file locally. |
| F-2 | **Medium** | C1 ("no CNAMEs") and C3 ("direct writer; no PgBouncer/RDS Proxy") were operator rules only. A reader, custom, proxy or instance endpoint, a CNAME or an IP address was accepted at startup. A region mismatch between the token's signing region and the cluster failed only at login. | `isWriterEndpoint` (`iam.go:303`, applied at `fence.go:249`) requires `<cluster>.cluster-<alnum id>.<FENCE_AWS_REGION>.rds.amazonaws.com` (case-insensitive) and refuses everything else at startup. The host is not echoed. |

**Tests added:**
- every STS error code mapped to its reason;
- an unreachable STS → `sts_unreachable`, without the address;
- the reason reaching the fence dialer with no message, ARN or request ID;
- reason vetting (free text, upper case, empty, over-long all dropped);
- network and TLS categories in `redact`;
- 13 endpoint cases.

**Mutation checks**, each restored byte-for-byte:
- writer check disabled → 11 refusals missing;
- `redact` dropping the reason → test fails;
- vetting removed → free text (including an ARN) leaks and the test fails.

**Behaviour changes to note:**
- **IAM DSN host:** must now be the writer cluster endpoint. That implements C1/C3 and decides nothing new, but an operator-supplied private DNS alias will now be refused by design.
- **Test hygiene:** a fake credential source in `iam_test.go` now fails before dialling, so no test resolves the test writer hostname (no DNS query to `amazonaws.com`).

### 3.3 Open findings (not fixed; owner decision or staging evidence needed)

| ID | Severity | Finding | Recommendation |
|---|---|---|---|
| F-3 | Low | With `ExpiryWindow=0`, credentials with seconds left can sign a token; if the login lands after expiry it fails (fail closed) and the next attempt refreshes. It is an availability blip at most, and only on re-dial. | Decide under D10 whether an early-refresh window is wanted. **No value proposed.** |
| F-4 | Low / policy | The SDK's HTTP client honours `HTTPS_PROXY`/`NO_PROXY` (`aws/transport/http/client.go:244`, `http.ProxyFromEnvironment`). TLS to STS is still verified, so a proxy sees metadata, not credentials. The manifest sets no proxy. | Owner decision: **(a)** refuse startup in IAM mode if a proxy variable is set (consistent with the static-key rule), or **(b)** ignore proxies for STS (direct via a VPC endpoint), or **(c)** accept and document. Recommended: (a), since it fails closed and is reversible. |
| F-5 | Low | A malformed STS response body (non-XML) is reported as `unknown`: the SDK gives it no code. | Acceptable; the reason is still distinct from TLS, network and DB failures. |
| F-6 | Info | The gateway's `internal/*` packages are untracked, so none of this code is exercised by CI until committed (owner action), and CI's `-race` is still unproven (D8). | Owner: commit when ready. D8 decides CI. |
| F-7 | Info | The manifest's `replicas: 2` with RollingUpdate differs from the staging A/B Recreate design. The fence still prevents two owners, but the staging test needs the P1-39 O-1 shape. | A5 (staging overlay); the base manifest is not changed here. |

## 4. Staging prerequisites (for A6/A7 authors)

### 4.1 Account and environment

- **Account separation (SEP):** a dedicated staging account, with all eight SEP evidence items (P1-38:40) before any gateway starts:
  1. distinct account ID;
  2. SCPs;
  3. no cross-account assume into production;
  4. and 5. network evidence: routes, peering, TGW, VPN, flow logs;
  6. the DB owner's confirmation of synthetic data;
  7. CloudTrail in the log archive;
  8. read-only secret and CA mount specs.

  Organization verification is **read-only** (P1-39).
- **Region:** `ap-south-1` only (R4b). Region-lock SCP per ADR-0008:187.
- **Synthetic data only;** no carrier credentials in staging (Exotel production media NO-GO is unaffected).

### 4.2 Identity

**IRSA role:**
- trust: `Federated` = the staging cluster's OIDC provider; `Action: sts:AssumeRoleWithWebIdentity`;
- `StringEquals` on `<issuer>:sub = system:serviceaccount:<namespace>:<serviceaccount>` **and** `<issuer>:aud = sts.amazonaws.com`;
- one role per service account (A and B may share a service account or have one each; owner's choice under A5);
- no wildcards in `sub`.

**Runtime permission (the only one the gateway needs):**
- `rds-db:connect` on `arn:aws:rds-db:ap-south-1:<staging-account>:dbuser:<DbClusterResourceId>/<fence-user>`;
- no `rds:*`, no `sts:AssumeRole` chaining, no Secrets Manager for this path.

  The STS web-identity call itself is unsigned and needs no permission.

**Setup permissions** (operator or Terraform principal, not the gateway):

| Purpose | Permissions |
|---|---|
| Aurora create/modify | `rds:CreateDBCluster`/`ModifyDBCluster` with `EnableIAMDatabaseAuthentication` |
| Role and policy creation | `iam:CreateRole`, `iam:PutRolePolicy`/`AttachRolePolicy`, `iam:CreateOpenIDConnectProvider` (or EKS-managed) |
| Cluster | `eks:*` for cluster creation |
| Network | `ec2:*` for the VPC, endpoints and security groups |
| Encryption and audit | `kms:*` for the CMK; `cloudtrail:*` |
| CA bundle delivery | ESO's read of the secret/parameter holding the CA bundle |

These belong to INFRA-AUTH (A7) and must be time-bound and audited.

**Static AWS keys:** never.
- The gateway refuses `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` and `AWS_SESSION_TOKEN` at startup.
- CI uses OIDC federation (SECURITY.md).
- No secret is ever pasted into chat or committed.

### 4.3 Database

- **Aurora PostgreSQL** (Serverless v2 per ADR-0009:91; engine version UNRESOLVED): IAM DB authentication **enabled**; storage encrypted (CMK, ADR-0008:185); deletion protection on.
- **Server-side TLS:** `rds.force_ssl=1` confirmed in the cluster parameter group.
- **Fence user (applied in staging only, by the DB owner, under A7):**

  ```sql
  CREATE ROLE <fence-user> LOGIN;
  GRANT rds_iam TO <fence-user>;
  ```

  - No password and no table privileges.
  - `CONNECT` only on the telephony database (revoke `PUBLIC` `CONNECT` if the database owner agrees).
  - Advisory locks, `pg_locks` and `pg_is_in_recovery()` need no extra grants (the local real-PG tests ran with a role that has no table privileges).
  - **NOT VERIFIED on Aurora:** that `rds_iam` disables password login for the role.
- **DSN:**

  ```
  postgres://<fence-user>@<cluster>.cluster-<id>.ap-south-1.rds.amazonaws.com:5432/<db>?sslmode=verify-full&sslrootcert=/<mounted>/rds-ca.pem&target_session_attrs=read-write
  ```

  It is delivered as a mounted file (ESO); it contains no secret, but it is still a file per SECURITY.md.
- **CA bundle:** the AWS-published regional RDS CA bundle, recorded with its source URL and SHA-256 at A6, mounted read-only, with a rotation plan (P1-39 prerequisite).

### 4.4 Network, isolation and logging

**Network paths:**
- **Egress from gateway pods:** Aurora writer:5432 and the STS endpoint:443 (an interface VPC endpoint with private DNS, so `sts.ap-south-1.amazonaws.com` resolves privately), plus DNS.
- **Aurora security group:** ingress 5432 only from the gateway pods' security group (security groups for pods) or the dedicated node group's security group.
- **NetworkPolicy:** pending B4.

**Isolation:**
- A and B on different Graviton nodes (C2/O-1);
- `runAsNonRoot`, read-only root, drop ALL (already in the base manifest);
- `fsGroup` so UID 65534 can read the projected token.

**Logging:**
- **Gateway logs:** reasons only (§3.2); sink-side redaction per SECURITY.md.
- **CloudTrail:** STS `AssumeRoleWithWebIdentity` (session name `callscreen-telephony-gateway-fence`).
- **Aurora:** `log_connections`/`log_disconnections` for the fence user, exported to CloudWatch for the test window.
- **VPC flow logs:** SEP evidence item 5.

### 4.5 Cost categories (no prices claimed; verify against the AWS pricing pages at A6)

| Category | Billed as |
|---|---|
| EKS control plane | per hour |
| EC2 Graviton nodes | per hour; EBS volumes |
| Aurora PostgreSQL Serverless v2 | capacity units per hour, storage, I/O (configuration-dependent), backups beyond the free allowance |
| NAT gateway (if any) | per hour plus data processed |
| Interface VPC endpoints (STS, ECR, logs, secrets) | per endpoint per AZ per hour, plus data |
| KMS customer-managed keys | per key per month, plus requests |
| Secrets Manager or Parameter Store advanced parameters | per secret or parameter (for ESO) |
| CloudWatch Logs | ingestion and storage; **VPC flow logs** and RDS log export add to it |
| CloudTrail | the first management-event trail copy is free, other copies and data events cost; log-archive S3 storage |
| ECR | storage |
| Data transfer | cross-AZ traffic |

**Expected no-charge items:** STS `AssumeRoleWithWebIdentity` and RDS IAM token generation (signing is local). **Confirm at A6.**

**Recommendation:** an AWS Budget with alerts on the staging account before A7, and teardown at the end of each test window (§4.6).

### 4.6 Rollback, cleanup, evidence and stopping conditions

- **Stop immediately and fail the run if:**
  - any interval with two owners is observed;
  - any token, credential, DSN with a secret, or web-identity token appears in logs or evidence;
  - the gateway authenticates by any path other than IAM;
  - TLS verification is bypassed;
  - any path to a production account or network is found;
  - the budget alert fires;
  - a live carrier is reachable.
- **Rollback:** scale A/B to 0; detach `rds-db:connect`; disable the IRSA role's trust; `REVOKE rds_iam` / `DROP ROLE` for the fence user; destroy the staging stack via Terraform (A6 state); confirm in the account inventory that nothing remains.
- **Evidence:** SEP items, test logs (reasons only), CloudTrail extracts, flow-log extracts, measurement datasets (§5), commit and image digests. Stored in the log-archive account under the retention ADR-0008:192 implies (immutable); no secrets.

## 5. D10 measurement plan (no values proposed)

**Values D10 must set from evidence:**
- `FENCE_HEARTBEAT_INTERVAL`;
- `FENCE_HEARTBEAT_TIMEOUT`, which bounds the **whole dial: STS exchange (when the credentials cache is cold or expired) + signing + TCP + TLS + PostgreSQL startup and IAM authentication**, and every heartbeat query;
- the derived validity/grace window (interval + timeout);
- optionally the credential early-refresh window (F-3).

**Evidence**, each as a distribution with p50/p99/p99.9/max, sample count and time span:

| # | Measurement | Conditions |
|---|---|---|
| E1 | STS `AssumeRoleWithWebIdentity` latency, cold (pod start) and at refresh; failure and throttle rates | via the VPC endpoint; across days and times |
| E2 | Token signing time (local) | expected negligible; confirm |
| E3 | TCP connect, TLS handshake, PostgreSQL startup + IAM auth latency, separately | warm and after idle |
| E4 | Heartbeat round trip (the `Holds` query) under normal and loaded writer | Aurora scaling events included |
| E5 | Dial failures by reason (§3.2 vocabulary) and by phase | whole test window |
| E6 | Time for the server to release a dead session's advisory lock (partition, pod kill, node loss) | with the TCP keepalive / idle settings actually configured |
| E7 | Live session survives credential and token expiry (no re-auth needed) | run > STS session duration |
| E8 | Aurora writer failover: time to new primary, re-acquisition time | forced failover (A9 drill) |
| E9 | STS outage: a live owner keeps ownership; a new acquisition fails closed with `sts_unreachable` | block the STS endpoint via security group (A9 drill) |

**How to measure:** E1–E3 need per-phase timing that the gateway **does not emit yet**. The next code-only step (§7) adds it.
- **Phase durations:** the credential phase, connect phase, and primary/lock queries as durations in the existing fence log lines, plus the reason.
- **No identifiers:** no host, user or values beyond durations and fixed reasons.
- **Metrics:** O-2's five metrics remain design-only, and their scraping method is open, so the log fields are the minimum and don't pre-empt O-2.

**Overlap validation (no two owners), using what the local tests do, scaled up:**
1. Gateways A and B (single replicas, different nodes) each log ownership transitions (owned/lost) with wall-clock and monotonic timestamps.
2. A separate observer samples, at a fixed cadence, both pods' readiness and the database's advisory-lock holder (`pg_locks` joined to `pg_stat_activity` by the fence user's application name). The observer uses its own read-only role, not the fence user.
3. **Pass criterion:** in every scenario (steady state, pod kill, node drain, network partition of the owner, process pause, writer failover, STS outage, credential expiry) the intervals in which A and B report ownership never intersect. The comparison includes the measured cross-node clock error (chrony/Amazon Time Sync offset recorded with each run). There must also be zero observer samples with both pods ready.
4. **Grace check:** for each hand-over, record the gap between the old owner's self-fence time and the new owner's "owned" time. The candidate D10 values must keep that gap ≥ 0 at the measured p99.9 of the dial and heartbeat latencies.
5. A failure of any criterion is a stop condition (§4.6). D10 values are chosen by the owner from these datasets. This record proposes none.

## 6. Remaining blockers (explicit)

1. **INFRA-AUTH (A7)** and its prerequisites:
   - A5 (staging manifests);
   - A6 (staging IaC: account, EKS/OIDC, Aurora with IAM auth, VPC endpoints, CloudTrail, flow logs, ESO, KMS);
   - the SEP evidence items;
   - Organization existence and verifier (UNRESOLVED).
2. **A8** (deploy) and **A9** (drills): unapproved.
3. **D10** (timing values): unresolved; it needs §5's evidence.
4. **`prod-global`** region policy: unresolved (P1-40 §4).
5. **Aurora engine version:** UNRESOLVED (P1-34A:40).
6. **F-4 proxy policy:** owner decision.
7. **B4** (NetworkPolicy/ingress/TLS): undecided.
8. **D8** (CI/`-race`): unapproved. **D9** unresolved. **D11** deferred.

**Credentials, accounts and paid services needed later**, none requested now:
- an AWS staging account and an operator principal with time-bound A6/A7 permissions;
- the EKS OIDC provider;
- a budget owner.

The gateway itself needs only IRSA and `rds-db:connect`. **No static key, password or API key is required for the IAM path**, and none should be shared in chat.

## 7. Next safe step (no cloud changes)

Add **dial-phase timing** to the fence's existing log lines:
- the credential acquisition duration;
- the connect duration (TCP + TLS + authentication);
- the primary-check and lock-query durations;
- the §3.2 reason.

Test offline with the existing fakes (fake STS, fake TLS PostgreSQL, throwaway PostgreSQL). This is the instrumentation that §5 (E1–E5) needs, and it adds no timing value, metric, dependency or infrastructure.

**Optional (can wait):**
- F-4 option (a) once the owner chooses;
- an arm64 image build check once the owner allows a local image build;
- an operator runbook generated from §4 once A6 is authorized.

---

## 8. Addendum — fence phase-timing instrumentation (§7 done)

**Scope:** code and offline/disposable-PG tests only. **No D10 measurement has been collected;** this only prepares the instrumentation that §5 E1–E5 need.

**Not changed:**
- timeouts, thresholds, metrics, dependencies, defaults;
- ownership, readiness or heartbeat behaviour, credential refresh;
- F-3, F-4, D10, `prod-global`.

### 8.1 What is logged

Each log line below gains a slog group `phase` with **durations only**, one field per phase reached, from fixed names.

| Line (level) | Phases |
|---|---|
| `cannot open a database session` (Warn) | credentials, dns, tcp, tls, auth, session_check, connect (those reached) |
| `not a writable primary` (Error) | … + primary |
| `lock attempt failed` (Warn) | … + primary, lock |
| `lock held elsewhere; standing by` (**Debug, new**; a standby repeats it every interval) | … + primary, lock |
| `lock acquired` (Info) | … + primary, lock |
| `ownership confirmed` (**Debug, new**; one per heartbeat) | heartbeat |
| `ownership LOST` and `confirmation arrived after the validity window` (Error) | heartbeat |

- **JSON output:** the JSON handler writes durations as integer nanoseconds.
- **Clock:** durations come from `time.Now`/`time.Since` (monotonic), never from the fence's ownership clock.
- **Code:** `internal/fence/timing.go`; hooks at `fence.go:240-316`.
- **Staging collection** needs the Debug lines, so the log level must be `debug` for the measurement window (`CS_TELEPHONY_GATEWAY_LOG_LEVEL`; an operator setting, not changed here).

### 8.2 What can and cannot be measured separately

| Phase | Measured at | Separately measurable? |
|---|---|---|
| credentials | around `Credentials.Password` | **Yes**, as a whole. *(Split into STS vs signing, with a refresh/reuse outcome, in §9.)* |
| dns | pgconn `LookupFunc` | Yes (called even for IP literals) |
| tcp | pgconn `DialFunc` | Yes |
| tls | `AfterNetConnect`: an **explicit** `HandshakeContext` | Yes. pgconn otherwise does the handshake lazily inside the startup write, which would fold it into `auth`. The explicit handshake uses the same `tls.Config` and the same dial context. |
| auth | the end of `AfterNetConnect` → the start of `ValidateConnect` (or the end of the connect when there is none, or on failure) | Yes. It includes the startup message, the server's IAM token check, and any parameter-status traffic before validation. |
| session_check | `ValidateConnect` (`target_session_attrs=read-write`) | Yes (IAM mode always has it) |
| connect | the whole `pgx.ConnectConfig` | Yes |
| primary, lock, heartbeat | around the respective queries | Yes (one network round trip plus server time each; not split) |

**Multi-host fallbacks** (password mode only; refused in IAM mode) report the **last** attempt per phase.

### 8.3 Defect found during this increment (fixed before use)

pgconn v5.10.0 (`pgconn.go:366-368`) stores the conn returned by `AfterNetConnect` **before** closing it on error. A hook returning `nil` on a failed handshake therefore **panicked** (nil pointer) on an untrusted server certificate. The hook now returns the original conn with the error.
- **Regression test:** `TestTiming_AnUntrustedCertificateFailsClosedInARunningFence`.
- **Mutation check:** restoring the `nil` return reproduces the panic.
- **Why the older test didn't catch it:** the earlier TLS test dialled without a timing collector, so it never used the hook; a running fence always installs one.

### 8.4 Security review of the new fields

- **Field set:** the field names are a fixed set of 10 (`phaseOrder`). Values are durations; there are no hosts, ports, users, DSNs, tokens, credentials, ARNs, request IDs or raw errors, and no per-call IDs, so cardinality is bounded.
- **The `error` field** is unchanged: still `redact` output (context error, SQLSTATE, the TLS/network/credential-reason categories, or "details withheld").
- **Timing as a side channel:** durations reveal only latency, which operators need. The token-check duration (`auth`) is server-side and not attacker-observable through these logs.
- **Tests:** they parse the JSON records and assert every `phase` key is known and numeric. Fake secrets, hosts, ports, users, raw error texts and ARNs are asserted absent on the success, failure, heartbeat, loss and real-PG paths.

### 8.5 Verification

| Check | Result |
|---|---|
| gofmt / vet / `GOWORK=off go mod tidy -diff` / `go mod verify` / `GOWORK=off go test` | clean / clean / clean / all modules verified / exit 0 |
| Gateway offline `go test -count=1 -v ./...` | **75 pass, 0 fail, 15 skip** (top-level; the skips are real-PG tests) + **17 subtests pass** |
| Gateway with throwaway PG, `-count=2` | **180 pass, 0 fail, 0 skip** (top-level) + **34 subtests pass**; `TestPostgresFence_PhaseTimingOnARealAcquisition` passed twice; all 10 overlap probes reported "no overlap"; container removed |
| Mutation checks | (1) the hook returns `nil` → panic; (2) dial-failure line without phases → 2 tests fail; (3) TLS handshake left lazy → `tls` missing and the failure is billed to `auth`. All restored byte-for-byte. |
| `-race` | **not run** (no cgo/GCC) |

---

## 9. Addendum — STS credential vs RDS token-signing sub-phases

**Scope:** code and offline/disposable-PG tests only. **No staging data has been collected.**

**Not changed:**
- authentication, TLS, ownership, readiness, heartbeat;
- the credential cache's refresh policy (`ExpiryWindow` still 0; F-3 open);
- timeouts, D10, dependencies, F-4, `prod-global`.

### 9.1 SDK facts relied on (re-verified in the pinned v1.47.1 source)

- `aws.CredentialsCache.Retrieve` returns cached credentials **without calling the provider** while `HasKeys()` holds and they are not `Expired()`.
- Otherwise it starts **one** singleflight call that runs the provider (with a `suppressedContext` that embeds the caller's context, so `Value` passes through). Concurrent callers **join** that call and do not run the provider.
- `auth.BuildAuthToken` (v1.7.4) does `creds.Retrieve(ctx)` then `PresignHTTP`; nothing between them is observable from outside.

### 9.2 Implementation (`internal/rdsauth/aws.go`, `internal/fence/timing.go`)

- **`buildToken`** runs the same two steps `BuildAuthToken` runs, as separately timed calls:
  1. `provider.Retrieve`, through the existing cache and `guardedProvider`;
  2. `auth.BuildAuthToken`, with a provider that returns **only the credentials just retrieved**.

  The credentials are not stored anywhere new, and authentication is unchanged: same credentials, same signer, same token. All pre-existing IAM tests pass unchanged, including hung STS, panic, expired credentials and the 13 failure reasons.
- **Refresh detection** is observed, not inferred.
  - A per-call marker (`retrieval`) rides on the context into `guardedProvider`, which the cache runs **only** for the call that starts a refresh.
  - The marker records running → succeeded or failed. "Succeeded" is set only **after** the provider returned **and** validation passed (non-empty keys and session token, an expiry in the future).
  - The marker also carries the dial context, replacing the former `dialKey`; the dial-bound cancellation is unchanged.
- **Fence seam:** `fence.RecordCredentialPhase` accepts only `credentials_sts` and `credentials_sign`. `fence.RecordSTSCredentials` accepts only the fixed outcomes. Anything else is ignored. The collector type is now exported (`fence.Timings`) so a source and its tests can use it; its recording methods stay unexported.

### 9.3 Exact boundaries

| Field (in the `phase` group) | Starts | Ends | Present when |
|---|---|---|---|
| `credentials` (fence) | before `Credentials.Password` | when it returns to the dialer | always on a credential dial |
| `credentials_sts` | before `provider.Retrieve` (the cache) | when the cache returns credentials or an error. A hit is a lookup; a refresh includes the token-file read, `AssumeRoleWithWebIdentity` (with the SDK's retries) and validation. | the cache returned before the dial gave up |
| `credentials_sign` | before `auth.BuildAuthToken` with the retrieved credentials | when the token (or an error) returns. Local SigV4 presigning only. | credentials were obtained |
| `sts_credentials` (string) | — | set once Retrieve has returned | same as `credentials_sts` |

**Nesting:** `credentials_sts` and `credentials_sign` are sequential and disjoint, and both lie **inside** `credentials` (which also includes the hand-off to the builder goroutine). **Do not add** them to `credentials`. The same applies to `connect` vs dns/tcp/tls/auth/session_check (§8). The test `TestAWS_SubPhasesNestInsideTheCredentialsPhase` asserts `credentials_sts + credentials_sign ≤ credentials`.

**`sts_credentials` values (finite):**

| Value | Meaning |
|---|---|
| `reused` | valid credentials, and this call did not run the exchange (cached; or it joined an exchange another caller started, which the fence's single dialer makes unlikely) |
| `refreshed` | this call ran the exchange, it passed validation, **and** the credentials reached this call |
| `refresh_failed` | this call ran the exchange and it failed: error, panic, unusable or expired credentials, or an unreadable token file |
| `refresh_incomplete` | this call started the exchange but stopped waiting first (its dial context ended) |
| `unavailable` | no credentials, and this call did not run the exchange |

**Limitations:**
- **Timed-out dials:** when the dial context ends during credential preparation, `Source.Password` returns at once, so the sub-phase fields may be absent from that log line; `credentials` is always there.
- **Clock resolution:** on hosts with a coarse clock (observed on the Windows development host), sub-millisecond steps such as signing can read `0s`. Staging (Linux) has nanosecond resolution.
- **Retries:** `credentials_sts` includes the SDK's internal STS retries and backoff; individual attempts are not split.

### 9.4 Security review

- **What's added:** two duration fields and one enumerated string, all with fixed names and finite values.
- **What's never logged:**
  - credentials (key ID, secret, session token);
  - the RDS token, or any part of it;
  - the web-identity token or its file path;
  - the role ARN or session name;
  - hosts, users, DSNs, SDK error text, request IDs.
- **No per-call ID.** The marker is a pointer in a context value, never logged.
- **Fail-closed behaviour is unchanged.** Errors still go through the §3.2 reason vocabulary.
- **The fake STS secrets, session token, key ID, token contents, role ARN, file path, session name, host, user and `X-Amz`/`Action=connect` text** are asserted absent from a running fence's JSON log (`TestAWS_RunningFenceLogsSubPhasesWithoutSecrets`). Scanning all test output found no secret markers.

### 9.5 Verification

| Check | Result |
|---|---|
| gofmt / vet / `GOWORK=off go mod tidy -diff` / `go mod verify` / `GOWORK=off go test` | clean / clean / clean / all modules verified / exit 0 |
| Gateway offline `go test -count=1 -v ./...` | **82 pass, 0 fail, 15 skip** (top-level; the skips are real-PG tests) + **21 subtests pass** |
| Gateway with throwaway PG, `-count=2` | **194 pass, 0 fail, 0 skip** (top-level) + **42 subtests pass**; all 10 overlap probes "no overlap"; container removed |
| New tests stable | the sub-phase tests pass under `-count=3` |
| Mutation checks | (1) failures marked "succeeded" → 4 failure cases fail (and the outcome table still never yields `refreshed`); (2) marker never set → the refresh/failure outcome tests fail; (3) `unavailable` mapped to `reused` → the outcome-table test fails. All restored byte-for-byte. |
| `-race` | **not run** (no cgo/GCC) |

---

## 10. Addendum — F-8: logged names of the credential timing fields (P1-43)

**Finding (P1-42 §3.1):** the gateway's real logger (`platform.NewLogger`, whose `RedactingHandler` matches deny-listed substrings in keys at any group depth) replaced the §9 fields `credentials`, `credentials_sts`, `credentials_sign` and `sts_credentials` with `"[REDACTED]"`, because each contains `credential`. The §8/§9 tests used plain `slog` handlers and could not see it.

**Fix (code only; boundaries and outcome meanings unchanged):**

| §8/§9 name | Logged name since P1-43 |
|---|---|
| `credentials` | `login_prepare` |
| `credentials_sts` | `login_prepare_sts` |
| `credentials_sign` | `login_prepare_sign` |
| `sts_credentials` | `sts_refresh` |

- **Unchanged:** Go identifiers (`fence.PhaseCredentials*`; the outcome key is the new constant `fence.FieldSTSOutcome`), the platform package and its redaction, authentication, dependencies, manifests and CI.
- **Tests through the real logger:** `TestGatewayLogger_TimingFieldsSurviveRedaction`, `TestGatewayLogger_FailedPreparationKeepsReachedPhasesOnly` and `TestAWS_SubPhasesSurviveTheGatewayLogger`. Details and mutation checks: P1-42 §3.1.
- **Verification:**
  - gofmt, vet, `GOWORK=off go mod tidy -diff`, `go mod verify` ("all modules verified") and `GOWORK=off go test`: clean;
  - offline: **85 pass, 0 fail, 15 skip** (top-level) + **25 subtests**;
  - disposable PG `-count=2`: **200 pass, 0 fail, 0 skip** + **50 subtests**; 10/10 overlap probes "no overlap"; container removed;
  - `-race` **not run** (no cgo).
- **Status:** fixed in code. The gateway code is still untracked (F-6), so the fix is in no image yet. E1/E2 remain **unmeasured**, and D10 remains unresolved.
