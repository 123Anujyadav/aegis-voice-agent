# OTP providers: architecture, the development mock, and production activation

- **As of:** 2026-11-09 (the owner's Option A preference recorded; ADR-0033 Proposed).
- **Status:**
  - **The development mock is implemented.** It sends nothing, and is refused outside an explicitly development environment.
  - **No real provider is connected.** Staging and production deliver no code: identity refuses OTP requests (`ERR_OTP_DELIVERY_UNAVAILABLE`), as before.
  - **No SMS has been sent, and no external account, API or route has been used or activated.**
  - **The F-1 controls are implemented** (2026-11-12): a real provider cannot start without a tester allowlist and a global hourly cap (§2).
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
- **No per-device or per-source limit.** There is no per-device limit (no device identity exists before enrolment), and no per-source-address limit until the trusted-proxy model is chosen (E6, ADR-0010 §10). With a real provider, the F-1 controls in §2 (a tester allowlist and a global hourly cap) bound the cost of many numbers.
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

### Real delivery-only providers (`internal/bootstrap/otp_providers.go`; ADR-0033 accepted for M1, 2026-11-12)

**MSG91 is the owner's selected primary provider (2026-11-13).** The M1 selection is `CS_IDENTITY_OTP_PROVIDER=msg91` **alone**: one provider means at most one request per code (no failover, no retry). Twilio is never used unless named explicitly. MSG91's wire format and the facts behind this section are in [`MSG91-INTEGRATION.md`](MSG91-INTEGRATION.md).

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

**Each attempt is bounded at 2 s.** Nothing retries the same provider. With two providers, identity fails over to the next in order: a timed-out first attempt may still have been delivered, so the subscriber may receive the same code twice (never two different codes). With `msg91` alone, a code is handed over in at most one request.

**MSG91's request id** for each accepted message is logged (`provider_request_id`, with `otp_provider`), never the number or the code, so the message can be traced in MSG91's SMS Logs. **Accepted is not delivered:** delivery is read from MSG91.

**Delivery is counted** in `identity_otp_deliveries_total{provider,outcome}` once the HTTP API is registered. This applies to the mock too.

**Account-deletion codes are not wired:** enabling them is decision O13. **The tester allowlist applies to them too** (since 2026-11-13, `web_erasure.go`): while it is configured, a deletion code for a number outside it is refused first, as at sign-in. They have their own hourly budget (migration 0009) instead of the sign-in cap. **A source scan** (`internal/bootstrap/deletion_unwired_test.go`) fails if the identity binary's production code installs a deletion sender or budget, or configures a deletion template.

**None of this activates anything.** It needs R1 (an account, DLT entity, header and templates, the India domestic route, billing) and the owner's ADR-0033 decision under rule 8a.

### Sign-in delivery controls (F-1): required with a real provider

The owner approved both controls for M1 on 2026-11-12 ([ADR-0033](../adr/0033-otp-ownership-and-delivery-only-sms-providers.md) approval record; [assurance](ADR-0033-M1-INTERNAL-ASSURANCE.md) F-1). They bound SMS pumping, where many numbers would otherwise mean many paid messages. Code: `internal/app/signin_controls.go`, `internal/bootstrap/otp_controls.go`, `internal/pgstore/signin_budget.go`.

| Variable | Value |
|---|---|
| `CS_IDENTITY_OTP_ALLOWED_NUMBERS_FILE` | The absolute path of a **mounted Secret file** listing the approved M1 tester numbers, one per line, as `+91` and ten digits, the first 6–9. Blank lines and lines starting with `#` are ignored, and surrounding spaces are removed. It must list at least one number, and none twice. |
| `CS_IDENTITY_OTP_SIGNIN_HOURLY_CAP` | A decimal integer from 1 to 10000: the most sign-in codes handed to the provider in one clock hour (UTC), across every number and every replica. |

**When each is required:**

| Provider | The two variables |
|---|---|
| `msg91`, `twilio`, or both | **Both required.** |
| `mock` (development only) | Optional, to exercise the controls. If either is set, both are required. |
| `none` (the staging overlay's value) | **Refused**, as stray configuration. |

**Startup is refused (`ErrInvalidConfig`), failing closed, when:**
- either variable is missing or blank;
- the file is unreadable, not a regular file, relative, larger than 64 KiB, or lists no number;
- a line is not a canonical Indian mobile number;
- a number is listed twice;
- the cap is not a decimal integer from 1 to 10000.

**Startup errors never show a number, the file's path or the cap's value.** They name the variable and, for the file, a line number.

**What happens to a request:**
1. **A number not on the list** is answered `503 ERR_OTP_DELIVERY_UNAVAILABLE`, byte for byte the answer when no delivery is configured. It is refused before anything else: no challenge is created, and the number's allowance and the hourly budget are untouched.
2. **A listed number** then passes the existing per-number limits.
3. **The hourly cap is reserved last,** immediately before the code is handed to the provider. It is one atomic database statement (`identity_signin_delivery_budget`, migration `0010`), exact across concurrent requests and across identity replicas.
4. **Once the hour's cap is spent, or if the reservation fails** (a database error fails closed), nothing is sent. The challenge is withdrawn as for a failed delivery, and the answer is the same `503 ERR_OTP_DELIVERY_UNAVAILABLE`. The request still counts against that number's own allowance.

**What is counted:** each code handed to the provider, whether or not the provider then delivers it. A refused request counts nothing. The count is per clock hour and resets at the next hour. Housekeeping deletes old hours.

**Not counted:** account-deletion codes. They are not wired (O13), and they have their own budget.

**Tester numbers are personal data:**
- They are held only in the mounted Secret file, and read once at startup.
- In memory, identity keeps only their HMAC pseudonyms (under the identity key).
- They are never logged, and never appear in an error or response.
- Nothing is audited per refusal: a number off the list could otherwise flood the durable audit log. Refusals are counted in `identity_auth_requests_total{endpoint,outcome="otp_delivery_unavailable"}`.

**Defence in depth:** `installOtpDelivery` is the only code that installs a sender. It installs the controls first, and refuses a real provider without them, whatever produced the configuration. A test checks that no other code installs a sender.

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
| F-1: a number off the list spends nothing; the cap is exact (N sent, N+1 refused and withdrawn); it resets at the next hour; 50 concurrent requests with cap 10 send exactly 10; a database error sends nothing; no controls means no change | `internal/app/signin_controls_test.go` |
| F-1: the hourly count is atomic per hour on both backends, across two pools; housekeeping; migration `0010` | `internal/pgstore/signin_budget_test.go` (PostgreSQL in CI's `identity-postgres`) |
| F-1: both variables required with a real provider, optional with the mock, refused with none; no number in any error; the only sender installation; two runtimes share one cap | `internal/bootstrap/otp_controls_test.go`, `otp_controls_postgres_test.go` |
| F-1 through HTTP: refusals are byte-for-byte "delivery unavailable", and nothing leaks | `internal/httpapi/signin_controls_test.go` |
| MSG91: the verified Send SMS contract (headers, exactly the documented body fields, `91` + number, the code variable only); the request id reported only when well-formed; one request per call whatever the answer; alone in the Sender, at most one request per code | `internal/otpdelivery/msg91_test.go` |
| Deletion codes: the allowlist applies first and spends nothing; nothing in production wires them (source scan) | `internal/app/web_erasure_allowlist_test.go`, `internal/bootstrap/deletion_unwired_test.go` |

**No test makes a network request.** The mock delivers in-process, and the adapters are tested against local TLS stand-ins. **Every package whose tests build a real provider client** routes all outbound HTTP(S) to a dead loopback proxy in `TestMain` (`main_test.go`), pinned by `no_network_test.go`: `internal/otpdelivery`, `internal/bootstrap`, `internal/playintegrity`, and `packages/go/aiproviders/{anthropic,elevenlabs}`, `packages/go/speechproviders/{deepgram,google}`. **A new provider package must add the same guard.**

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
- [x] **Engineering:** the delivery-only MSG91 and Twilio adapters merged (#301, `0a8e88d`, 2026-11-12), with the F-1 controls; MSG91 hardened for primary use (2026-11-13). Internal assurance only (rule 8a); the independent review is required before M2.
- [ ] **SRE:** the credential as a mounted Secret file, and egress from identity to the provider only (a network-policy change: approval).
- [ ] **Owner:** the M1 tester numbers and the hourly cap (F-1, §2).
- [ ] **SRE:** the tester list as a mounted Secret file (`CS_IDENTITY_OTP_ALLOWED_NUMBERS_FILE`) and `CS_IDENTITY_OTP_SIGNIN_HOURLY_CAP`. **Startup refuses a real provider without both.** Migrate the schema to `0010` first.
- [ ] **SRE:** set `CS_IDENTITY_OTP_PROVIDER` to the provider in the pod specification. **Never `mock`.**
- [ ] **Test:** the runbook's §1 acceptance (S1–S3) on real handsets, recorded with evidence.
- [ ] **Monitor:** `identity_otp_deliveries_total` by provider and outcome.

## 7. Known limitations and open decisions

- **Option A** ([ADR-0033](../adr/0033-otp-ownership-and-delivery-only-sms-providers.md)): identity generates, hashes and verifies every code, and a provider only delivers it. **Accepted for M1 on internal assurance (2026-11-12, rule 8a)**; the independent human review (OD-12; [review package](OD-12-OTP-REVIEW-PACKAGE.md)) is required before M2.
- **The delivery-only adapters are merged but not activated** (#301). **No real SMS has been sent or validated.** The Twilio Verify adapter (draft PR #271) generates and checks codes at Twilio, which contradicts Option A: **it is not selected and will not be merged.**
- **Open owner decisions this touches:**
  - the SMS and DLT approval (E2, R1); the provider is chosen (MSG91, 2026-11-13), not activated;
  - a resend cooldown, if any (§1);
  - the deletion-code budget (O13);
  - tuning of the initial limits;
  - the independent human reviewer for high-risk changes (OD-12).
- **Production readiness is unchanged.** M1 still needs a delivered SMS, staging and Play (`M1-APPROVAL-REQUESTS.md`). The mock is for development and tests only.
