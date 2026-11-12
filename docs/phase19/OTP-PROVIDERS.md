# OTP providers: architecture, the development mock, and production activation

- **As of:** 2026-11-09 (the owner's Option A preference recorded; ADR-0033 Proposed).
- **Status:**
  - **The development mock is implemented.** It sends nothing, and is refused outside an explicitly development environment.
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

- **Why an unset environment is not enough for the mock:** elsewhere, an unset `CS_IDENTITY_ENVIRONMENT` reads as development (OD-15's known limit). A deployed pod that lost the variable must never sign anyone in with a code read from an outbox.
- `CS_IDENTITY_OTP_MOCK_OUTBOX_FILE` is refused unless the mock is selected.
- Values are exact: lower case, no surrounding whitespace. An unknown value is refused **without being echoed**, since it may be a misplaced credential.
- These variables are read only when identity is activated, and never activate it. Startup logs the provider name (`otp_provider`), and warns when it is the mock.
- **Production builds:** the server is one binary for every tier, so the mock is excluded at startup by environment, not at build time. The Android app has no provider code and never sees a code or credential.

### Real delivery-only providers (`internal/bootstrap/otp_providers.go`; held until the owner accepts ADR-0033 for M1)

`CS_IDENTITY_OTP_PROVIDER` may name real providers, comma-separated and without spaces, in the order they are tried: `msg91`, `twilio`, `msg91,twilio` or `twilio,msg91`.

| Provider | Variables (all required when selected) |
|---|---|
| `msg91` | `CS_IDENTITY_OTP_MSG91_AUTH_KEY_FILE` (a mounted Secret file); `CS_IDENTITY_OTP_MSG91_SIGNIN_FLOW_ID` (the registered flow); `CS_IDENTITY_OTP_MSG91_CODE_VARIABLE` (the flow's variable that receives the code) |
| `twilio` | `CS_IDENTITY_OTP_TWILIO_ACCOUNT_SID_FILE`, `CS_IDENTITY_OTP_TWILIO_API_KEY_SID_FILE`, `CS_IDENTITY_OTP_TWILIO_API_KEY_SECRET_FILE` (mounted Secret files); `CS_IDENTITY_OTP_TWILIO_MESSAGING_SERVICE_SID`; `CS_IDENTITY_OTP_TWILIO_SIGNIN_TEMPLATE` (the DLT-registered text, with `{code}` once) |

**What startup refuses:**
- any selected provider with a missing or blank variable;
- **any provider variable set for a provider that is not selected** (no half-configured provider);
- the same provider twice, or more than two;
- the mock combined with a real provider;
- a mock outbox file with a real provider.

**Startup reads the credential files,** so a missing, relative or malformed one stops startup without echoing its content.

**What the adapters enforce:**
- **India only:** `+91` mobile numbers, refused before anything is sent. There is no international route.
- **Delivery only:** identity's code, in the registered template.
- **Transport:** TLS to the provider's API host only, with no redirects.
- **Errors** are fixed values, never provider text.

**Each attempt is bounded at 2 s,** and identity fails over to the next provider in order.

**Delivery is counted** in `identity_otp_deliveries_total{provider,outcome}` once the HTTP API is registered. This applies to the mock too.

**Account-deletion codes are not wired:** enabling them is decision O13.

**None of this activates anything.** It needs R1 (an account, DLT entity, header and templates, the India domestic route, billing) and the owner's ADR-0033 decision under rule 8a.

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

- **The owner's preferred architecture is Option A** (2026-11-09; [ADR-0033](../adr/0033-otp-ownership-and-delivery-only-sms-providers.md), **Proposed**): identity generates, hashes and verifies every code, and a provider only delivers it. That is what this document describes. It is a preference, not yet a formal approval: acceptance needs the independent human review (OD-12; [review package](OD-12-OTP-REVIEW-PACKAGE.md)).
- **A real provider adapter is not merged.** The Twilio Verify adapter (draft PR #271) generates and checks codes at Twilio, which contradicts Option A: **it is not selected and will not be merged.** A delivery-only adapter (Twilio Programmable Messaging and/or MSG91) is assessed in ADR-0033 §8, and would need its own review before merge and the owner's approvals before activation.
- **Open owner decisions this touches:**
  - the SMS provider and the SMS and DLT approval (E2, R1);
  - a resend cooldown, if any (§1);
  - the deletion-code budget (O13);
  - tuning of the initial limits;
  - the independent human reviewer for high-risk changes (OD-12).
- **Production readiness is unchanged.** M1 still needs a delivered SMS, staging and Play (`M1-APPROVAL-REQUESTS.md`). The mock is for development and tests only.
