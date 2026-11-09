# OTP providers: architecture, the development mock, Twilio Verify, and production activation

- **As of:** 2026-11-09.
- **Status:**
  - **The development mock is implemented.** It sends nothing, and is refused outside an explicitly development environment.
  - **A Twilio Verify adapter is implemented and DISABLED** (§8). It is selected nowhere, and no Twilio credential, Verify service or route exists for this project. Activating it needs the owner's approvals (§5, §6).
  - **No real provider is connected.** Staging and production deliver no code: identity refuses OTP requests (`ERR_OTP_DELIVERY_UNAVAILABLE`), as before.
  - **No SMS has been sent, and no external account, API or route has been used or activated.**
- **Related:**
  - [`../runbooks/identity-provider-onboarding.md`](../runbooks/identity-provider-onboarding.md): onboarding a real provider;
  - [`M1-APPROVAL-REQUESTS.md`](M1-APPROVAL-REQUESTS.md) R1: the SMS and DLT approval request;
  - [`INTERNAL-TESTING-SETUP.md`](INTERNAL-TESTING-SETUP.md) A1–A5.

## 1. Architecture

One sign-in flow, in identity (`services/go/identity`). Nothing is duplicated, and edge-api and the app are unchanged.

| Layer | Where | What it does |
|---|---|---|
| The flow | `internal/app` (`RequestOtp`, `ValidateOtp*`, `checkCode`) | Mints each code (`crypto/rand`, 6 digits), stores **only its keyed hash**, and enforces expiry, the attempt limit, single use, and the per-number rate limits and lockout. The number itself is never stored (an HMAC pseudonym is). |
| Delivery | `internal/otpdelivery` | `Sender`: an ordered failover over up to 3 `Provider`s, each attempt time-bounded, never returning provider text. `MockProvider`: the development provider. |
| Provider-generated codes | `internal/app` (`OtpVerifier`), `internal/twilioverify` | A provider that generates, sends **and checks** codes itself (Twilio Verify). identity stores its verification id (`provider_ref`, migration `0010`) instead of a hash, and asks it whether a code is right. Every other control in the table below stays identity's (§8). |
| Selection | `internal/bootstrap/otp.go` | `CS_IDENTITY_OTP_PROVIDER` chooses the provider at startup, under the rules in §2. |
| HTTP | `internal/httpapi` | `POST /v1/otp/request` answers 202 with a request id and the lifetime. **The code is never in a response, a header or a log.** |

### The values identity enforces (from the code; not changed here)

