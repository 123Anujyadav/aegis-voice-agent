# Independent security review package: OTP ownership and delivery providers (OD-12)

- **Prepared:** 2026-11-09, `main` at `74a3735`, by Claude Code at the owner's direction.
- **What this is:** the material an **independent human reviewer** needs to review identity's one-time-code architecture and its providers.
- **What this is not:**
  - **It is not a review, and it approves nothing.** Everything below was prepared by the drafter of the code, so it is AI-performed, and it is not independent (`ARCHITECTURE_FREEZE.md` §6.1, rule 9).
  - **CI results are evidence for the reviewer, not approval.**
  - **OD-12 is open.** No reviewer has been identified.
- **Decision under review:** [ADR-0033](../adr/0033-otp-ownership-and-delivery-only-sms-providers.md) (Proposed), the owner's preferred **Option A**: identity generates, stores and verifies codes; SMS providers only deliver them.

## 1. The reviewer

**Required: independence.**
- Not the drafter: not Claude Code, and not anyone who wrote or directed the code under review.
- No commercial interest in any SMS provider being considered (Twilio, MSG91 or others).
- The owner's decision and the review are separate roles: the owner cannot be their own independent reviewer.

**Required qualifications:**
- Hands-on experience designing or assessing **authentication and account-recovery systems** that use one-time codes. Familiarity with OWASP ASVS v4 chapter 2 (authentication) and NIST SP 800-63B (the out-of-band authenticator rules), or equivalent.
- The ability to **read and test Go**: the code is Go, and the tests run with `go test`.
- An understanding of **SMS as an authenticator and its abuse:**
  - interception (SIM swap, SS7);
  - SMS pumping (toll fraud);
  - enumeration;
  - denial of service by lockout.

**Helpful:**
- India's commercial SMS rules (TRAI DLT: entity, header, template, PE–TM chain);
- DPDP Act processor obligations;
- PostgreSQL concurrency.

**Output expected:** a written report with findings rated by severity, and an explicit statement on each scope item in §2. It should be linked from ADR-0033's approval record and from OD-12.

## 2. Scope

| # | Item | Where | State |
|---|---|---|---|
| S1 | **The architecture: Option A** (ADR-0033) | `docs/adr/0033-…` | Proposed. Owner-preferred, not approved |
| S2 | **The existing OTP lifecycle** (generation, hash, expiry, attempts, single use, rate limits, lockout) | `services/go/identity/internal/app/identity.go`: `RequestOtp`, `checkCode`, `wrongCode`, `validateOtp`, `secureOtpCode`, `hashString`, `defaultOtpRatePolicy`; repositories `MemoryIdentityRepository` and `internal/pgstore` | On `main` since earlier phases. Its ADR-0029 is Proposed |
| S3 | **The delivery boundary and failover** | `internal/otpdelivery/otpdelivery.go` (`Provider`, `Sender`) | On `main` |
| S4 | **The development mock and its selection rules: PR #270, merged** | `internal/otpdelivery/mock.go`; `internal/bootstrap/otp.go`, `runtime.go` (`Open`); `cmd/server/identity.go` | **Merged as `74a3735` before the owner's 2026-11-09 statement that authentication changes are high-risk.** A post-merge review. See §6 |
| S5 | **A future delivery-only provider adapter** (Twilio Programmable Messaging and/or MSG91) | Not written yet | Review **before merge** and before activation |
| S6 | *For reference only:* **PR #271, Twilio Verify** | Draft PR #271 | **Not selected** (contradicts Option A). Not to be merged. No review needed unless the owner reconsiders |

**Out of scope:** access tokens and DPoP (ADR-0029), Play Integrity (T4, unresolved), account deletion (except where it shares `checkCode`), and the Android app.

## 3. How to run the evidence yourself

**Unit tests:**

```sh
cd services/go/identity
go test ./internal/app/ ./internal/otpdelivery/ ./internal/bootstrap/ ./internal/httpapi/ ./cmd/server/
```

**With real PostgreSQL** (Docker, a throwaway container, from the repository root):

```sh
POSTGRES_IMAGE=docker.io/library/postgres@sha256:7e5df973a74872482e320dcbdeb055e178d6f42de0558b083892c50cda833c96 \
GO_TEST_FLAGS=-race COVERAGE_REPORT=1 tools/scripts/identity-postgres-tests.sh
```

