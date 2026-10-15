# P1-40 — A1: RDS IAM authentication design decisions and implementation

**Phase 19, P1-40.**

**Approval sources (conversation):**

- the owner's explicit confirmation of R1–R6, B1, B2, H1/H2, H3, M2, M3 and environment naming (the "Implementation-Readiness Review" instruction);
- the owner's **autonomous engineering authorization** for approved-scope code and offline tests (A1).

**Not approved, and not done:**

- A2 (dependency: `aws-sdk-go-v2`);
- A3 (network fetch);
- INFRA-AUTH;
- staging, deployment and carrier operations.

**Statuses:** D8 unapproved; D9 unresolved; D10 unresolved; D11 deferred. **Staging NO-GO.** **Exotel AgentStream production media NO-GO.**

---

## 1. Decisions recorded

| ID | Decision |
|---|---|
| R1 | IAM mode requires `sslmode=verify-full`, an explicit CA bundle and hostname verification; no plaintext fallback. *(Amends P1-37 O-5 for IAM mode only.)* |
| R2 | `CS_TELEPHONY_GATEWAY_FENCE_AUTH_MODE` is required (`iam` or `password`), with no default. Password mode only with `CS_TELEPHONY_GATEWAY_ENVIRONMENT` **explicitly present** and equal to `development`. |
| R3 | IAM mode rejects passwords from the DSN, the environment, passfiles and service files. Credentials and tokens are never logged or returned in errors. |
| R4a / H3 | IAM mode requires `CS_TELEPHONY_GATEWAY_FENCE_AWS_REGION`. If present, `AWS_REGION` and `AWS_DEFAULT_REGION` must match it. The explicit region is the one passed to the credential adapter. |
| R4b | Staging: `ap-south-1`. Production: `ap-south-1`, `ap-south-2`. |
| R5 | Credential retrieval and token generation run inside the existing per-dial `HeartbeatTimeout`; no new timing value. **D10 unresolved.** |
| R6 | Static misconfiguration stops startup. Runtime credential, STS, TLS and database-authentication failures fail closed (not owner, readiness false); no password fallback; no AWS call at startup. |
| B1 | IAM DSN: URL form only. Raw-text validation (single host, explicit port, one database, no userinfo password, query allow-list `sslmode`/`sslrootcert`/`target_session_attrs`/`application_name`, no duplicates), then parsed-pgx invariants that must equal the raw text. |
| B2 | IAM mode is refused in `development` until a separate region decision. |
| H1/H2 | IAM mode refuses all 24 `PG*` variables pgx v5.10.0 reads. **Re-review on any pgx upgrade.** |
| M2 | `sslrootcert=system` refused; an absolute, readable, regular CA file is required. |
| M3 | Unconfigured means none of the six fence variables is present. Present-but-empty is invalid. `FENCE_AWS_REGION` must be absent in password mode. |
| Environment naming | Code accepts only the platform values. Operator mapping: `dev` → `development`, `staging` → `staging`, `prod-in` → `production`. **`prod-global` unresolved:** it also runs as `production` and cannot be told apart in code (§4). |
| A1 condition | Until the AWS adapter exists (A2 + A3), a fully valid IAM configuration **stops startup** (`ErrIAMAdapterUnavailable`); it never falls back to password mode. |

---

## 2. Implementation (A1 scope)

All files are under `services/go/telephony-gateway/`; all were already untracked, or are new.

