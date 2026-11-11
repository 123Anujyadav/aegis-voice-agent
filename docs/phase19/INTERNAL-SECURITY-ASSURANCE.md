# Internal security assurance: authentication (2026-11-11)

> **AI-performed internal assurance, by Claude Code. NOT an independent security review.** It does not satisfy `ARCHITECTURE_FREEZE.md` §6.1 rule 8 (rule 9), and it is never to be described as an audit or a third-party review. Programme: [`OD-12-REVISED-DIRECTION-2026-11-11.md`](OD-12-REVISED-DIRECTION-2026-11-11.md) §4.

- **As of:** `main` `290cdd9`.
- **Method:**
  1. Threats were drawn from the OD-12 package's threat analysis (§4), extended to sessions, tokens, DPoP and the API.
  2. Each threat was mapped to the existing tests that cover it, from a read of `services/go/identity`, `services/go/edge-api`, `packages/go/accesstoken`, `packages/go/dpop` and `packages/go/middleware`.
  3. Test-only coverage was added where a threat was partly or not covered.
  4. Behaviour that is a gap, not a defect, is recorded below.
- **Scope:** sign-in and OTP, sessions and refresh, access tokens and DPoP, edge-api authentication, account deletion, redaction. Out of scope: Play Integrity (T4, unresolved) and the Android client.

## 1. Coverage by threat

| ID | Threat | Status | Evidence |
|---|---|---|---|
| T-a | Codes come from a cryptographic source, with the right format and no bias | **Covered (added)** | `TestSecurityAssurance_OtpCodeIsSixUniformASCIIDigits`: 100,000 codes, all six ASCII digits, per-position chi-square within a loose bound. `TestSecurityAssurance_OtpEntropySourceIsCryptoRand`: a source scan shows `crypto/rand`, no `math/rand`. Existing: `TestOtp_IsNotAConstant`, `TestOtp_HasTheExpectedFormat` |
| T-b | Codes are stored only as an HMAC | Covered | `TestOtpDelivery_SenderReceivesTheNumberAndTheCode`; `pgstore` contract "OtpRoundTripAndCopies" |
| T-c | Expiry is enforced | **Covered (added)** | `TestSecurityAssurance_ChallengeExpiresAtNowPlusOtpLifetime` (stored expiry within ±2 s of now + 5 min). Existing: `TestExpiry_*` |
| T-d | A code is single use | Covered | `TestOtp_IsSingleUse`; `TestConsumption_*` |
| T-e | Attempt limits per challenge, and lockout | Covered | `TestOtp_AttemptLimitIsEnforcedAndPersisted`; `TestOtpRateLimit_FiveFailuresLockTheNumber`; `TestLockoutPolicy_*`; `TestHTTP_EnrolmentCodeFailuresAndLockout` |
| T-f | A new request cannot reset a lockout | Covered | `TestLockoutPolicy_GuessingDuringALockoutCannotExtendIt` |
| T-g | Per-number request rate limit | Covered | `TestOtpRateLimit_RequestsPerWindow`; `..._ConcurrentRequestsCannotExceedTheLimit`; Postgres cross-instance tests |
| T-h | Sign-in does not reveal whether a number has an account | **Covered (added)** | `TestSecurityAssurance_SignInDoesNotRevealAccountExistence`: same status, headers, body shape and length for an enrolled number and a new one, and the same 401 for a wrong code. Deletion was already covered |
| T-i | Constant-time code comparison | **Covered (added)** | `TestSecurityAssurance_CodeHashIsComparedInConstantTime` and `..._EqualHashIsConstantTime`: AST scans show `hmac.Equal`, and no `==` or `!=` on `CodeHash` |
| T-j | Concurrent validation races | Covered | `TestValidateOtp_ConcurrentCorrectCodesConsumeOnce`; `..._ConcurrentWrongGuessesAreAllCounted`; Postgres "ConsumeOtpHasExactlyOneWinner" |
| T-k | Several live challenges for one number | **Covered (added), as current behaviour** | `TestSecurityAssurance_SecondRequestDoesNotInvalidateTheFirst`; `..._ChallengesShareTheNumbersFailureBudget`. See gap G2 |
| T-l | Input validation of the number and the code | **Covered (added)** | `TestSecurityAssurance_ValidateRefusesNonASCIISixDigitCodes` and `..._DeletionConfirmRefusesNonASCIISixDigitCodes`: Arabic-Indic, fullwidth, seven digits, whitespace and empty codes get 400, no attempt is counted, and the proof is not spent. The number was already covered (`TestMSISDN_SyntaxRule`) |
| T-m | The mock provider is never selected outside explicit development | Covered | `TestResolveOtpDelivery_MockIsRefusedOutsideExplicitDevelopment` |
| T-n | Delivery failures leak nothing | Covered | `TestSender_AllFailWithoutLeakingProviderText`; `TestOtpDelivery_FailureWithdrawsTheChallengeAndHidesTheCause` |
| T-o | Deletion codes share the sign-in protections | Covered | `TestWebDeletion_SharesTheNumbersLimitsWithSignIn`; `..._CodesAreBoundToTheirPurpose` |
| T-p | Refresh rotation | Covered | `TestIdentityService_TokenRotationAndReuse`; `TestRefreshToken_ConcurrentRefreshesRotateExactlyOnce` |
| T-q | Refresh reuse detection | Covered | `TestRefreshReuse_ARealSupersededTokenRevokes`; `TestRefreshToken_ReuseStillRevokes`. See gap G4 |
| T-r | Refresh expiry | Covered | `TestRefreshToken_ExpiredSessionIsRefusedNotRevoked`; `TestIssuance_ExpiredSessionCannotRefresh` |
| T-s | Access tokens: algorithm pinning, time, issuer and audience | Covered | `TestToken_OnlyEdDSAIsAccepted` (none, HS256 confusion, ES256, jku/x5u/jwk/crit); `TestToken_TimeIssuerAndAudience` |
| T-t | DPoP: method and URL binding, freshness, replay, key binding | Covered | `TestProof_*`; `TestPostgres_ConcurrentReplayAcceptsAtMostOnce`; `TestAuth_ProofFailures` |
| T-u | Every edge-api route is authenticated | Covered | `TestMe_AuthenticationFailuresNeverReachTheHandler`; `TestRoutes_ExactlyMe` |
| T-v | Sessions end on account deletion | Covered | `TestDeleteAccount_ErasesTheSubscriberEverywhereAndNobodyElse`. See gap G3 |
| T-w | Rate limiting on refresh | **Not implemented: recorded** | `TestSecurityAssurance_DocumentsNoRefreshRateLimit` pins today's behaviour. See gap G1 |
| T-x | No number, code or token in logs, errors or metrics | **Covered (extended)** | `TestSecurityAssurance_DeletionEndpointsLeakNoNumberOrCode` covers the deletion endpoints' 202, 401, 400, 429 and 503 paths and a failing provider. The sweep already covered the rest (`TestHTTP_NothingSensitiveLeaksIntoErrorsOrLogs` and others) |

