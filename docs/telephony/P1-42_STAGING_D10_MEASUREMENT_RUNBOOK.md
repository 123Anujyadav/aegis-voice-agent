# P1-42 — Staging measurement runbook for RDS IAM D10 evidence

**Phase 19, P1-42.** Documentation only. No code, test, dependency, manifest, CI or ADR change was made.

**Inputs:**
- P1-41 §5 (the D10 measurement plan and evidence items E1–E9), §8 and §9 (the phase-timing fields);
- P1-32 §4–§6 (scenarios S1–S10, acceptance criteria, D10 worksheet semantics);
- P1-39 §3 (prerequisites);
- the source files listed in §2.

**Status:** this is a procedure for a **future, separately authorised** staging run. It authorises nothing.
- Staging is **NO-GO**.
- INFRA-AUTH (A7), deploy (A8) and drills (A9) are unapproved.
- **D10 is unresolved.**
- **Exotel AgentStream production media is NO-GO.**

Nothing here has been measured. No value, threshold or timeout is proposed.

> **F-8 (§3.1), fixed in code by P1-43:** the shipped gateway logger used to turn all four credential timing fields into `"[REDACTED]"`. They were renamed (table below) and now survive the real logger; regression tests go through `platform.NewLogger`. The fix is in the untracked gateway code: it reaches a staging image only once the owner commits it (F-6). **E1 and E2 remain unmeasured.**
>
> | Old field (redacted) | New field | Meaning (unchanged) |
> |---|---|---|
> | `credentials` | `login_prepare` | the whole `Credentials.Password` call: preparing this connection's login secret |
> | `credentials_sts` | `login_prepare_sts` | obtaining the STS credentials: a cache lookup, or this call's STS exchange |
> | `credentials_sign` | `login_prepare_sign` | signing the RDS token locally |
> | `sts_credentials` | `sts_refresh` | the outcome: `reused`, `refreshed`, `refresh_failed`, `refresh_incomplete` or `unavailable` |
>
> Go identifiers are unchanged (`fence.PhaseCredentials`, `PhaseCredentialsSTS`, `PhaseCredentialsSign`; the outcome key is the new constant `fence.FieldSTSOutcome`).

---

## 1. Purpose and safety gates

**Purpose:** collect the distributions P1-41 §5 lists, so the owner can choose `FENCE_HEARTBEAT_INTERVAL` (I) and `FENCE_HEARTBEAT_TIMEOUT` (T). The derived window is V = I + T (`fence.go:107`). The owner may also decide on an early-refresh window (F-3).

**This runbook does NOT authorise:**
- any AWS API call, console session or credential use;
- connecting to staging;
- deployment, infrastructure change or database migration;
- creating the fence role;
- production testing or live carrier calls.

**Gates that must all be closed, in writing, before §4 is executed:**

| Gate | Source | Status today |
|---|---|---|
| SEP evidence items 1–8 (separate account, no production path, synthetic data) | P1-38:40, P1-41 §4.1 | open |
| A5 staging overlay: A/B single replicas, `strategy: Recreate`, ServiceAccount, mounts | P1-39 O-1, P1-41 §2.2 | open (F-7) |
| A6 staging IaC; **A7 INFRA-AUTH**; A8 deploy | P1-41 §6 | unapproved |
| A9 per-drill approval (needed for E6–E9 scenarios) | P1-39 §3 item 6 | unapproved |
| D10 **run-configuration** candidates recorded as candidates only | P1-39 §3 item 5 | none recorded |
| F-8 fix (P1-43) committed and present in the image under test (§3.1) | P1-43 | fixed in code and verified offline; **not committed**, no image |
| CA bundle provenance, SAN and rotation verified | P1-37 O-5 | open |
| Budget alert on the staging account | P1-41 §4.5 | open |

If any gate is open, **stop**. Do not substitute a workaround.

## 2. Source-verified facts this runbook depends on

