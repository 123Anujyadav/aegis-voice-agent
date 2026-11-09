# ADR-0033: One-time codes are owned by identity; SMS providers only deliver them (Option A)

- **Status:** **Proposed: the owner's PREFERRED architecture (2026-11-09); high-risk; awaiting independent human review. Not accepted, not ratified.**
  - **Owner preference, not formal approval.** On 2026-11-09 the repository owner selected **Option A** as their preferred architecture. Under `ARCHITECTURE_FREEZE.md` §6.1, an authentication decision becomes Accepted only with the owner's decision **and** an independent human review (rules 6 and 8). No independent reviewer exists yet (OD-12, gate G0a), so this ADR stays **Proposed**. Rule 7 also applies: drafting is not a decision.
  - **High-risk** under `docs/adr/README.md`, "Approval": it concerns authentication, identity, and a vendor processing personal data (phone numbers). That is the author's proposed classification; the final classification is the owner's (rule 6).
  - **What Option A changes in the code: nothing.** It describes the architecture `main` already has (§2). It records it as the intended architecture for every SMS provider, and rules out code-generating providers such as Twilio Verify (PR #271, not selected).
  - **ADR-0010 is Accepted and is not modified.** This ADR elaborates its §5(a) (SMS OTP at enrolment) and §9 (failover) for providers. No invariant is changed or superseded.
  - **ADR-0032 (Proposed)** would make email the primary sign-in, with SMS deferred, not cancelled. It is not decided here. Option A applies to SMS codes whenever they are used, and would apply equally to an email code sender.
- **Date:** 2026-11-09.
- **Deciders (none recorded yet):** the repository owner (§6.1, rule 6), after the independent human review (rule 8).
- **Independent human review:** required; **not yet available.** Review package: [`../phase19/OD-12-OTP-REVIEW-PACKAGE.md`](../phase19/OD-12-OTP-REVIEW-PACKAGE.md).
- **Drafted by:** Claude Code, at the owner's direction. **The analysis is AI-performed**; it is not an independent review.
- **Related:**
  - ADR-0010 §5(a), §9;
  - ADR-0029 (access tokens);
  - ADR-0030 (HMAC key separation, Proposed);
  - ADR-0012 (privacy);
  - [`../phase19/OTP-PROVIDERS.md`](../phase19/OTP-PROVIDERS.md);
  - [`../runbooks/identity-provider-onboarding.md`](../runbooks/identity-provider-onboarding.md) §1;
  - [`../phase19/M1-APPROVAL-REQUESTS.md`](../phase19/M1-APPROVAL-REQUESTS.md) R1.

## 1. Decision (proposed)

**The identity service generates, stores and verifies every one-time code. An SMS provider only delivers a message that identity has composed.**

1. identity **generates** each code: 6 digits from `crypto/rand`, uniformly distributed.
2. identity **stores only a keyed hash** of the code (HMAC-SHA-256), never the code, and never the phone number (a keyed pseudonym is stored instead).
3. identity **enforces** expiry, the per-challenge attempt limit, single use, the per-number request and failure limits, and the lockout.
4. A **provider-independent delivery interface** (`otpdelivery.Provider`) sends the code. Its only operation is "deliver this code to this number".
5. Twilio Programmable Messaging, MSG91, or any other approved provider **implements only that interface**.
6. **Changing provider never changes code generation or verification.** No provider is asked whether a code is right.

**Consequence:** a provider product that generates or verifies codes itself (Twilio Verify, MSG91's OTP API, and similar) is **not used**. A provider's plain messaging API is used instead.

## 2. What exists on `main` today (`74a3735`)

Option A is the existing design. Nothing below is new.

| Responsibility | Code |
|---|---|
| Generate | `secureOtpCode` (`services/go/identity/internal/app/identity.go`): `crypto/rand.Int` over 10^6; it panics if the entropy source fails |
| Store a hash | `hashString`: HMAC-SHA-256 under the identity HMAC key (`CS_IDENTITY_HMAC_SECRET_KEY_FILE`); `OtpChallenge.CodeHash` |
| Verify | `checkCode`, in this order: the attempt guard, expiry, the per-number failure reservation, then a constant-time comparison (`hmac.Equal`) |
| Expiry | `OtpLifetime` = 5 minutes, checked before the comparison, so an expired code is refused even when correct |
| Attempts | `maxOtpAttempts` = 3 wrong codes per challenge, read from the stored counter |
| Single use | `ConsumeOtp`: an atomic consume after a correct code; a concurrent second use is refused |
| Rate limits and lockout | `defaultOtpRatePolicy`: 3 requests and 5 failures per number in any 15 minutes, then a **fixed** 15-minute lockout (not escalating, on purpose) |
| Delivery interface | `otpdelivery.Provider { Name(); SendOtp(ctx, msisdn, code) error }`, and `DeletionCodeProvider` for the deletion template |
| Failover | `otpdelivery.Sender`: up to 3 providers in order, each attempt bounded (0.5–4 s), stops when the request ends, never returns provider text |
| Failure handling | `RequestOtp` withdraws a challenge whose code was not handed on (`withdrawUndeliveredOtp`) and answers `ERR_OTP_DELIVERY_FAILED`; the request still counts |
| Development provider | `otpdelivery.MockProvider` (#270), accepted only with `CS_IDENTITY_ENVIRONMENT=development` set explicitly |

These values are the code's **initial** values ("not production-tuned"). Tuning them is a separate owner decision (§10).

## 3. Why identity must keep generation and verification

1. **One security model, whichever provider is used.** Every control in §2 is implemented, tested and audited once. With a code-generating provider, the code's strength, lifetime, attempt counting and reuse rules become the vendor's. They differ between vendors and can change without notice.
2. **Provider portability (ADR-0010 §9 requires a fallback provider).** Failover between two code-generating providers is impossible: a code issued by one cannot be checked by the other. Under Option A, any provider can deliver any code, and `Sender` already fails over between them.
3. **Availability of sign-in.** With a code-generating provider, a provider outage stops verification as well as delivery: a code already received cannot be checked. Under Option A, a received code can always be verified.
4. **Auditability.** identity's security audit log (migration `0005`) records every wrong code and lockout. A vendor's verification outcomes would live in the vendor's systems.
5. **Data minimisation.** The provider learns the number and the message. Under Option A it does not also hold the authoritative verification state.
6. **No new trust boundary in the check path.** Verification never leaves identity's process, so no network failure, credential or vendor response can approve a sign-in.

**What Option A gives up:** a vendor's own fraud and SMS-pumping protections, which some code-generating products bundle. These must be provided by identity (§7) or bought separately.

## 4. Provider responsibilities and security boundaries

| | identity | Delivery provider |
|---|---|---|
| Generates the code | **Yes** | No |
| Composes the message | **Yes**, from the registered template (India DLT: the text must match it exactly) | No |
| Holds the code | Only during the send call; never stored, never logged | In transit and in its message records (a processor under DPDP) |
| Decides whether a code is right | **Yes** | **Never** |
| Rate limits and lockout | **Yes** | Its own limits may add to them, never replace them |
| Credentials | Reads the provider credential from a mounted Secret file | Issues it |

**Boundaries an adapter must keep** (already in the runbook §1 and `otpdelivery`'s contract):
- It honours the request's context and never retries internally (`Sender` owns failover).
- It never logs the number, the code, a credential, a header or the provider's response.
- It returns only fixed errors, never provider text, which may echo the number or the code.
- It sends credentials only to the provider's API host over TLS, and never follows redirects.
- It is built only when explicitly selected, and refused when partly configured.
- The Android app never sees a provider credential.

## 5. Database implications

- **None for Option A:** the schema at migration `0009` already stores `code_hash` and no number.
- **PR #271's migration `0010`** (`provider_ref`) exists only for code-generating providers. **It is not needed, and is not merged.**
- **Delivery status** (a provider's "delivered"/"failed" receipts) would need a webhook endpoint exposed to the internet, and somewhere to keep the result. Neither exists, and neither is proposed here: exposing an endpoint is an external change needing approval.

## 6. Delivery failure, retries, idempotency and duplicate messages

**Failure:**
- **A provider that fails or times out** is followed by the next provider, with the **same code**. If every provider fails, the challenge is withdrawn and the caller is told `ERR_OTP_DELIVERY_FAILED`.
- **When the request ends** (deadline or client gone), no further provider is tried. A code nobody is waiting for is not sent.

**Retries:**
- **No adapter retries internally.** A retried send after a lost answer could send a second message.
- **A user's "resend" is a new request:** a new challenge, a new code, counted against the 3-per-15-minutes limit.

**Duplicates:** the same code can arrive twice, but never two different codes from one request. If a provider timed out but delivered late, the next provider sends the same code again. Accepted.

**Idempotency:** `POST /v1/otp/request` has **no idempotency key**. A client that retries after a lost answer creates a second challenge and a second message, bounded by the rate limit. Whether to add a client idempotency key is an open question (§10).

**Provider-side idempotency:** whether Twilio's or MSG91's send API accepts an idempotency token is **not verified here**. If one exists, the challenge's request id is the natural key.

**Two live codes per number (finding):**
- A new request does **not** withdraw the number's earlier, still-valid challenge. Both stay usable until expiry, each with its own attempt budget.
- The per-number failure limit (5 per 15 minutes) bounds guessing across them.
- Whether a new request should invalidate earlier challenges is an open question (§10). Nothing is changed here.

## 7. Rate limiting and abuse prevention

**What exists:**
- per number: 3 requests and 5 failures in 15 minutes, then a fixed 15-minute lockout;
- a malformed number is refused before any work;
- a refused or rate-limited request sends nothing;
- every wrong code and lockout is audited.

**What does not exist (risks, §9):**
- **No per-source-address limit:** it waits on the trusted-proxy model (E6, ADR-0010 §10).
- **No per-device limit:** no device identity exists before enrolment.
- **No country restriction.** Any E.164 number is accepted. **SMS pumping** (an attacker requesting codes to premium or international numbers to generate traffic charges) is bounded only per number, so an attacker using many numbers is unbounded in cost. An India-only allowlist (`+91`) at request time would bound it; that is an owner decision (§10).
- **No app attestation before a send:** Play Integrity (T4) is unresolved and is not touched here.
- **No global send budget for sign-in codes.** Account-deletion codes have one; sign-in codes do not (§10).

## 8. Provider implications

### Twilio

- **Use Programmable Messaging, not Verify.** That is `POST /2010-04-01/Accounts/{AccountSid}/Messages.json`, with `To`, `Body` and a sender (`From`, or `MessagingServiceSid`), authenticated with an API key (SID and secret).
- **The adapter is a small `otpdelivery.Provider`:** about the size of `internal/twilioverify`'s client, with no change to identity's flow, no migration and no `OtpVerifier` port.
- **Configuration would be:**
  - `CS_IDENTITY_TWILIO_ACCOUNT_SID` (now used, in the URL);
  - `CS_IDENTITY_TWILIO_API_KEY_SID`;
  - `CS_IDENTITY_TWILIO_API_KEY_SECRET_FILE`;
  - a sender: a Messaging Service SID, or the DLT-registered header;
  - the template's text and, for India, its DLT template id, where Twilio requires them.
  - The Verify service SID is no longer needed.
- **India (unverified; to confirm with Twilio before any build is relied on):**
  - how Programmable Messaging carries DLT data on the domestic route: the entity id, header and template id, the PE–TM chain, and whether they are account configuration or per-message parameters;
  - that the body must match the registered template exactly.
- **What PR #271's code can be reused for:** its HTTP discipline, which is provider-independent. That covers the network-guarded tests, fixed errors, no retries, no redirects, bounded calls, loopback-only test base URLs, and the secret-file loading and selection rules. **Not reusable:** the `OtpVerifier` port, migration `0010`, and the Verify calls.

### MSG91

- MSG91 offers a provider-generated OTP API **and** a templated SMS ("flow") API. **Under Option A only the templated SMS API is used,** with identity's code as the template's variable. *To be confirmed against MSG91's current documentation.*
- The same adapter pattern applies: an `otpdelivery.Provider`, credentials as a mounted file, DLT template and header ids as configuration.

### Two providers

ADR-0010 §9 requires a fallback. Under Option A, Twilio and MSG91 (or any pair) can be the primary and the fallback in `Sender`, with no change to identity.

## 9. Testing and migration requirements

**Before any adapter is merged:**
- Unit tests against a loopback server only, behind a transport that fails on any other host (as in PR #271).
- Coverage for:
  - the request shape;
  - every error status mapped to a fixed error;
  - no retries and no redirects;
  - timeout and cancellation;
  - configuration validation without echoing values;
  - **no request for invalid input**;
  - the message body matching the template.
- A `Sender` test with the adapter as primary and the mock as fallback (failover).
- The existing flow tests continue to pass unchanged: generation, expiry, attempts, reuse, the rate limits and the lockout (`internal/app`, `internal/otpdelivery`). **No security test is weakened.**
- Lint, format, the real-PostgreSQL suite (`identity-postgres`), and every CI check.
- **The independent human review (OD-12)** of the adapter and its configuration.

**Before activation (each needs the owner):**
- the SMS and DLT category approval (R1);
- DLT registration and templates after the naming review (F26);
- the provider's India domestic route;
- account upgrade and billing;
- the credential as a mounted Secret file;
- egress to the provider's API host only;
- the runbook's S1–S3 on real handsets, recorded with evidence.

**Migration:** no schema migration. Switching provider is a configuration change, and challenges in flight are unaffected, because verification never involved the provider.

## 10. Risks and unresolved questions

| # | Item | Kind |
|---|---|---|
| Q1 | **Formal acceptance of this ADR**, after the independent human review | Owner, with OD-12 |
| Q2 | A **country allowlist** (India only at launch?) against SMS pumping | Owner (security and product) |
| Q3 | A **global sign-in send budget** (per hour), like the deletion-code budget | Owner or SRE: a value |
| Q4 | Whether a new request **invalidates the number's earlier live challenges** | Owner (security policy) |
| Q5 | A **resend cooldown** (none exists) | Owner (security policy) |
| Q6 | A **client idempotency key** on `POST /v1/otp/request` | Owner, or delegated (API contract) |
| Q7 | **Tuning** the initial limits (attempts, window, lockout) | Owner (security policy) |
| Q8 | **Provider choice and order** (primary, fallback): Twilio Messaging, MSG91, other | Owner (R1, spending) |
| Q9 | **India delivery facts per provider** (DLT parameters, PE–TM chain, domestic route) | Owner with the provider; unverified |
| Q10 | **Delivery receipts** (a webhook endpoint) | Owner (external exposure) |
| R1 | SMS is interceptable (SIM swap, SS7, malware). ADR-0010 accepts this; it is unchanged by Option A | Accepted risk (ADR-0010) |
| R2 | The provider sees numbers and codes in transit and keeps message logs | DPDP processor terms (E9, L1) |
| R3 | One HMAC key covers codes, refresh tokens and pseudonyms; separation is ADR-0030 (Proposed) | Pending ADR-0030 |
| R4 | PR #270 was merged under the delegated-routine judgement **before** the owner's statement that authentication changes are high-risk. It is included in the review scope for a post-merge review | OD-12 package |

## Approval record

| Step | Who | Status |
|---|---|---|
| Owner preference: Option A | Repository owner | **Recorded, 2026-11-09** (preference, not formal approval) |
| AI-performed analysis | Claude Code | Done, 2026-11-09 (this document). Not an independent review |
| Independent human security review | A qualified reviewer independent of the drafter (OD-12) | **Not available; not started** |
| Formal decision (Accepted / amended / rejected) | Repository owner | **Pending**, after the review |