| Control | Value | Source |
|---|---|---|
| Code length | 6 digits, uniformly random | `otpDigits`, `secureOtpCode` |
| Expiry | 5 minutes | `app.OtpLifetime` |
| Wrong codes per challenge | 3; the third exhausts it | `maxOtpAttempts` |
| Requests per number | 3 in any 15 minutes | `defaultOtpRatePolicy` |
| Failed verifications per number | 5 in any 15 minutes, then a fixed 15-minute lockout | `defaultOtpRatePolicy`; fixed, not escalating, on purpose (the comment there, and ADR-0029's ratification brief §3a) |
| Reuse | A used code is consumed atomically and never accepted again | `ConsumeOtp` |

These are the existing **initial** values, "not production-tuned" in the code's own words. Tuning them is a security-policy decision for the owner, and nothing here changes them.

### Gaps, recorded and not invented

- **No resend cooldown.** Nothing sets a minimum interval between two requests for one number. A resend is a new request with a new code, bounded only by the 3-per-15-minutes limit. A cooldown, its length and its error are a policy decision for the owner.
- **Limits are per number only.** There is no per-device limit (no device identity exists before enrolment), and no per-source-address limit until the trusted-proxy model is chosen (E6, ADR-0010 §10).
- **Account-deletion codes** (web deletion) are not delivered by the mock. They have their own DLT template, budget and decision (O13).
- **Delivery metrics:** the mock is built without the `identity_otp_deliveries_total` hook. A real provider's sender must be built with it (the runbook §1).

## 2. Choosing the provider: `CS_IDENTITY_OTP_PROVIDER`

| `CS_IDENTITY_ENVIRONMENT` | Unset | `mock` | `none` | Anything else |
|---|---|---|---|---|
| `development` (**set explicitly**) | **mock** | mock | none | startup refused |
| unset | none | **startup refused** | none | startup refused |
| `staging`, `production` | none | **startup refused** | none | startup refused |

`twilio` is accepted in any environment, but only with all four `CS_IDENTITY_TWILIO_*` variables (§8); any of them set **without** `twilio` stops startup.

- **Why an unset environment is not enough for the mock:** elsewhere, an unset `CS_IDENTITY_ENVIRONMENT` reads as development (OD-15's known limit). A deployed pod that lost the variable must never sign anyone in with a code read from an outbox.
- `CS_IDENTITY_OTP_MOCK_OUTBOX_FILE` is refused unless the mock is selected.
- Values are exact: lower case, no surrounding whitespace. An unknown value is refused **without being echoed**, since it may be a misplaced credential.
- These variables are read only when identity is activated, and never activate it. Startup logs the provider name (`otp_provider`), and warns when it is the mock.
- **Production builds:** the server is one binary for every tier, so the mock is excluded at startup by environment, not at build time. The Android app has no provider code and never sees a code or credential.

## 3. Local development with the mock

1. Run identity as usual for development (the activation variables, and `service migrate` on a local PostgreSQL), plus:

   ```sh
   CS_IDENTITY_ENVIRONMENT=development           # must be set explicitly
   CS_IDENTITY_OTP_PROVIDER=mock                 # optional: the default in development
   CS_IDENTITY_OTP_MOCK_OUTBOX_FILE=/tmp/lily-otp-outbox.txt   # optional
   ```

2. Request a code from the app or with `POST /v1/otp/request`. **No SMS is sent.**
3. **Read the code** from the outbox file. Each code is one line, `<expires-at> <number> <code>`, and the file is created with mode 0600:

   ```sh
   tail -n 1 /tmp/lily-otp-outbox.txt
   ```

   Without an outbox file, codes stay in memory only (tests read them with `Outbox.Latest`), and a person cannot retrieve them.
4. Enter the code in the app as usual. Every control in §1 applies exactly as it would with a real provider.

**The outbox holds raw numbers and live codes.** Use it only on your own machine; never point it at a shared or synced folder.

## 4. Tests

| What | Where |
|---|---|
| A 6-digit code is generated and reaches only the mock | `internal/otpdelivery/mock_test.go` |
| The correct code signs in; a wrong one is refused, then the right one still works | same |
| An expired code is refused even when correct | same |
| A used code cannot be reused | same |
| Three wrong codes exhaust the challenge | same |
| A resend gets a fresh challenge that accepts only its own code (no cooldown: §1) | same |
| The 4th request in 15 minutes is rate-limited and sends nothing | same |
| Malformed numbers and unknown request ids are refused | same |
| A failed delivery withdraws the challenge; a cancelled send delivers nothing | same |
| The code and the number never appear in the API response, its headers or the logs | same |
| The outbox file's format and 0600 mode; the outbox is bounded | same |
| Default selection; the mock refused outside explicit development; invalid values | `internal/bootstrap/otp_test.go` |
| The failover, timeouts and health of `Sender` | `internal/otpdelivery/otpdelivery_test.go`, `deletion_test.go` |
| Every flow control above, at the application layer | `internal/app` tests |

**No test makes a network request:** the mock delivers in-process.

Run them:

```sh
cd services/go/identity
go test ./internal/otpdelivery/ ./internal/bootstrap/ ./internal/app/ ./cmd/server/
```

## 5. India: what a real provider needs first

**Creating an account, a Verify service or an API key does not, by itself, enable delivery to Indian numbers on the domestic route.** Before any real code can be delivered in India:

1. **DLT Principal Entity registration** (TRAI), on an operator's DLT portal.
2. **An approved Sender ID (header).**
3. **An approved OTP content template**, with the code as its variable, in the product's name. The naming review (F26) comes first: SMS text must match its template exactly.
4. **The PE–TM chain:** the Principal Entity must declare the provider (the telemarketer) and any intermediaries in its delivery chain, as DLT now requires.
5. **The provider's India domestic route approved for the account**, including its own registration of the entity, header and template where the provider requires it.
6. **The provider account upgraded, and billing approved** by the owner (the SMS and DLT spending category).

**No international-route fallback is to be used.** The intended route is domestic, with DLT.

## 6. Production activation checklist

Each step needs the owner's approval where marked. **None is done.**

- [ ] **Owner:** approve the SMS and DLT category, the provider and its contract (`M1-APPROVAL-REQUESTS.md` R1).
- [ ] **Owner:** the naming review (F26), then DLT steps 1–4 (§5) and the provider's domestic route (5).
- [ ] **Owner:** the provider account upgraded and billing approved (6).
- [ ] **Engineering:** the provider's adapter merged, after its own review (see §7).
- [ ] **SRE:** the credential as a mounted Secret file, and egress from identity to the provider only (a network-policy change: approval).
- [ ] **SRE:** set `CS_IDENTITY_OTP_PROVIDER` to the provider in the pod specification. **Never `mock`.**
- [ ] **Test:** the runbook's §1 acceptance (S1–S3) on real handsets, recorded with evidence.
- [ ] **Monitor:** `identity_otp_deliveries_total` by provider and outcome.

## 7. Known limitations and open decisions

- **The Twilio Verify adapter (§8) changes who generates and checks codes.** It moves code generation and comparison from identity to Twilio, for challenges started through it. `ARCHITECTURE_FREEZE.md` §6.1 treats authentication changes as high-risk, so it needs the owner's decision and an independent human review (OD-12) before it is merged and before it is activated.
- **Open owner decisions this touches:**
  - the SMS provider and the SMS and DLT approval (E2, R1);
  - a resend cooldown, if any (§1);
  - the deletion-code budget (O13);
  - tuning of the initial limits;
  - the independent human reviewer for high-risk changes (OD-12).
- **Production readiness is unchanged.** M1 still needs a delivered SMS, staging and Play (`M1-APPROVAL-REQUESTS.md`). The mock is for development and tests only.

## 8. Twilio Verify (implemented, DISABLED)

**Nothing here has been activated.** No Twilio API was called, no SMS was sent, and no account, Verify service, API key, route or billing setting was created or changed. Every test runs against a loopback test server, behind a transport that fails the test for any other host.

### How it works

| Step | identity (unchanged controls) | Twilio Verify |
|---|---|---|
| Request | Canonical number check, then the per-number request limit (3 per 15 minutes). **Only then** is Twilio asked | `POST /v2/Services/{VA…}/Verifications` with `To` and `Channel=sms`: Twilio generates the code and sends it |
| Store | The challenge stores the verification id (`VE…`, not personal data), the number's pseudonym, and identity's own 5-minute expiry. **No code and no hash** | — |
| Check | Attempt limit (3 per challenge), expiry, the per-number failure reservation and lockout, **then** Twilio is asked; single use is identity's atomic consume | `POST /v2/Services/{VA…}/VerificationCheck` with `VerificationSid` and `Code` (never the number) |

- **Outcomes:** `approved` signs in. `pending`, a verification Twilio no longer has (404), or a code that is not digits counts as a **wrong code**, against identity's limits. A Twilio failure (timeout, 5xx, 429, an untrustworthy answer) is **not** a wrong code: nothing is counted, the challenge stays, and the API answers 503 `ERR_TEMPORARILY_UNAVAILABLE` (an existing code; the API contract is unchanged).
- **A start that fails** leaves no challenge, and the request still counts against the number. The API answers 503 `ERR_OTP_DELIVERY_FAILED`, as for any provider.
- **No retries.** A retried start could send a second SMS; a retried check could count twice at Twilio. Each call is bounded (4 s by default, 8 s at most) and by the request's own deadline.
- **Duplicate requests:** bounded by identity's per-number limit before Twilio is called. Twilio itself also reuses a pending verification for the same number.
- **Responses are trusted only if they name the configured account and Verify service** (and, for a check, the same verification). Redirects are never followed.
- **Nothing is logged** by the adapter. Errors are fixed values: never the number, the code, a credential, a header or any of Twilio's response text.
- **Credentials:** the API key secret is a **mounted Secret file**, read once at startup, and sent only as HTTP Basic credentials to `https://verify.twilio.com/v2`. The client refuses any other base URL except a loopback test server. The Android app never sees a credential or a code.
- **Codes identity minted earlier** (before a switch) are still checked locally; Twilio is never asked about them.
- **Only SMS** is requested. **No international-route fallback** is implemented; the route is Twilio account configuration (§5).

### Configuration

The task's names, mapped to identity's conventions (the `CS_IDENTITY_` prefix, and the secret as a file, as the runbook §1 requires):

| Task name | identity variable | Kind |
|---|---|---|
| `OTP_PROVIDER` | `CS_IDENTITY_OTP_PROVIDER` = `twilio` | configuration |
| `TWILIO_ACCOUNT_SID` | `CS_IDENTITY_TWILIO_ACCOUNT_SID` | configuration (`AC` + 32 hex) |
| `TWILIO_API_KEY_SID` | `CS_IDENTITY_TWILIO_API_KEY_SID` | configuration (`SK` + 32 hex) |
| `TWILIO_API_KEY_SECRET` | `CS_IDENTITY_TWILIO_API_KEY_SECRET_FILE` | **path to a mounted Secret file** (never an environment value) |
| `TWILIO_VERIFY_SERVICE_SID` | `CS_IDENTITY_TWILIO_VERIFY_SERVICE_SID` | configuration (`VA` + 32 hex) |

**Template, placeholders only.** These values are deliberately invalid: identity refuses to start with them.

```sh
# NOT FOR USE until the owner's approvals in §5 and §6 exist.
CS_IDENTITY_OTP_PROVIDER=twilio
CS_IDENTITY_TWILIO_ACCOUNT_SID=<AC followed by 32 hex digits>
CS_IDENTITY_TWILIO_API_KEY_SID=<SK followed by 32 hex digits>
CS_IDENTITY_TWILIO_API_KEY_SECRET_FILE=/var/run/secrets/identity/twilio-api-key-secret
CS_IDENTITY_TWILIO_VERIFY_SERVICE_SID=<VA followed by 32 hex digits>
```

- The secret file holds the API key secret only; a trailing newline is tolerated.
- Values are validated at startup, before any connection; errors name the variable, never the value or the path.

### Activation (in addition to §6)

- [ ] **Owner:** decide the high-risk change (Twilio generates and checks codes), with an independent human review (OD-12).
- [ ] **Owner:** approve the SMS and DLT category with Twilio as the provider (`M1-APPROVAL-REQUESTS.md` R1).
- [ ] **Owner, with Twilio:** confirm how Verify's SMS reaches Indian numbers on the **domestic** route: whose DLT entity, header and template it uses, and the PE–TM chain (§5). **Unverified here.** Creating a Verify service or an API key does not establish it.
- [ ] **Owner:** upgrade the Twilio account and approve billing.
- [ ] **SRE:** run `identity migrate` (adds `0010`) before rolling out the image.
- [ ] **SRE:** the API key secret as a mounted Secret file; egress from identity to `verify.twilio.com` only.
- [ ] **Test:** the runbook §1 acceptance on real handsets, recorded with evidence.

### Tests

| What | Where |
|---|---|
| Request shape (path, form, Basic credentials); outcomes; every error status; no retries; no redirects followed; untrustworthy answers refused; timeout and cancellation; no request for invalid arguments; configuration validated without echoing; the network guard | `internal/twilioverify/twilioverify_test.go` |
| The flow with a provider that generates codes: no code minted, the id stored, approved once, reuse refused, wrong codes exhaust the challenge, expiry before the provider, provider failure uncounted, start failure leaves nothing, the rate limit before the provider, invalid ids, inconsistent challenges, earlier local codes | `internal/app/otp_verifier_test.go` |
| Selection, the four variables, stray variables, the secret file, the adapter's mapping | `internal/bootstrap/otp_twilio_test.go` |
| Migration `0010`: the round trip, and the database refusing both, neither or a malformed id | `internal/pgstore/provider_ref_test.go` (real PostgreSQL) |