No test sends an SMS or contacts any provider.

## 4. Threat analysis (AI-performed; for the reviewer to confirm or correct)

| # | Threat | Control today | Evidence (tests) | Residual risk |
|---|---|---|---|---|
| T1 | **Guessing a code** | 10^6 space; 3 wrong per challenge; 5 failures per number per 15 min, then a 15-min lockout; the failure reservation is taken **before** the comparison, atomically | `attemptlimit_test.go`, `otp_ratelimit_test.go`, `lockout_policy_test.go`, `otp_race_test.go`; `otpdelivery/mock_test.go` (exhaustion) | Two live challenges per number at once (ADR-0033 §6): bounded by the per-number failure limit |
| T2 | **Replaying a used code** | `ConsumeOtp` is atomic; only the consumer proceeds | `consumption_test.go`; `mock_test.go` (reuse) | — |
| T3 | **An expired code** | Expiry is checked before the comparison | `expiry_test.go`; `mock_test.go` | — |
| T4 | **Codes or numbers leaking through the API, logs or errors** | The code is never returned or logged; provider text is never returned; the number is stored only as a keyed pseudonym | `otp_delivery_test.go`; `httpapi_test.go` leak checks; `mock_test.go` (response, headers, logs); `msisdn_pseudonym_test.go` | The provider sees numbers and codes in transit (DPDP processor) |
| T5 | **Codes readable from the database** | Only HMAC-SHA-256 hashes, under a key not stored in the database | `otp_delivery_test.go` (stored challenge holds no code) | One HMAC key for several purposes (ADR-0030, Proposed). With the key **and** the database, a 6-digit hash is brute-forceable |
| T6 | **The mock enabled in a deployed tier** (anyone reading its outbox could sign in) | Accepted only with `CS_IDENTITY_ENVIRONMENT=development` set **explicitly**; refused when the variable is unset, staging or production; never the default there | `bootstrap/otp_test.go` | The environment variable is operator-set. A pod spec that sets `development` in production would enable it. A review of deployment manifests is needed when they exist |
| T7 | **The mock outbox file** | `0600`, development only, optional | `mock_test.go` (format and mode on Unix) | It holds raw numbers and live codes on a developer's machine |
| T8 | **Denial of service by locking out a victim** | The lockout is fixed (15 min), not escalating, by design; refused guesses during a lockout do not extend it | `lockout_policy_test.go` | Anyone can lock a known number for 15 minutes repeatedly |
| T9 | **SMS pumping / toll fraud** | Per-number request limit only | `otp_ratelimit_test.go` | **No country allowlist, no per-source limit and no global sign-in budget** (ADR-0033 §7, Q2, Q3) |
| T10 | **Delivery failure leaving a live, undelivered code** | The challenge is withdrawn, detached from the request's context; the request still counts | `otp_delivery_test.go`; `mock_test.go` (delivery failure) | Withdrawal is best effort; the code expires in 5 min anyway |
| T11 | **A slow or hostile provider** | Each attempt bounded (0.5–4 s), failover, stop on request end, no provider text returned | `otpdelivery_test.go` | Adapter-specific behaviour is reviewed per adapter (S5) |
| T12 | **Provider credential exposure** | Secret files only; never an environment value, log or error; never sent to Android | Runbook §1; `bootstrap` secret-file tests | No adapter exists yet (S5) |
| T13 | **Enumeration (is this number registered?)** | `POST /v1/otp/request` answers the same for any canonical number | `httpapi_test.go` | Timing differences were not measured |
| T14 | **SMS interception** (SIM swap, SS7, malware) | None in identity; ADR-0010 accepts this risk; re-enrolment is audited | `reenrol_audit_test.go` | Accepted by ADR-0010; SIM-swap controls are in `SIM-SWAP-CONTROLS-REVIEW.md` |

## 5. Test evidence (CI; evidence, not approval)