Each fact below was read from the working tree on 2026-10-15 (HEAD `06e276d`; the gateway's `internal/*` packages are untracked).

### 2.1 Log records that carry timing (`internal/fence/fence.go`)

| Message (exact text, prefix `fence: `) | Level | Line | Extra fields | Phases present (if reached) |
|---|---|---|---|---|
| `cannot open a database session` | WARN | 245 | `error` | login_prepare, login_prepare_sts, login_prepare_sign, dns, tcp, tls, auth, session_check, connect; `sts_refresh` |
| `the database is not a writable primary; refusing to take the lock` | ERROR | 255 | `error` | dial phases + primary |
| `lock attempt failed` | WARN | 270 | `error` | dial phases + primary, lock |
| `lock held elsewhere; standing by` | **DEBUG** | 276 | — | dial phases + primary, lock |
| `lock acquired; owner after the grace period` | INFO | 283 | `grace` (= V) | dial phases + primary, lock |
| `ownership confirmed` | **DEBUG** | 316 | — | heartbeat |
| `ownership LOST; no longer accepting work` | ERROR | 299 | `lock_missing`, `error` | heartbeat |
| `confirmation arrived after the validity window; ownership dropped` | ERROR | 310 | — | heartbeat |

Notes:
- **Phase group:** all phases sit in a JSON object `phase`. The 12 names are fixed (`timing.go:71-97`). `sts_refresh` is a string with five values (`timing.go:91-121`).
- **Field encoding:** in the JSON handler a duration is an **integer number of nanoseconds**. JSON is the default (`LOG_FORMAT`, `platform/config.go:320`) and is mandatory in production (`config.go:391`); staging must use JSON too.
- **Record context:** every record also carries `time` (the wall clock at emission), `level`, `msg`, `service`, `version`, `environment` and `region` (`platform/logging.go:288-293`). Outside production it also carries `source`.
- **Log level:** the Debug lines need `CS_TELEPHONY_GATEWAY_LOG_LEVEL=debug`. The base manifest maps it from the configmap's `LOG_LEVEL: "info"` (`telephony-gateway.yaml:122-126`, `configmap.yaml:7`).

### 2.2 Timing boundaries (from source, not from earlier reports)

| Field | Boundary | Source |
|---|---|---|
| `login_prepare` | around `Credentials.Password` in the credential dialer | `fence.go:447` |
| `login_prepare_sts` | around `provider.Retrieve` (the credentials cache). A cache hit is a lookup. A refresh includes the token-file read, `AssumeRoleWithWebIdentity` with the SDK's retries and backoff, and validation. | `rdsauth/aws.go:87-90` |
| `login_prepare_sign` | around `auth.BuildAuthToken` with the retrieved credentials (local SigV4 presign) | `aws.go:96-99` |
| `sts_refresh` | set after Retrieve returns, from the per-call marker | `aws.go:91`, outcome map in `aws.go` `(*retrieval).outcome` |
| `dns` / `tcp` | pgconn `LookupFunc` / `DialFunc` | `timing.go:255-266` |
| `tls` | an explicit `HandshakeContext` in `AfterNetConnect` | `timing.go:271-278` |
| `auth` | the end of `AfterNetConnect` → the start of `ValidateConnect` (or the end of connect). It includes the startup message and the **server's** IAM token check. | `timing.go:289-309` |
| `session_check` | `ValidateConnect` (`target_session_attrs=read-write`) | `timing.go:292-299` |
| `connect` | the whole `pgx.ConnectConfig` | `timing.go:304` |
| `primary` | `SELECT NOT pg_is_in_recovery()` | `fence.go:252,476` |
| `lock` | `SELECT pg_try_advisory_lock($1)` | `fence.go:267,482` |
| `heartbeat` | the `Holds` query (`pg_locks` for this backend, plus a recovery check) | `fence.go:295,492-495` |

**Rules that follow from the code:**
1. **Durations nest; never add them up.**
   - `login_prepare` ⊇ `login_prepare_sts` + `login_prepare_sign`, plus hand-off overhead (asserted by `TestAWS_SubPhasesNestInsideTheCredentialsPhase`).
   - `connect` ⊇ dns, tcp, tls, auth and session_check.
   - The whole dial is `login_prepare` followed by `connect`; both are bounded by one dial context of length T (`fence.go:241`).
2. **Each record is one attempt.** The phases in one record belong to the same dial or heartbeat, so no request ID is needed to correlate them.
3. **The clock is monotonic per process** (`time.Since`). Durations do not depend on wall-clock synchronisation; record timestamps do.
4. **Durations are censored at T:**
   - a dial that exceeds T fails with `context deadline exceeded`;
   - a dial that ends during credential preparation may lack `login_prepare_sts`/`login_prepare_sign`/`sts_refresh` (`aws.go` doc comment; `rdsauth.go` `Password` returns at once on context end);
   - `primary`, `lock` and `heartbeat` are bounded by T individually (`fence.go:250,265,291`).
5. **The owner never re-dials while it holds the lock.**
   - The standby re-dials once per loop (`fence.go:227-233`: dial → primary → try-lock → release → sleep I).
   - So **the standby produces almost all dial samples**, including the periodic `refreshed` samples when its STS credentials expire.
   - The owner produces `heartbeat` samples.
6. **Release is neither timed nor logged.** `release()` (`fence.go:330-336`) runs unlock and close under T and ignores errors. `Shutdown` (`fence.go:340`) logs nothing.

### 2.3 Failure vocabulary in `error`

`redact` (`fence.go:517-546`) emits only:
- `context deadline exceeded` or `context canceled`;
- `fence: database credentials unavailable: <reason> (details withheld)`, where `<reason>` is one of the 13 `rdsauth` reasons (`rdsauth.go:45-57`): `sts_access_denied`, `sts_invalid_identity_token`, `sts_expired_token`, `sts_identity_provider_error`, `sts_region_disabled`, `sts_throttled`, `sts_error`, `sts_unreachable`, `web_identity_token_unreadable`, `sts_credentials_unusable`, `token_builder_panic`, `token_empty`, `unknown`;
- the same without a reason;
- `postgres error <SQLSTATE>`;
- `TLS certificate verification failed (details withheld)`;
- `network error reaching the database (details withheld)`;
- `database operation failed (details withheld)`.

The `error` key is not on the redactor's deny list, so these strings survive the platform logger.

## 3. Findings and gaps established while writing this runbook

### 3.1 F-8 (High for D10 evidence): the platform redactor erased the credential timing fields — fixed in code (P1-43)

The finding as recorded by P1-42 (old field names kept as history):

- **Cause:** `platform.NewLogger` wraps every handler in `RedactingHandler` (`logging.go:288`). It replaces any attribute whose lower-cased key **contains** a deny-list substring with `"[REDACTED]"`, recursing into groups (`logging.go:60-80,193-222`). `"credential"` is on that list. The gateway uses this logger for the fence (`cmd/server/main.go:84,100`).
- **Effect:** `phase.credentials`, `phase.credentials_sts`, `phase.credentials_sign` and `phase.sts_credentials` reach the sink as `"[REDACTED]"`. All other phase fields, `error`, `grace` and `lock_missing` are unaffected.
- **Verification:** a throwaway program in the session scratchpad (outside the repository) passed one record with all 12 phases and the outcome through `platform.NewRedactingHandler` over a JSON handler. Output:

  ```
  "phase":{"credentials":"[REDACTED]","credentials_sts":"[REDACTED]","credentials_sign":"[REDACTED]","dns":1,"tcp":1,"tls":1,"auth":1,"session_check":1,"connect":1,"primary":1,"lock":1,"heartbeat":1,"sts_credentials":"[REDACTED]"}
  ```
- **Why the §8/§9 tests did not catch it:** they log through plain `slog` JSON/Text handlers, not the platform handler.
- **Consequence:** E1 and E2 are **not collectable** from gateway logs today. E5's per-reason failure counts still work, because the reason is inside `error`.
- **Remedy (owner choice; not done here):**
  - **(a)** rename the four fields to keys outside the deny list. Note that `token` and `pin` are also on it, so names must avoid those substrings too.
  - **(b)** add exact-match entries to `alwaysAllowed` in `packages/go/platform`, a shared module (wider blast radius).

  Either needs an offline test that logs **through `platform.NewLogger`** and asserts the values are durations. Recommendation: (a), confined to the gateway.

**Resolution (P1-43): option (a).**
- **Rename:** the four logged names were changed (mapping at the top of this record). Boundaries and outcome meanings are unchanged. The platform package and its redaction were not touched.
- **Names checked against both lists:** the new names contain no deny-list substring (`credential`, `token`, `secret`, `password`, `pin`, `otp`, …) and no pseudonymised substring (`ip`, `contact`, …).
- **Regression tests** build the logger exactly as `cmd/server/main.go` does (`platform.LoadConfig` with the `CS_TELEPHONY_GATEWAY` prefix → `Validate` → `platform.NewLogger`):
  - `TestGatewayLogger_TimingFieldsSurviveRedaction` (staging and production configurations): all 12 phases keep exact integer-nanosecond values, at top level and inside an enclosing group, while `password` and nested `token`/`session_token` keys are still redacted;
  - `TestGatewayLogger_FailedPreparationKeepsReachedPhasesOnly`: in a running fence, a failed refresh logs only the phases it reached, `sts_refresh=refresh_failed` and the vetted reason; a source that records nothing logs `login_prepare` alone, with no outcome field;
  - `TestAWS_SubPhasesSurviveTheGatewayLogger`: a running fence with the fake STS logs `refreshed`, then `reused`, with one STS request, numeric sub-phases, no `[REDACTED]` and no secrets.
- **Mutation checks:** restoring any old name, or using a name containing `ip`, makes these tests fail.
- **Still unverified:** that the staging log pipeline (collector, sink-side redaction per SECURITY.md) also leaves these fields intact. Check it at the start of the run (§6.3 item 5).

### 3.2 Gaps (measurement limits from the code, not defects)

| ID | Gap | Effect on evidence | Handling in this runbook |
|---|---|---|---|
| G-1 | Ownership **start** (`Owned()` turning true at receipt + V) is not logged | owner intervals must be **inferred** | §4.6: conservative interval from logged times |
| G-2 | Self-fence by the clock (no confirmation within V) is not logged until the heartbeat returns or times out | the end of ownership is inferred | §4.6 |
| G-3 | Records carry only wall-clock `time`; there is no monotonic timestamp (P1-41 §5 step 1 assumed both) | cross-process comparisons need a measured clock offset | §4.7 |
| G-4 | SDK STS retries are inside `login_prepare_sts`; a throttle that later succeeded is invisible | E1 throttle **rate** is a lower bound from `sts_throttled` failures | only CloudTrail could add attempt counts (**unverified** whether retried attempts appear) |
| G-5 | Censoring at T (§2.2 rule 4) | upper tails above T appear only as failures | report censored counts separately; never treat them as missing |
| G-6 | Lock release, session close and server-side release time are not instrumented in the gateway | E6 comes **only** from the database observer | §4.6 |
| G-7 | `auth` combines network round trips with the server's IAM token check; there is no server-side split | database-side cost of IAM auth is **not separately measured** | Aurora `log_connections` timestamps (if enabled at A6) give DB-clock events, not durations |
| G-8 | `/readyz` aggregates every registered check (`internal/app/fence.go:357`) | "ready" ⇒ owned, but "not ready" does not imply not owned | readiness is a lower bound on ownership; logs are the primary source |
| G-9 | No pod or instance identity in records; the collector adds metadata | A/B labelling depends on the log pipeline | §4.5 |
| G-10 | Cold start: the first dial after process start must refresh, but "first after start" is inferred from record order | E1 "cold" samples are identified by position | §4.4 |

## 4. Collection procedure (future, authorised run only)

### 4.1 Preconditions to verify (record each in the worksheet §5.1)

1. All §1 gates are closed, with approver names and dates.
2. **Build provenance:**
   - the image digest and git commit are recorded;
   - the untracked `internal/{fence,app,rdsauth}` code has been committed by the owner (F-6);
   - the build includes the F-8 fix;
   - `go test` for the gateway passed on that commit.
3. **Two single-replica workloads, A and B, on different nodes** (P1-39 O-1). Base manifest F-7 (2 replicas, RollingUpdate) must **not** be used.
4. **Identical run configuration on A and B:** `FENCE_LOCK_KEY`, I, T, `FENCE_AUTH_MODE=iam`, `FENCE_AWS_REGION`.
   - Identical values are a safety requirement (P1-32 §6: V_new ≥ V_old).
   - I and T are the owner's recorded **run candidates**; this runbook supplies none.
5. **Logging:**
   - `CS_TELEPHONY_GATEWAY_LOG_LEVEL=debug` for the window only;
   - `LOG_FORMAT=json`;
   - the sink-side redaction from SECURITY.md is active.
6. **Database:**
   - IAM authentication is enabled;
   - `rds.force_ssl=1`;
   - the fence role is `LOGIN`, `rds_iam`, with no password and no table privileges (P1-41 §4.3);
   - a **separate read-only observer role** can read `pg_locks` and the fence sessions in `pg_stat_activity` (verify which grant is required on the engine version chosen at A6; not verified here).
7. **Instance identification at the database:** decide at A5 how the observer maps a lock holder to A or B without retaining addresses. The IAM DSN allow-list permits `application_name` (`internal/app/iam.go:69`); a distinct value per workload is one option (an owner choice; not proposed here as a value).
8. **Clock synchronisation** is active on every node and the observer host, and the method of reading the offset is fixed (§4.7).
9. **The secret-marker check is prepared** (P1-32 §4): the operator knows a marker unique to each sensitive value without printing it.
10. **Stop conditions (§6.3) are read aloud** to the run team, and a named person holds the stop authority.

### 4.2 Bounded debug-log window

1. Record the window start (UTC, from the observer host) and the planned end. The duration is the owner's choice; P1-32 S1 plans a 24-hour observation.
2. Enable debug only for A and B, by a staging overlay change approved under A8. There is no live edit of the base configmap.
3. Collect only the gateway container logs of A and B. Use an **allow-list extraction**: keep only `time`, `level`, `msg`, `error`, `phase.*`, `grace`, `lock_missing`, `service`, `version`, `environment` and `region`, plus the A/B label (§4.5). Drop everything else, including collector-added metadata.
4. At the planned end, revert to the approved level (`info` in the base configmap) and record the actual end time.
5. Do not extend the window without recording a new end and the reason.

### 4.3 Classifying records

| Class | Selector (`msg` and fields) | Feeds |
|---|---|---|
| Dial, success, standby | `lock held elsewhere; standing by` | E1–E3 (dial phases), primary, lock |
| Dial, success, acquirer | `lock acquired; owner after the grace period` | E1–E3, primary, lock; hand-over timing |
| Dial, failure | `cannot open a database session` | E5 (by `error` category and last phase present) |
| Not primary | `the database is not a writable primary; …` | E5, E8 |
| Lock query failure | `lock attempt failed` | E5 |
| Heartbeat, success | `ownership confirmed` | E4 |
| Heartbeat, failure | `ownership LOST; …` (`lock_missing` true or false) or `confirmation arrived after the validity window; …` | E4 tail, E5, E6–E9 |

**Within each class, split by `phase.sts_refresh`** (named `sts_credentials` before P1-43):

| Value | Use |
|---|---|
| `refreshed` | E1 at refresh. `login_prepare_sts` includes the STS exchange. |
| `reused` | the cache-hit baseline. `login_prepare_sts` is a lookup. |
| `refresh_failed` | E1 failure. Pair it with the `error` reason. |
| `refresh_incomplete` | the dial ended (T) during the exchange. Count as censored (G-5). |
| `unavailable` | no credentials and no exchange by this call. Count separately. |
| absent | the dial ended before Retrieve returned. Censored. |

**Never pool `refreshed` and `reused` samples.** Their `login_prepare_sts` distributions measure different things.

### 4.4 Correlating without identifiers

- **Within an attempt:** the record itself; all phases in one `phase` object belong to one attempt.
- **Across attempts:** order by `time` within one A/B label. Do not invent sequence numbers or request IDs, and do not add any to the gateway.
- **Cold start (G-10):** the first dial record after a process start (the orchestrator's container start time, recorded as a timestamp only) should be `refreshed`. Label it `cold`. Later `refreshed` records are `at_refresh`.
- **Hand-over:** pair the old owner's last `ownership confirmed` or `ownership LOST` record with the new owner's `lock acquired` record, using the timestamps only (§4.6).

### 4.5 Labelling and redaction of the dataset

- Replace pod names, node names, namespaces and any collector metadata with the labels **A** and **B** before the data leaves the collection host. Keep the mapping nowhere in the artifacts.
- Never retain: credentials, tokens, web-identity tokens or their path, DSNs, role ARNs, account IDs, cluster or database hostnames, IP addresses, user names, request IDs, or raw SDK or driver error text. §2.3 lists the only strings `error` can hold; anything else in `error` is a stop condition (§6.3).
- Run the secret-marker scan (§4.1 item 9) and a pattern scan over every artifact before retention:
  - `AKIA`/`ASIA` key prefixes;
  - `X-Amz-`, `Action=connect`;
  - `arn:aws:`;
  - `.rds.amazonaws.com`;
  - `postgres://`;
  - `eyJ` (JWT prefix);
  - the fence user name;
  - IPv4 patterns.

  Record the scan command and its zero-hit result, not the matches.

### 4.6 Independent overlap observer (no two owners)

The observer runs on a separate host, under its own read-only identities. It never uses the fence user, the gateway's IRSA role or the DSN file.

1. **Readiness sampling:** run `tools/scripts/fence-validation/readiness-overlap-monitor.sh` against A's and B's `/readyz` (port 8081, `telephony-gateway.yaml:160-163`), per P1-32 §4.
   - It samples sequentially and alternates the order. Its documented limits apply: misses shorter than the period, and false overlaps at a hand-over.
   - Record the measured period from its summary.
2. **Database sampling** (read-only, same cadence). For the advisory key, use the same split as `Holds` (`fence.go:489-495`): `classid` = high 32 bits, `objid` = low 32 bits, `objsubid = 1`.

   ```sql
   SELECT clock_timestamp() AS db_time,
          count(*) FILTER (WHERE l.granted) AS holders
   FROM pg_locks l
   WHERE l.locktype = 'advisory' AND l.classid = $1::oid AND l.objid = $2::oid AND l.objsubid = 1;
   ```

   Plus, under the §4.1 item 7 decision, the holder's A/B label via `pg_stat_activity` **mapped in memory**, retaining only `A`, `B` or `none`.
   - Holder transitions (A→none→B) give the **server-side lock release** time (E6).
   - Retain `db_time`, `holders` and the label. Retain no pid, address, user or query text.
3. **Owner intervals from logs** (conservative, widened so an overlap cannot hide):
   - **Start of ownership:** `lock acquired` record time − `phase.lock` + `grace`. The fence's real start is the TryLock receipt time + V, and receipt lies within the `lock` phase before the record (`fence.go:267-283`).
   - **End of ownership:** the earliest of:
     - the `ownership LOST` or `… validity window …` record time;
     - the last `ownership confirmed` record time + `grace`.

     The real end is the last confirmation's **send** time + V, and send precedes the record. With no LOST record (shutdown, kill, pause), use the second form. G-1/G-2 apply.
4. **Pass criteria** (P1-41 §5, P1-32 §5):
   - zero intervals in which the widened A and B intervals intersect, after adding the measured clock-offset bound (§4.7) to each end;
   - zero readiness rounds with both instances ready, unless each such round is shown to be a sequential-sampling artefact from both logs;
   - zero database samples with `holders > 1` (impossible for an exclusive lock; a non-zero value invalidates the observer).
5. **Grace check per hand-over:** gap = new start − old end (both from step 3). Record each gap. A negative gap is an overlap: stop (§6.3).

### 4.7 Clocks: client-observed vs database-side

| Quantity | Clock | Synchronisation needed |
|---|---|---|
| `phase.*` durations | each gateway process's monotonic clock | no |
| record `time` | the gateway node's wall clock | yes, for hand-over and overlap comparisons |
| readiness monitor rounds | the observer host's wall clock | yes |
| `db_time` | the database server's clock (`clock_timestamp()`) | yes; estimate its offset from the observer's request/response midpoint and record half the round trip as the bound |
| Aurora connection logs (if enabled) | the database server's clock | yes |

- **Record clock offsets:** at the window start, every hour, and at the end, record each node's and the observer's offset estimate and the method used. The method (for example `chronyc tracking` on the node, or Amazon Time Sync Service status) is fixed at A6. Node access itself needs approval.
- **Unknown offset:** if an offset cannot be obtained, the overlap result for that period is **inconclusive**, not a pass.
- **Database-side work:** client-observed durations include network time. Server-side work is **not** separately measured (G-7). Do not present `auth`, `primary`, `lock` or `heartbeat` as database execution time.

### 4.8 Statistics and recording rules

- **For every distribution, record:**
  - n;
  - the window (start, end);
  - min, p50, p99, p99.9 and max, by nearest rank on the raw samples;
  - the censored count (§4.3);
  - the failure count by `error` category.
- **Small samples:** p99.9 is reported only when n ≥ 1000; otherwise record "insufficient samples" (it would equal the maximum). This is an arithmetic rule, not a D10 value.
- **Missing observations** (a phase absent where expected, a gap in the readiness or DB samples, an unknown clock offset) are listed with their time spans. They are never interpolated.
- **No pooling across conditions:** do not mix `refreshed` and `reused`, warm and after-idle, normal and Aurora-scaling, or before and after a drill.
- **Raw data:** keep the redacted raw dataset (§4.5) so every number can be recomputed.

## 5. D10 evidence matrix and decision worksheet

**Evidence vs decision:** §5.2 records evidence. §5.3 records proposals and decisions; the owner fills them, with a rationale citing §5.2 rows.

**Nothing here is a measurement.** Every cell marked `____` is blank by design.

### 5.1 Run record

| Field | Value |
|---|---|
| Approvals (A7, A8, A9 per drill): approver, date | ____ |
| Commit, image digest, F-8 fix verified offline (yes/no) | ____ |
| Run configuration: I, T, V = I + T (candidates only; identical on A and B: yes/no) | ____ |
| Window start / end (UTC) | ____ |
| Clock method; offset bounds (start, hourly, end) | ____ |
| Readiness monitor period (measured) | ____ |
| DB observer period (measured) | ____ |
| Secret scans: commands run, hits (must be 0) | ____ |
| Deviations from this runbook | ____ |

### 5.2 Evidence matrix (P1-41 §5 definitions; fields from §2)

**Status key:**
- **direct:** a logged duration or count;
- **inferred:** derived from timestamps;
- **external:** observer or AWS-side;
- **not measured:** no source exists.

| # | P1-41 definition | Fields / source | Status | Required observations | Failure cases to capture | Observed (n, p50/p99/p99.9/max, failures, censored) |
|---|---|---|---|---|---|---|
| E1 | STS `AssumeRoleWithWebIdentity` latency, cold and at refresh; failure and throttle rates | `phase.login_prepare_sts` where `sts_refresh=refreshed` (split cold / at_refresh); `refresh_failed` with the `error` reason | direct (survives the gateway logger since P1-43); **unmeasured**; includes token-file read, retries and validation; throttle rate is a lower bound (G-4) | `refreshed` samples across days and times (the standby supplies them) | `sts_*` reasons, `web_identity_token_unreadable`, `sts_credentials_unusable`, `refresh_incomplete` | ____ |
| E1 (baseline) | (not a P1-41 item) the cache-hit cost | `login_prepare_sts` where `reused` | direct; **unmeasured** | — | `unavailable` | ____ |
| E2 | token signing time | `phase.login_prepare_sign` | direct (since P1-43); **unmeasured** | all dials with credentials | `token_empty`, `token_builder_panic` | ____ |
| E3 | TCP, TLS, PostgreSQL startup + IAM auth, separately; warm and after idle | `phase.dns`, `tcp`, `tls`, `auth`, `session_check`, `connect` | direct; `auth` includes the server IAM check (G-7) | standby dials (steady); first dial after an idle period (define "idle" in §5.3 before the run) | TLS-verification and network categories; `postgres error 28000`/`28P01`-class SQLSTATEs as logged | ____ |
| E3 (adjacent) | primary check, lock query | `phase.primary`, `phase.lock` | direct | every successful dial | not-primary records, `lock attempt failed` | ____ |
| E4 | heartbeat round trip under normal and loaded writer, including Aurora scaling | `phase.heartbeat` on `ownership confirmed` (DEBUG) | direct | owner heartbeats; scaling events timestamped from AWS-side records (method fixed at A6) | `ownership LOST` (`lock_missing`, `error`), the validity-window record | ____ |
| E5 | dial failures by reason and by phase | `cannot open a database session`, `not … primary`, `lock attempt failed`: `error` category plus the last phase present | direct (`error` survives redaction) | counts per category and per last phase, over the whole window | all of §2.3 | ____ |
| E6 | server release of a dead session's advisory lock | observer holder transitions (§4.6) | **external only** (G-6) | per drill (A9): partition, pod kill, node loss | lock never released; release slower than the window planned for the drill | ____ |
| E7 | a live session survives credential and token expiry | owner `ownership confirmed` continuity across > one STS session duration; the standby's `refreshed` records mark expiry | inferred; the STS session duration is the SDK default (P1-41 §3.1) and is **not logged** | one owner tenure longer than the STS session duration | any LOST without a network or DB cause | ____ |
| E8 | writer failover time and re-acquisition | not-primary records, LOST records, `lock acquired`, plus observer holder transitions | inferred + external | the A9 failover drill | overlap; no re-acquisition | ____ |
| E9 | STS outage: the live owner keeps ownership; new acquisitions fail closed with `sts_unreachable` | owner heartbeats continue; standby `refresh_failed`/`refresh_incomplete` with `sts_unreachable` once its cache expires (fresh-cache dials stay `reused`) | direct + inferred | the A9 STS-block drill, lasting past the standby's credential expiry | ownership change during the outage; any non-IAM authentication | ____ |

E1–E5 are fully defined in P1-41 §5. E6–E9 are included because the safety questions here (credential expiry, STS failure, lock release) are those items, not E1–E5.

### 5.3 Decision worksheet (owner fills; evidence rows referenced by number)

| Decision | Evidence rows used | Proposed value | Derivation / rationale | Reviewer | Decision (approve / reject / defer), date |
|---|---|---|---|---|---|
| T (`FENCE_HEARTBEAT_TIMEOUT`): must cover the whole dial, including a **refreshed** credential preparation (E1 + E2 + E3 + primary + lock), and each heartbeat (E4) | ____ | ____ | ____ | ____ | ____ |
| I (`FENCE_HEARTBEAT_INTERVAL`): T < I is enforced (`fence.go:96`) | ____ | ____ | ____ | ____ | ____ |
| V = I + T against the detection and takeover targets (P1-32 §6) | ____ | ____ | ____ | ____ | ____ |
| Definition of "after idle" for E3 (must be fixed **before** the run) | — | ____ | ____ | ____ | ____ |
| F-3 early-refresh window: **only** if E1 at-refresh tails or E5 show expiry-related failures; otherwise "none" is a valid decision | ____ | ____ | ____ | ____ | ____ |
| Spurious-loss threshold (P1-32 §5) | ____ | ____ | ____ | ____ | ____ |
| Accepted unmeasured items (G-1 … G-10) | — | — | ____ | ____ | ____ |

## 6. Safety, retention and cleanup

### 6.1 Never in any artifact

- Credentials, session tokens or RDS tokens.
- Web-identity tokens or their file path.
- DSNs or their components (host, user, database).
- Role ARNs, account IDs or the role-session name.
- Database or cluster hostnames.
- IP addresses, request IDs, or raw SDK or driver error text.

The `[REDACTED]` marker must not be "recovered" from any other source.

### 6.2 Retention

- **Where:** the redacted datasets, worksheet, scan results, monitor CSVs and observer samples go to the log-archive account, immutable (P1-41 §4.6). The retention period is set per ADR-0008 by the owner: ____ .
- **What is destroyed:** unredacted collection-host copies are destroyed at the end of the run, and the destruction is recorded in §5.1.
- **Access:** limited to the D10 reviewers: ____ .

### 6.3 Stop conditions (stop the run and fail it; collect nothing further)

1. Any widened A/B ownership intervals intersect (§4.6), a hand-over gap is negative, or the observer sees `holders > 1`.
2. Authentication by any path other than IAM, a password-mode start, or TLS verification bypassed. P1-41 §4.6 lists the full set.
3. **Unexpected authentication failures:** any `sts_access_denied`, `sts_invalid_identity_token`, `web_identity_token_unreadable`, `sts_region_disabled`, `sts_credentials_unusable` or `token_builder_panic` outside a planned drill; or `postgres error` authentication SQLSTATEs on a cluster where the role was verified.
4. Any `error` text outside §2.3. Any key outside the §4.5 allow-list in the raw stream that carries an identifier. Any secret-scan hit.
5. **Unexplained timing gaps:**
   - a phase missing where §2.2 says it must be present (outside censoring);
   - `login_prepare_sts + login_prepare_sign > login_prepare`;
   - any `phase` field arriving as `[REDACTED]` or as a pseudonym (the pipeline is altering timing evidence);
   - any child phase greater than its parent;
   - a gap in the readiness or DB sampling longer than the recorded period without an explanation.
6. A path to production, a reachable live carrier, or the budget alert (P1-41 §4.6).

On stop:
- scale A and B to 0;
- record the time and the condition number;
- preserve the redacted artifacts collected so far;
- do not resume without a new approval.

### 6.4 Cleanup and post-run verification

1. **Revert the log level** to the approved value and record it.
2. **Scale A and B to 0.**
3. **Confirm with the observer** that `holders = 0` for the lock key, and that no fence-user session remains (count only).
4. **If the run's approval ends here, follow P1-41 §4.6 rollback:**
   - detach `rds-db:connect`;
   - disable the IRSA trust;
   - `REVOKE rds_iam` / `DROP ROLE` for the fence user;
   - destroy the stack;
   - confirm in the account inventory that nothing remains.
5. **Re-run the secret scans** on the final artifact set. Record the zero-hit results.
6. **Write the run record (§5.1).** Do **not** mark D10 resolved: §5.3 decisions are a separate owner act.

## 7. Unresolved decisions (unchanged by this record)

| Item | State |
|---|---|
| **D10** values (I, T, V) and their derivation | unresolved; needs §5.2 evidence; nothing proposed |
| **F-8** credential timing fields redacted by the platform logger | **fixed in code (P1-43, option (a))**; not committed (F-6), not in an image, staging pipeline unverified |
| **`prod-global`** region policy | unresolved (P1-40 §4) |
| **F-3** credential expiry: `ExpiryWindow=0`, no early refresh | an evidence-based D10 decision (§5.3) |
| **F-4** `HTTPS_PROXY`/`NO_PROXY` honoured by the STS client | owner decision: (a) refuse, (b) ignore, (c) accept (P1-41 §3.3) |
| **F-7** base manifest: 2 replicas with RollingUpdate, vs the agreed A/B single-replica `Recreate` staging design | A5 staging overlay; the base manifest is unchanged |
| **F-6 / D8** gateway `internal/*` untracked (no CI); `-race` never run locally (no cgo) | owner commit; D8 approval |
| A5–A9, SEP items, Organization verifier, Aurora engine version, B4, D9, D11 | as P1-41 §6 |

## 8. Verification of this record

- **Scope:** documentation only. The only repository change is this new file.
- **P1-43 update:** field names, the F-8 status, the E1/E2 status and one stop condition were updated. Nothing was measured.
- **The F-8 probe** ran in the session scratchpad with `GOWORK=off`, `GOPROXY=off`, against the repository's `packages/go/platform` through a local `replace`. It wrote nothing into the repository.
- **Source references** were read from the working tree on 2026-10-15. Line numbers refer to that state of the untracked gateway files.