| File | Change |
|---|---|
| `internal/fence/fence.go` | **Additive:** the `Credentials` interface; `PgxCredentialDialer` (the password is set only on a per-dial `cfg.Copy()`, from the source called under the dial context); `ErrCredentialsUnavailable` (the source's error text is dropped). No AWS import. |
| `internal/app/iam.go` (new) | `checkIAMEnvironment` (B2, R4a/b, H1–H3, R6 static IRSA checks: `AWS_ROLE_ARN`, a readable `AWS_WEB_IDENTITY_TOKEN_FILE`); `validateIAMDSN` (B1, M2, R1, R3; never wraps `url.Parse` errors, which embed the URL); `validateIAMParsed` (verify-full TLS signature, no fallbacks, no password, target equality, `ValidateConnect`); `ErrIAMAdapterUnavailable`; the `iamCredentials` hook (nil in this build) |
| `internal/app/fence.go` | `FENCE_AUTH_MODE` and `FENCE_AWS_REGION` variables; M3 presence logic; the R2 explicit-environment rule; the IAM path (raw validation, parse, invariants); mode-based dialer selection in `WireFence` with no fallback; `auth_mode` in the startup log |
| `internal/fence/credentials_test.go`, `tls_test.go`, `credentials_postgres_test.go` (new) | per-dial credentials, no leakage, context bound, no-AWS-import guard; on-the-wire `verify-full` refusal with a positive control; real-PG credential rotation |
| `internal/app/iam_test.go` (new) | the configuration rules, each negative case asserting its own reason |
| `internal/app/fence_test.go`, `cmd/server/fence_startup_test.go` (mine, P1-30B) | configurations updated for M3/R2; port helpers fixed (a reproduced flake: two separately chosen free ports collided) |

**Not changed:**

- `cmd/server/main.go`, `main_test.go`, `buildinfo*.go` (user-owned);
- `packages/go/telephony/lifecycle.go`, `runtime.go`;
- `go.mod`/`go.sum` (no new dependency; their existing diff, pgx v5.10.0, predates A1), manifests, Dockerfile, CI;
- `infra/k8s/base/deployment.yaml` (still deleted).

---

## 3. Verification

**Environment:** `GOFLAGS=-mod=readonly GOTOOLCHAIN=local GOPROXY=off`, Windows. Real-PG runs use a throwaway `postgres@sha256:7e5df973…c96` container (random secret, loopback, `--pull never`, removed on exit).

| Check | Result |
|---|---|
| gofmt, vet, `GOWORK=off go mod tidy -diff`, `GOWORK=off go test` (gateway; also telephony, telephonystore, telephonyredis, platform) | clean / exit 0 |
| Gateway, offline `go test -count=1 -v ./...` | **45 pass, 0 fail, 14 skip** (the skips are real-PG tests without a DSN: skips, not passes) |
| Gateway with real PG, `-count=2` | **118 pass, 0 fail, 0 skip, 0 ownership overlaps** |
| Other modules, offline | telephony 228/0/0; telephonystore 21/0/39 (skips: real-PG tests); telephonyredis 15/0/9 (skips: real-Redis tests); platform 71/0/0 |
| Mutation check | removing the `sslrootcert=system` rule and the parsed-password invariant made the matching tests fail; the code was restored byte-for-byte |
| Flake found and fixed | `TestWireFence_InvalidConfigurationStopsStartup` failed once under `-count=2` (both ports picked the same free port). The helper was fixed; `-count=20` passes. |
| `-race` | **not run:** `go: -race requires cgo` (no GCC locally) |
| CI / lint / staging / real IAM | **not run:** D8 unapproved; golangci-lint not installed; no staging; no AWS |

---

## 4. Limitations and open items

- **IAM mode cannot run yet:** the AWS adapter needs A2 + A3. Real IAM authentication, STS behaviour, token lifetime and SDK credential-cache behaviour under cancellation are provable only in staging.
- **`prod-global`:** code sees only `ENVIRONMENT=production`, so the R4b production list would apply to it. That's acceptable only while `prod-global` has no deployment (its Terraform environment is empty). Enforcing a separate policy needs a new decision (for example, a deployment-identity variable).
- **D10:** unresolved. The evidence needed (staging S1): dial-phase timing split into credential, token build, TCP/TLS and authentication; STS latency and failure rate; credential-cache refresh frequency; acquisition failures from credential retrieval.
- **pgx coupling:** the 24-variable list and the TLS signature are pinned to pgx v5.10.0 and must be re-reviewed on upgrade.
- **"No CNAME"** is an operator rule; DNS cannot be checked offline.
- **The user-owned `cmd/server/buildinfo_test.go`** has the same two-free-ports pattern that flaked in my helpers. Left untouched; noted for you.

---

## 5. Next steps and required external authorizations

1. **A2:** approve adding `aws-sdk-go-v2` modules to the gateway `go.mod`. Route choice: `config` (simpler) or `credentials/stscreds` + `service/sts` (a smaller closure). Apache-2.0 licence.
2. **A3:** approve a network fetch of exactly those modules (Go module proxy and checksum database).
3. Then the adapter `internal/rdsauth` (`config.WithRegion(explicit)`, `auth.BuildAuthToken(host:port, region, user, creds)`), set as `iamCredentials`, with offline adapter tests.

**Credentials:** none are needed for A1–A3. Staging later needs:
- IRSA (an EKS service-account role; no static keys);
- a staging Aurora user with `rds_iam` (no password);
- the CA bundle as a mounted file.

AWS usage (EKS, Aurora, STS) only begins under INFRA-AUTH, which is unapproved. No secret should ever be pasted into chat; they are delivered via ESO-mounted files and IRSA (SECURITY.md).

---

## 6. Addendum — RDS IAM adapter core (A1 continuation)

**Scope:** code and offline tests only. **A2 and A3 are still NOT approved.** No AWS module is in the local module cache (`GOMODCACHE` has no `github.com/aws`) and none is in any repo `go.mod`. So the SDK binding was not written, and IAM authentication **does not work end-to-end**.

### 6.1 Dependency choice (for A2)

**Chosen route:** `github.com/aws/aws-sdk-go-v2` (core: `aws.NewCredentialsCache`), `service/sts`, `credentials/stscreds` (web-identity provider) and `feature/rds/auth` (`BuildAuthToken`). The general `config` package is **not** used.

**Why:**
- `config.LoadDefaultConfig` resolves credentials and endpoints from many sources the gateway has not validated: static-key variables, `AWS_PROFILE`, shared config and credentials files, `AWS_ENDPOINT_URL*`, container credentials and IMDS.
- The narrow route uses exactly the IRSA values that startup has already validated (explicit region, `AWS_ROLE_ARN`, `AWS_WEB_IDENTITY_TOKEN_FILE`), so the environment cannot substitute another identity or redirect STS.
- It is expected to have the smaller module closure.

**Not verifiable offline** (to be checked at A3, when the modules are fetched):
- exact versions;
- compatibility with Go 1.25;
- closure size;
- the Apache-2.0 licence files;
- `go.sum` hashes against `sum.golang.org`.

**Planned binding** (`internal/rdsauth/aws.go`, after A2 + A3; not present):

```go
stsClient := sts.New(sts.Options{Region: id.Region})
provider := aws.NewCredentialsCache(stscreds.NewWebIdentityRoleProvider(
    stsClient, id.RoleARN, stscreds.IdentityTokenFile(id.WebIdentityTokenFile)))
src, err := rdsauth.New(id, rdsauth.TokenBuilderFunc(func(ctx context.Context, endpoint, region, user string) (string, error) {
    return auth.BuildAuthToken(ctx, endpoint, region, user, provider)
}))
```

It is set as `app.iamCredentials`; `TestPackage_ImportsNoAWSSDK` is then narrowed to allow that one file.

### 6.2 Implemented

| File | Change |
|---|---|
| `internal/rdsauth/rdsauth.go` (new) | `Identity` (region, role ARN, token-file path; validated; no static-key field); `TokenBuilder`; `Source` (a `fence.Credentials`). It builds a **new token on every call** and caches nothing; the endpoint is `net.JoinHostPort(host, port)`; the call returns within the dial context even if the builder ignores it, and a late token is discarded. Builder errors and empty tokens become `ErrTokenUnavailable` with the cause withheld. No AWS import. |
| `internal/rdsauth/rdsauth_test.go` (new, 9 tests) | the inputs reaching the builder (endpoint including bracketed IPv6, region, user); a fresh token per call; missing inputs; provider, signing and empty-token failures withheld; the context bound with a builder that ignores ctx; a cancelled context; a token racing cancellation discarded; a failure through `fence.PgxCredentialDialer` → `ErrCredentialsUnavailable`, no connection; the no-AWS-import guard |
| `internal/app/iam.go` | `iamCredentials` now receives an `rdsauth.Identity`. **New static rule:** IAM mode refuses `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` and `AWS_SESSION_TOKEN` (it names the variable, never the value). This is an engineering decision implementing "IRSA only, no static keys"; it adds no network call. |
| `internal/app/fence.go` | `FenceSettings.IAM` carries the validated identity; a padded or malformed role or token-file value stops startup |
| `internal/app/iam_test.go` | static-key and padded-role cases (each with a leak check); a wired test using the real `rdsauth.Source` with a failing builder: identity passed through, ≥2 token builds across re-dials, never owner or ready, no secret, ARN, STS text or token-file path in readiness output or logs |

### 6.3 Verification (this increment)

| Check | Result |
|---|---|
| gofmt, vet, `GOWORK=off go mod tidy -diff`, `GOWORK=off go test` | clean / exit 0 |
| Gateway offline `go test -count=1 -v ./...` | **55 pass, 0 fail, 14 skip** (the skips are real-PG tests) |
| Gateway against throwaway PG (`postgres@sha256:7e5df973…c96`, `--pull never`), `-count=2` | **138 pass, 0 fail, 0 skip**; every overlap probe reported "no overlap"; container removed |
| Mutation checks | (1) context bound removed → `TestPassword_IsBoundedByTheContext` hangs (test timeout); (2) builder error returned → `TestPassword_BuilderFailuresAreWithheld` fails; (3) static-key rule disabled → 3 cases fail. All restored byte-for-byte. |
| `-race`, lint, CI, staging, AWS | **not run** (no cgo/GCC; no golangci-lint; D8; no staging; A2/A3) |

### 6.4 Still open

- **End-to-end IAM authentication:** blocked on A2 + A3, then INFRA-AUTH and staging.
- **Unverified SDK behaviour:**
  - whether `BuildAuthToken` returns the token without a scheme;
  - whether the credentials cache honours cancellation;
  - how `stscreds` behaves when STS is slow.

  The `Source` context bound covers the last two regardless.
- **D10, `prod-global`, the pgx coupling and the `buildinfo_test.go` port pattern** are unchanged from §4.

*(§6.4's SDK questions are answered in §7.2.)*

---

## 7. Addendum — A2/A3: AWS SDK dependency and the real token builder

**Authorization:** the owner's instruction "Unblock RDS IAM Authentication and Complete A2/A3" (conversation). It covers only the narrow route in §6.1 plus its required transitive modules, fetched through the configured Go proxy with checksum verification.

**Still not approved:** INFRA-AUTH, staging, D8, D9. **Other statuses:** D10 unresolved; D11 deferred. **Staging NO-GO. Exotel AgentStream production media NO-GO.**

### 7.1 Dependencies (gateway module only)

| Choice | Detail |
|---|---|
| Release set | The coordinated SDK release of **2026-09-24**: the newest release that was **at least 14 days old** at selection time (2026-10-15). This was a supply-chain caution; the 2026-10-09 release was skipped. |
| Source and verification | `proxy.golang.org`, `GOSUMDB=sum.golang.org` (checksum database consulted during `go get`); `go mod verify` → "all modules verified" |
| Go version | Every module declares `go 1.24`. The gateway stays at `go 1.25.0`; the local toolchain is go1.26.5. |
| Licence | `LICENSE.txt` / `LICENSE` read in each compiled module: Apache License, Version 2.0 |
| Diff check | `go.mod`: only the 4 direct and 6 indirect `github.com/aws` requirements are new (the `go 1.25.0` and pgx lines predate A1). `go.sum`: only `github.com/aws` lines added (20). No other module, `go.work` or `go.work.sum` changed. |

| Module | Version | `h1:` hash (go.sum) |
|---|---|---|
| `github.com/aws/aws-sdk-go-v2` (direct) | v1.47.1 | `h1:uOIZnp4PK3ZhKI0dNrJrhTEsLxbpXHTAJlwoS1pvAtw=` |
| `github.com/aws/aws-sdk-go-v2/credentials` (direct) | v1.20.6 | `h1:NpAFXCU7NzXNkdGK3zQTtsRJ+3v9tZQV0xcdRw8uBdw=` |
| `github.com/aws/aws-sdk-go-v2/feature/rds/auth` (direct) | v1.7.4 | `h1:DsW6xUKRhy6HhbadXNPIRB2/8CAFk0mSH63RVhR12l0=` |
| `github.com/aws/aws-sdk-go-v2/service/sts` (direct) | v1.51.1 | `h1:0HOqZXRvMytH6bFHVIc0oJX07sZjfhz0zXtjs6gdE8s=` |
| `github.com/aws/aws-sdk-go-v2/internal/configsources` | v1.5.4 | `h1:CLq4+8UHCI+ZZYl/EuJxXovaIVN2xeeT8JV+dsApQ5E=` |
| `github.com/aws/aws-sdk-go-v2/internal/endpoints/v2` | v2.8.4 | `h1:dD4MR81I7YkpEBRk6UP9rocC2QnT3qVuXwzlYTtfGEs=` |
| `github.com/aws/aws-sdk-go-v2/internal/v4a` | v1.5.4 | `h1:7Wo47d/xn/7KttCSBd8EGYeZ7ULRFRkUHr6vkZPBzVQ=` |
| `github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding` | v1.13.19 | `h1:bAdDl/HkGCcGPoe25ToSHEw23VIxt6CT5fLcg111BKg=` |
| `github.com/aws/aws-sdk-go-v2/service/internal/presigned-url` | v1.14.4 | `h1:29SvnfGhXjTl8ONxFwbj2rs6lbhiFXD2CgFQmbT/bXY=` |
| `github.com/aws/smithy-go` | v1.28.1 | `h1:R/nXH00c8qcfCzQVELtRw+eLQWtzv+VAIEFJ1/xxXlQ=` |

`imds`, `sso`, `ssooidc` and `signin` appear in the module graph (via `credentials`' requirements) but are **not compiled** into the gateway; `go mod tidy` keeps them out of `go.mod`.

### 7.2 SDK behaviour, verified from the v1.47.1 / v1.51.1 / v1.20.6 / v1.7.4 source

| Behaviour | Finding | Consequence |
|---|---|---|
| `sts.New(Options)` vs `NewFromConfig` | `AWS_ENDPOINT_URL` / `AWS_ENDPOINT_URL_STS` are read only on the `NewFromConfig` path | `sts.New` is used; a test proves environment overrides are ignored |
| `AssumeRoleWithWebIdentity` auth | `smithyauth.SchemeIDAnonymous` | the STS client carries no credentials; a test proves the request is unsigned |
| `stscreds.WebIdentityRoleProvider` | re-reads the token file on every refresh; its errors embed the file path and STS text; it dereferences `resp.Credentials` **without nil checks** | a mutation test proved a malformed STS response **panics** |
| `aws.CredentialsCache.Retrieve` | returns when the caller's context ends, but runs the refresh on a `suppressedContext` (**no cancellation, no deadline**) via singleflight, which **re-raises panics in another goroutine** | without a guard, a hung STS call blocks every later dial and a panic crashes the process |
| `auth.BuildAuthToken` | calls `creds.Retrieve(ctx)`; `X-Amz-Expires=900`; returns `host:port?Action=connect&DBUser=…&X-Amz-…` (**no `/`**, scheme stripped) | this answers §6.4 |
| Other environment reads in compiled code | `AWS_LAMBDA_FUNCTION_NAME`/`_X_AMZN_TRACE_ID` (trace header), `AWS_EXECUTION_ENV` (user agent), `AWS_NEW_RETRIES_2026` (retry mode), a read-timeout opt-in, and `HTTPS_PROXY`/`NO_PROXY` (`http.ProxyFromEnvironment`) | none changes identity, endpoint or TLS verification; a proxy would see STS traffic metadata only (TLS end-to-end). Operator note. |

### 7.3 Implementation

`internal/rdsauth/aws.go` (new; the only file importing the SDK):

- `NewAWS(id)`: builds `sts.New(sts.Options{Region: id.Region})` with no credentials, then `aws.NewCredentialsCache(guardedProvider{stscreds.NewWebIdentityRoleProvider(…, IdentityTokenFile(path), RoleSessionName = "callscreen-telephony-gateway-fence")})`, then `auth.BuildAuthToken` inside the SDK-free `Source`. **No network call at construction.**
- `guardedProvider` (inside the cache):
  1. re-binds the STS exchange to the dial that triggered it (`context.AfterFunc`). The bound is the existing per-dial `HeartbeatTimeout`; **no new timing value (D10)**.
  2. recovers SDK panics into an error.
  3. refuses credentials that are incomplete or already expired, so they are neither cached nor used.
- STS credentials are cached until their expiry; **the RDS token is signed anew for every connection attempt**. `ExpiryWindow` is left at 0 (no early refresh): choosing a value is a timing decision.

Other changes:

- **`internal/rdsauth/rdsauth.go`:** a panicking builder becomes `ErrTokenUnavailable`.
- **`internal/app/iam.go`:** `iamCredentials` now defaults to `rdsauth.NewAWS`. `nil` still means `ErrIAMAdapterUnavailable`.
- **Tests:**
  - `aws_test.go` (new, 7 tests, 7 subtests):
    - STS request contents (role, session name, token-file content, unsigned);
    - token shape (host:port, `DBUser`, credential scope `…/ap-south-1/rds-db/aws4_request`, session token, 900 s);
    - one STS call but two distinct tokens across two connections;
    - seven failure modes (denied, 500, malformed XML, credentials missing → panic recovered, expired, empty key, aborted connection), all withheld and none cached;
    - an unreadable token file, with no STS call;
    - a hung STS call abandoned at the dial deadline, without blocking the next dial;
    - the production regional endpoint `https://sts.ap-south-1.amazonaws.com`, while static keys, `AWS_PROFILE`, `AWS_REGION` and `AWS_ENDPOINT_URL*` are set and ignored;
    - **on the wire:** over `verify-full` TLS, a fake PostgreSQL server receives the IAM token as the cleartext password, one fresh token per connection; a server with an untrusted certificate receives **nothing** (one connection, no password).
  - `rdsauth_test.go`: the import guard now enforces "only `aws.go` may import the SDK"; plus a panicking-builder test.
  - `iam_test.go`: the default adapter is built at startup with no AWS call and no ownership; cleanups restore the default.

### 7.4 Verification

| Check | Result |
|---|---|
| gofmt / vet / `GOWORK=off go mod tidy -diff` / `go mod verify` / `GOWORK=off go test` | clean / clean / clean / all modules verified / exit 0 |
| Gateway offline `go test -count=1 -v ./...` | **64 pass, 0 fail, 14 skip** (top-level; the skips are real-PG tests) + **7 subtests pass** |
| Gateway with throwaway PG, `-count=2` | **156 pass, 0 fail, 0 skip** (top-level) + **14 subtests pass**; every overlap probe reported "no overlap"; container removed |
| Mutation checks (`aws.go`) | (1) dial re-binding removed → the hung-STS test fails (request not abandoned); (2) panic recovery removed → **the test binary crashes with a nil pointer dereference inside the SDK**; (3) expiry check removed → the expired-credentials case returns a token. All restored byte-for-byte. |
| `-race`, lint, CI | **not run** (no cgo/GCC; no golangci-lint; D8) |
| Real AWS STS, real RDS IAM login, staging | **not run.** No AWS call was made. |

### 7.5 Status and open items

- **The IAM path is complete in code and verified against local fakes.** It is **not proven against AWS:** the real STS trust policy, the `rds-db:connect` permission, an Aurora user with `rds_iam`, and real token acceptance need staging (INFRA-AUTH).
- **D10:** the STS exchange and signing now run inside the existing `HeartbeatTimeout`. Staging must measure STS latency (cold start and refresh) against it before D10 is set.
- **`prod-global`:** unchanged; unresolved (§4).
- **Credential expiry edge:** with `ExpiryWindow=0`, a dial right at expiry triggers a refresh in that dial; there is no early refresh. Any change is a timing decision.
- **Proxy environment:** `HTTPS_PROXY` would route STS through a proxy; operators should not set it for the gateway unless intended.
- **Upgrades:** re-run the §7.2 source checks on any SDK upgrade (the panic, suppressed-context and endpoint findings are version-specific).

*(Follow-up: the INFRA-AUTH readiness audit, with failure-reason classification and writer-endpoint enforcement, is in `P1-41_INFRA_AUTH_READINESS_AUDIT.md`.)*