| Head | What | Runs |
|---|---|---|
| `c2585d6` (PR #270) | All 162 required and optional checks passed, including `ci-gate`, `identity-postgres` (real PostgreSQL, `-race`) and `pr-security` (gitleaks) | `ci-gate` 42647456293; `identity-postgres` 42647456189; `pr-security` 42647456307 |
| `74a3735` (`main`, after merge) | Post-merge runs passed | `identity-postgres` 42650499973; `pr-security` 42650500020 |
| `9d54a05` (PR #271, not selected) | All 162 checks passed | `ci-gate` 42650617571; `identity-postgres` 42650617546 |

**Local runs** (Windows, without `-race`) of `identity-postgres-tests.sh`:
- #270: pass 1017, fail 0, skip 1 (the allowed helper process);
- #271: pass 1077, fail 0, skip 1.

**Coverage**, from `identity-postgres`'s coverage report, on the pre-#270 baseline (`babfdb6`):
- `app` 94.7%;
- `otpdelivery` 100%;
- `bootstrap` 90.9%;
- `httpapi` 95.2%.

## 6. A disclosure about PR #270

- **Judgement at merge:** #270 adds a development-only delivery provider and its selection rules. It does not change code generation, hashing, verification or any limit, and its tests show that. I judged it routine and merged it under the owner's standing authorization.
- **The owner's later statement:** on 2026-11-09 the owner stated that authentication changes are high-risk under §6.1, and that none is merged before the independent review.
- **The remedy:** #270 touches identity's startup configuration, so it is included here (S4) for a **post-merge review**. The owner directed that #270 stay unchanged. It is not reverted.
- **If the reviewer finds a defect,** the fix is a new change, reviewed the same way.

## 7. Unresolved risks and questions for the reviewer

- Everything in ADR-0033 §10 (Q1–Q10, R1–R4).
- **Specific questions:**
  1. Is the fixed, non-escalating lockout (T8) the right trade-off against guessing (T1)?
  2. Should a new request withdraw the number's earlier live challenges (T1)?
  3. Is the explicit-development gate for the mock (T6) sufficient, or should the mock also be excluded from production builds (a build tag)?
  4. Which abuse controls (T9) are required before any real SMS is sent?
  5. Is the HMAC-per-purpose separation (ADR-0030) required before production?

## 8. After the review

1. The reviewer's report is linked from ADR-0033's approval record and from OD-12.
2. The owner decides ADR-0033: Accepted, amended or rejected.
3. Only then:
   - a delivery-only adapter is merged, after its own review (S5);
   - the owner's external approvals proceed (R1, DLT, billing).

## 9. Handoff to the reviewer (prepared 2026-11-09; OD-12 stays open)

A send-ready brief for approaching candidates (the message, channels, selection criteria, a conflict-of-interest declaration, and the owner's decisions before sending) is in [`OD-12-REVIEWER-ENGAGEMENT-BRIEF.md`](OD-12-REVIEWER-ENGAGEMENT-BRIEF.md) (2026-11-10). No candidate has been contacted.

**Engagement (the owner's steps):**
1. The owner chooses the reviewer against §1. The reviewer declares, in writing, no conflict of interest: not the drafter, and no commercial tie to an SMS provider under consideration.
2. The owner grants **read-only** access to the repository, or supplies an archive of `main` at the commit under review. No credentials, staging access or provider accounts are needed: everything runs locally.
3. The owner agrees the scope (§2) with the reviewer. If the owner chooses sign-out option A or B (`OWNER-DECISION-PACKET-2026-11-09.md` §4), that change joins the scope as **S7**, reviewed before it is merged.

**Materials:**
- **Decision and code:**
  - ADR-0033;
  - this package;
  - `OTP-PROVIDERS.md`;
  - the code paths in §2;
  - draft PR #271, for reference only.
- **Evidence:** the CI runs in §5, and the commands in §3.
- **Environment:** Go 1.26.10, and Docker for the throwaway PostgreSQL. Linux or macOS is needed for `-race`.

**The deliverable (a written report):**
- For each scope item S1–S6 (and S7 if added): reviewed or not reviewed, and a statement of the conclusion.
- Findings, each with:
  - a severity (critical, high, medium, low or informational);
  - the file and line;
  - the evidence;
  - a recommended fix.
- Answers to §7's questions.
- A recommendation on ADR-0033: accept, amend or reject. **The decision stays the owner's.**

**Recording:**
- The owner links the report from ADR-0033's approval record and from OD-12, and records their decision.
- Findings become tracked items. Any fix is a new change, reviewed the same way.
- **Nothing in this repository marks OD-12 done:** only the owner, on receiving the report.