**Result:** 23 of 24 threats are covered by tests. The 24th (T-w) has no control to test, and is recorded as gap G1. **No defect was found:** no added test needed to be skipped.

## 2. Gaps recorded (behaviour, not defects; each is an owner decision)

| # | Gap | Where it is decided | Pinned by |
|---|---|---|---|
| G1 | **No rate limit on token refresh.** Only rotation's compare-and-swap bounds it. `middleware.RateLimitingMiddleware` exists but is not mounted | C13 per-source limiting (E6); O3 and O4 neighbours | `TestSecurityAssurance_DocumentsNoRefreshRateLimit` |
| G2 | **A new code request does not invalidate earlier live challenges.** Up to 3 per window are live, sharing the number's 5-failure budget | ADR-0033 Q4 | `TestSecurityAssurance_SecondRequestDoesNotInvalidateTheFirst` |
| G3 | **An access token issued before deletion stays valid for up to 900 s** | O17, H2 (an ADR-0010 amendment) | `TestE2E_IdentityTokenAdmittedByEdgeThroughTheRealLifecycle` |
| G4 | **Refresh reuse revokes that session only,** not the subscriber's other sessions | O5 (SIM-swap policies) | `TestRefreshReuse_*` |
| G5 | **No country allowlist and no per-source or global sign-in budget** (threat T9, SMS pumping) | ADR-0033 Q2, Q3 | Not testable until decided. The draft delivery adapters (#301) refuse non-Indian numbers at the delivery boundary only |

## 3. How to reproduce

```sh
cd services/go/identity && go test -count=1 -run SecurityAssurance -v ./internal/app/ ./internal/httpapi/
```

- CI runs these on every change, including `-race` and the repeated shuffled race runs in `hardening`.
- The real-PostgreSQL suites run in `identity-postgres`.
