# MSG91 integration: the primary SMS provider for sign-in codes (2026-11-13)

- **Decision:** the owner selected **MSG91 as the primary SMS/OTP delivery provider** on 2026-11-13. Twilio stays in the code as an optional second provider, and is never used unless named.
- **What this document holds:**
  1. what is built;
  2. every SMS use case;
  3. MSG91's API, as verified from MSG91's own documentation;
  4. the configuration;
  5. the security evidence;
  6. the India DLT checklist;
  7. the Startup Benefits eligibility checklist;
  8. the plan for **one** controlled test transaction;
  9. a draft confirmation email to MSG91;
  10. the owner's remaining approvals.
- **Status, stated plainly:**
  - **No real SMS has been sent, and no MSG91 call has been made.** The adapter is tested against local stand-ins only.
  - **No MSG91 account information has been seen.** Nothing about the owner's account, its eligibility or its balance is verified here.
  - **No startup benefit has been claimed.**
- **Not authorised by the selection:**
  - live SMS sending;
  - a paid test transaction;
  - activating MSG91 for production;
  - DLT registration fees;
  - SMS credits or subscriptions;
  - deployment;
  - production credentials;
  - account-deletion SMS (decision O13).

## 1. What is built (all merged or in this change; nothing activated)

| Piece | Where | State |
|---|---|---|
| **Delivery-only MSG91 adapter:** identity's code is sent through a DLT-registered MSG91 template ("flow"). MSG91's own OTP API, which generates and verifies codes, is not used (ADR-0033) | `services/go/identity/internal/otpdelivery/msg91.go` | Merged (#301 `0a8e88d`). Hardened 2026-11-13: `realTimeResponse`, request-id capture, the no-retry contract |
| **Selection:** `CS_IDENTITY_OTP_PROVIDER=msg91` | `internal/bootstrap/otp.go`, `otp_providers.go` | Merged. **Default: `none`** (or the mock, in explicit development only) |
| **F-1 controls:** the tester allowlist and the global hourly cap, both required with any real provider | `internal/app/signin_controls.go`, `internal/pgstore/signin_budget.go`, migration `0010` | Merged (#301) |
| **The allowlist also covers account-deletion codes**, which are not wired (O13) | `internal/app/web_erasure.go` | This change |
| **MSG91's request id is logged** for each accepted message: `provider_request_id`, never the number or the code | `cmd/server/identity.go`, `internal/bootstrap/runtime.go` | This change |
| **Network isolation of every provider test** (dead loopback proxy, pinned) | `main_test.go` + `no_network_test.go` in 7 packages (§5) | This change |

**Who does what (ADR-0033):**
- **Identity:** generates the code (6 digits, `crypto/rand`), stores only its HMAC, sets the 5-minute expiry, verifies it in constant time, and enforces every limit.
- **MSG91:** receives the code once, as the value of the template's variable, and delivers it.
- **The Android app:** holds no provider credential and calls no provider. It talks only to the identity API (release permissions: no SMS permission).

## 2. SMS use-case inventory (from `main` `e42ccf2`)

| # | Use case | Status in code | Caller / endpoint | Needed? | MSG91 needs | Consent, privacy, DLT | Decisions | Milestone |
|---|---|---|---|---|---|---|---|---|
| 1 | **Sign-up / phone verification** | **Same flow as sign-in:** the first successful validation creates the account. There is no separate sign-up | `POST /v1/otp/request`, `POST /v1/otp/validate`; Android `PhoneVerificationViewModel` → `IdentityClient.requestOtp` | **Yes** | The sign-in flow (below) | The user-initiated OTP is a DLT service-implicit message; the template states its purpose | ADR-0010 §5(a); ADR-0033 (M1); ADR-0032 (Proposed: email-first) | **M1** |
| 2 | **Passwordless sign-in** | **Implemented and wired.** Real delivery not activated | `httpapi.requestOtp` → `app.RequestOtp` (`identity.go`) → `otpSender.SendOtp` (the one call site) | **Yes** | One DLT-approved OTP template mapped to an MSG91 flow; the flow id; the variable name; a DLT header | As above | ADR-0033; F-1; R1 | **M1 (the critical path)** |
| 3 | **OTP resend** | No resend endpoint and no server cooldown. A resend is a new request, limited to 3 per number per 15 minutes. Android's "Send a new code" honours `Retry-After` | `POST /v1/otp/request` again | Covered by #2 | None extra: the same flow | — | ADR-0033 Q4 (invalidate earlier codes) and Q5 (cooldown): optional for M1 | M1 (as #2) |
| 4 | **Account-deletion code** (web deletion) | **Implemented, not wired.** Every call answers 503. **The tester allowlist applies** (this change). A source scan fails if production wires it | `POST /v1/account/deletion/code`, `/confirm` → `web_erasure.go` | Later | A **second** DLT template and flow (deletion text). There is no variable for it yet | Its own DLT template; the E10 public page | **O13**, E2, E10 | **M2** |
| 5 | **Phone-number change** | Documented only (domain `ChangeMsisdn`, UX A51n). No route | — | Later | Would reuse an OTP template | — | No ID; ADR-0032 D32.5 (Proposed) | Unassigned |
| 6 | **Security alerts** (new device, SIM swap) | Documented only. **The channel is FCM; SMS is rejected** ("the attacker now controls" the number) | — | Not by SMS | None | — | O5, E7 | Later (C5) |
| 7 | **Transactional notifications** | **Absent by design:** six FCM channels, "no seventh"; notification text exists only on the device | — | No | None | — | — | — |
| 8 | **Other** | The business member invitation SMS (documented only, M3 business scope) and a voice-call OTP fallback (documented only). Nothing for family alerts, scam warnings or call summaries | — | Not for M1 | A separate DLT template each | Each needs its own consent basis | — | M3 or later |

**For M1, MSG91 needs exactly one template:** the sign-in code. Use cases 1–3 share it.

## 3. MSG91's API, verified from MSG91's documentation (2026-11-13)

**How this was verified:** docs.msg91.com renders each page in the browser, so the facts below were read from the page source (the page's embedded API data) and from msg91.com/help articles. The URLs are given so they can be re-checked. **None of this has been exercised against MSG91.**

### 3.1 Send SMS (the "Flow" API): what the adapter calls
- **Source:** https://docs.msg91.com/sms/send-sms
- **Request:** `POST https://control.msg91.com/api/v5/flow`
  - Headers: `authkey: <key>`, `content-type: application/json`, `accept: application/json`.
- **Body:** `template_id`, `short_url` ("1 (On) or 0 (Off)"), the optional `short_url_expiry`, `realTimeResponse` ("1 (Optional)"), the optional `schtime`, and `recipients`.
  - Each recipient is `{"mobiles": "919XXXXXXXXX", "<VAR>": "<value>"}`.
  - `mobiles`: "Keep number in international format (with country code)".
  - Variables: "Variable is case sensitive which means in your template if your defined variable as ##var1## in API as well you should pass it as var1".
- **What the adapter sends:**
  - `{"template_id": <flow id>, "short_url": "0", "realTimeResponse": "1", "recipients": [{"mobiles": "91" + <10 digits>, <code variable>: <code>}]}`.
  - A test pins exactly these fields (`msg91_test.go`).
- **Documented success:** `200 {"type":"success","message":"5e1e93cad6fc054d8e759a5b"}`, where `message` is the request id.
  - The same family of APIs also answers **200 with `"type":"error"`** (SendOTP's documented samples). The adapter treats anything but `"type":"success"` as refused.
- **`realTimeResponse`:** MSG91 says "This parameter provides a real-time response in the second instance due to caching, addressing most API error codes". The adapter sets it so a refusal is answered at once rather than only by email.
  - Its exact behaviour is to be confirmed in the test transaction.
- **"SUCCESS" means accepted, not delivered:** "If the API is successfully submitted to our server the response is Type: 'SUCCESS' and String: 'REQUEST ID'. For checking the complete reports, you need to check the delivery reports or can use webhooks" (https://msg91.com/help/MSG91/how-to-get-sms-status-in-api).

### 3.2 Delivery evidence: the SMS Logs API and the panel's Logs
- **Source:** https://docs.msg91.com/sms/sms-logs
- **Request:** `POST https://control.msg91.com/api/v5/report/logs/p/sms?startDate=YYYY-MM-DD&endDate=YYYY-MM-DD`, with `authkey` and `accept` headers.
- **Optional parameters:** `requestId` ("Unique request ID of your SMS submission"), `fields` (e.g. `status, deliveryDate, deliveryTime, telNum, requestId, senderId, failureReason, credit, route, DLT_TE_ID, flowID`) and `limit`.
- **Window:** at most 3 days, and `startDate` must be within the last 3 days.
- **Sample row:** `{"status":"Delivered","telNum":"91XXXXXXXXXX","requestDate":…,"deliveryDate":…,"deliveryTime":…}`.
- **Statuses** (https://msg91.com/help/MSG91/what-is-the-meaning-of-different-status-in-the-delivery-reports):
  - **Submitted:** handed to the operator.
  - **Sent/Pending:** "yet to be delivered". An out-of-network number shows Pending for up to 4 hours.
  - **Delivered:** "received by the user successfully".
  - **Failed**, **Blocked number** and **NDNC** (promotional route only).
- **Cost of a message:** the panel's Logs show "the credits deducted and the amount" on hover (https://msg91.com/help/MSG91/wallet-check-balance-deduction-from-delivery-logs).

### 3.3 SendOTP: not used
- **Source:** https://docs.msg91.com/otp/sendotp (`POST https://control.msg91.com/api/v5/otp?template_id=&mobile=`).
- It accepts a caller-supplied `otp` parameter ("OTP you want to send"), but it is MSG91's code-management product: it has its own expiry and its own `Verify OTP` and `Resend OTP` endpoints.
- **ADR-0033 Option A keeps every code decision in identity,** so the Flow API (§3.1), which only delivers, is the one used.

### 3.4 Account and API facts that affect the test
- **A demo account replaces the text.** "If you are sending an SMS from your account and your account status is 'demo,' you will receive the content as: 'You have received this SMS as per your testing…'". KYC (Aadhaar e-KYC or documents) makes the account live (https://msg91.com/help/MSG91/my-account-status-is-in-demo-what-can-i-do-to-make-my-account-live).
  - **So a test from a demo account does not deliver the sign-in code.** It proves acceptance and routing only.
- **API Security (IP whitelisting)** is optional per authkey. With it enabled, requests from other addresses are refused with error **418** (https://msg91.com/help/MSG91/what-do-you-mean-by-api-security). **Recommended:** enable it with only the test machine's address, then the staging egress address.
  - **IPv4 versus IPv6 (found 2026-11-13).** `control.msg91.com` has both IPv4 and IPv6 addresses, and a host with global IPv6 (this development machine) connects over IPv6 from a temporary address. MSG91 then sees an address that is not allowed: the owner's first read-only check got `401` and MSG91 listed the IPv6 address under "Recent API Calls from Non Whitelisted IPs". The same check forced to IPv4 got `200`.
  - **`CS_IDENTITY_OTP_MSG91_IPV4_ONLY=true`** (§4) makes identity's MSG91 connections IPv4 only, so an IPv4-only allowlist works from such a host.
- **Error codes that matter here** (https://msg91.com/help/MSG91/what-are-the-reason-for-error-codes-received-under-the-api-failed):

  | Code | Meaning |
  |---|---|
  | 203 | Invalid sender ID or DLT Entity Id missing |
  | 207 | Invalid authentication key |
  | 211 | DLT Template Id missing |
  | 301 | Insufficient balance |
  | 400 | Template ID missing, incorrect or archived |
  | 401 | Flow not yet approved |
  | 403 | Flow disabled |
  | 418 | IP not whitelisted |

  - **311:** "When the same SMS content is sent to the same number within 10 seconds… The first SMS will be delivered and the second will be rejected. The balance will also be deducted only once". This is MSG91-side duplicate protection.
- **Templates:** "Accounts are template restricted". The DLT Template ID is entered on the MSG91 template (flow), and the variables are named without `##` (https://msg91.com/help/MSG91/map-approved-dlt-template-id-with-respective-flow-id-on-msg91-panel).
  - If DLT verification fails, the flow "will just be sent on international numbers and not on Indian numbers" (https://msg91.com/help/MSG91/how-to-create-flow-id-to-send-sms-via-api).
  - **The adapter refuses every non-`+91` number, so it can never take that path.**
- **No sandbox or test mode was found.** The panel's "Test DLT" option tests a template on one number. It appears to send a real SMS, and whether it is charged is not documented.
- **Not found in the docs:** idempotency keys, numeric rate limits for the API, and the webhook payload. The 429 article says panel rate limits do not apply to API integrations "unless specified otherwise".

## 4. Configuration (exact)

| Variable | Value |
|---|---|
| `CS_IDENTITY_OTP_PROVIDER` | **`msg91`** for M1 (alone: at most one request per code). Accepted values: `none`, `mock` (only with `CS_IDENTITY_ENVIRONMENT=development` set explicitly), `msg91`, `twilio`, `msg91,twilio`, `twilio,msg91`. **Unset:** `none`, or `mock` in explicit development. Blank, padded or unknown values stop startup, and unknown values are not echoed |
| `CS_IDENTITY_OTP_MSG91_AUTH_KEY_FILE` | Absolute path of a mounted secret file holding the authkey. Read at startup; never logged |
| `CS_IDENTITY_OTP_MSG91_SIGNIN_FLOW_ID` | MSG91's template (flow) id for the sign-in template: 1–64 letters or digits |
| `CS_IDENTITY_OTP_MSG91_CODE_VARIABLE` | The template's variable name, exactly as defined (case-sensitive, without `##`): 1–64 letters or digits |
| `CS_IDENTITY_OTP_MSG91_IPV4_ONLY` | **Optional**; `true` or `false` exactly, **unset means `false`** (connections exactly as before). With `true`, every MSG91 connection is **IPv4 only** (`tcp4`): no IPv6 fallback, **no proxy** (a request a proxy would carry is refused, and startup refuses if MSG91's own address would be proxied), and startup fails if the restriction cannot be guaranteed. TLS verification, timeouts, the redirect refusal, India-only numbers, fixed errors and request-id handling are unchanged. Use it when the authkey's IP security allows only an IPv4 address. Refused unless `msg91` is selected; never applies to Twilio or any other client |
| `CS_IDENTITY_OTP_ALLOWED_NUMBERS_FILE` | **Required with `msg91`.** Absolute path; one `+91[6-9]XXXXXXXXX` number per line; `#` comments; at most 64 KiB; no duplicates |
| `CS_IDENTITY_OTP_SIGNIN_HOURLY_CAP` | **Required with `msg91`.** 1–10000 deliveries per clock hour (UTC), across all replicas |

- **Startup refuses:**
  - any Twilio variable while Twilio is not selected;
  - a missing control;
  - a missing, relative or unreadable key file.
- **Not configurable, on purpose:**
  - the API host (`control.msg91.com`, TLS only, no redirects);
  - the route (India `+91` mobiles only; no international fallback);
  - deletion templates (O13).
- *ADR-0033 §8 lists earlier Twilio variable names. The ADR is kept as written; the names above are the code's.*

## 5. Security and abuse-control evidence (local and CI; mocks and stand-ins only)

| Control | Evidence |
|---|---|
| **Allowlist before challenge creation:** an unlisted number spends no challenge, allowance or budget, and is never logged | `internal/app/signin_controls_test.go`; HTTP byte-for-byte refusal in `internal/httpapi/signin_controls_test.go` |
| **The allowlist also blocks deletion codes** (mutation-checked: removing it fails the test) | `internal/app/web_erasure_allowlist_test.go` |
| **The global hourly cap across instances:** two runtimes on one PostgreSQL deliver exactly the cap; 60 concurrent reservations at limit 13 store 13 | `internal/bootstrap/otp_controls_postgres_test.go`, `internal/pgstore/signin_budget_test.go` (CI `identity-postgres`) |
| **Database failure sends nothing** (the cap fails closed) | `internal/app/signin_controls_test.go` |
| **No provider credential in the Android app** | Release permissions have no SMS permission; no provider SDK; only the identity origin (`android/app/release-permissions.txt`, `build.gradle.kts`). ADR-0033: "The Android app never sees a provider credential" |
| **Errors never reveal provider text:** fixed errors; the request id is passed on only when it is shaped like one | `internal/otpdelivery/adapters_test.go` (`TestAdapters_ErrorsAreFixedAndNeverEchoTheMessage`), `msg91_test.go` |
| **No uncontrolled retries or duplicates:** one request per call for 5xx, 429, 401, a 200 error and a timeout; MSG91 alone in the Sender is one request per code | `msg91_test.go` (`TestMSG91_OneRequestPerCallNeverRetried`, `TestMSG91_AloneInTheSenderIsAtMostOneRequestPerCode`) |
| **No other installation path:** `installOtpDelivery` is the only `SetOtpSender` caller; deletion codes are not wired by any production code (mutation-checked) | `internal/bootstrap/otp_controls_test.go`, `deletion_unwired_test.go` |
| **No test can reach a provider:** a dead loopback proxy in `TestMain`, pinned by a test, in `internal/otpdelivery`, `internal/bootstrap`, `internal/playintegrity`, `aiproviders/anthropic`, `aiproviders/elevenlabs`, `speechproviders/deepgram`, `speechproviders/google` | `main_test.go` + `no_network_test.go` in each |

**This is internal assurance (rule 8a), not independent review.** The independent human review is required before M2 (rule 8b).

## 6. India DLT readiness checklist (owner's account; nothing here is verified)

Steps from https://msg91.com/help/MSG91/step-by-step-guide-to-implement-dlt-in-sms. **Each needs the owner; each is not done or not known.**

| # | Item | Who | Status | Notes |
|---|---|---|---|---|
| D1 | MSG91 account, with KYC complete (not "demo") | Owner | **Unknown** | A demo account replaces the message text (§3.4) |
| D2 | API access: an authkey for this integration only, with API Security on (whitelisted IPs) | Owner | **Unknown** | The key goes to a file, never to chat, email or the repository |
| D3 | **DLT Principal Entity registration** on an operator's DLT portal; the PE ID | Owner | **Not done** | About ₹5,900 per portal (earlier estimate, unverified). **A paid step: needs approval** |
| D4 | **Header (Sender ID)** registered on DLT | Owner | **Not done** | It follows the naming review (F26) |
| D5 | **Content template** registered and approved on DLT: the sign-in code, as a service-implicit (OTP) template | Owner | **Not done** | Draft text below; F26 first |
| D6 | **PE–TM chain** declaring MSG91 (and any intermediary) | Owner | **Not done** | https://msg91.com/help/MSG91/pe-tm-chain-binding-on-dlt |
| D7 | On MSG91: the header added and **mapped to the PE ID** | Owner | **Not done** | https://msg91.com/help/MSG91/map-your-dlt-entity-pe-id-with-dlt-approved-header-sender-id |
| D8 | On MSG91: a template (flow) with the **exact DLT text**, the **DLT Template ID** and the approved header; marked default; status verified | Owner | **Not done** | It gives `CS_IDENTITY_OTP_MSG91_SIGNIN_FLOW_ID` and the variable name |
| D9 | The route MSG91 assigns, domestic | Owner | **Unknown** | Read from the logs' `route` field in the test |
| D10 | Balance: startup balance (if granted) or a wallet top-up | Owner | **Unknown** | The smallest public pack is 5,000 SMS for ₹1,250 + 18% GST (₹0.25 each), from https://msg91.com/in/pricing/sms |

**Draft sign-in template**, to be registered only after the naming review (F26). The MSG91 form is shown; DLT portals write the variable as `{#var#}`:

> `##otp## is your Lily AI sign-in code. It expires in 5 minutes. Do not share it with anyone.`

- **5 minutes** is identity's `OtpLifetime`.
- **The variable name** (`otp` here) becomes `CS_IDENTITY_OTP_MSG91_CODE_VARIABLE`.
- **Not settled here:** the brand name, any app hash for Android auto-fill (not implemented), and the header.

## 7. Startup Benefits: eligibility checklist (nothing verified; the owner's account and email are not visible here)

**Sources:**
- https://msg91.com/startups
- https://msg91.com/startup-policy
- https://msg91.com/help/MSG91/what-is-msg91-startup-program-and-how-to-apply-for-it

**Offer:** "Up to $100 worth of balance every month for 6 months", usable for OTP, transactional SMS or email. That is, for example, "25,000 SMS credits per month for a duration of 6 months", refilled "only in Transactional or SendOTP route", from "the date of first SMS credits addition". "MSG91 holds the sole right to qualify the Startups for the offer."

| Criterion (MSG91's words) | Status | What the owner must confirm or do |
|---|---|---|
| "Must be a new account at MSG91" / "Only applicable for New MSG91 users" | **Not verified** | When the account was created; that no earlier MSG91 account exists |
| "A startup needs to be associated with any incubation center or accelerator program" / one of MSG91's "collaborators" | **Not verified** | Which partner, and that the account was created through **that partner's shared link** (the owner's email says the link is required; the public pages say only "associated") |
| The startup form ("Apply Now", msg91.com/startups-form) completed accurately | **Not verified** | That it was submitted and what it said. **Do not create a second account to fix it** |
| "Use a Corporate email ID to signup" (webmail "auto-rejected") | **Not verified** | The account's email domain |
| "Should not have more than one account from the same domain name" | **Not verified** | — |
| "Must have HTTPS certificate" (a website) | **Not verified** | The product website's address |
| **"DLT Registration is mandatory"** | **Not done** (§6 D3–D8) | **A paid external step:** it needs the owner's approval |
| "MSG91 API should be integrated (Offer balance can be availed only after MSG91 API integration)" | **Code ready; not exercised** | One successful test transaction (§8) |
| "Startup has to add MSG91's Startup Badge on their website with tagline – Communication Partner" | **Not verified** | **A publishing action:** owner only |
| Use: "transactional messaging only", no marketing; not "Hosting providers, Developers, Consultancy services…" | Lily AI's sign-in codes are transactional | MSG91 decides |
| **Deadline / benefit cycle** | **Not published on any page read** | It must come from MSG91's email to the owner |
| **"Successful test transaction" and "email reply"** (from the owner's email) | Not on the public pages | Follow the email; §8 and §9 |

**The critical finding:** MSG91 lists **DLT registration as mandatory** for the startup programme, and a **live (KYC'd) account** is needed for the test SMS to carry the real text. Both are owner-side steps, and the DLT step costs money. They come **before** a meaningful test transaction.

## 8. The controlled test transaction: plan (needs the owner's specific approval; not performed)

| Item | Plan |
|---|---|
| **Purpose** | Prove the integration end to end: identity mints a code and hands it to MSG91 through the production adapter and controls, and MSG91 delivers it to one tester's phone. This is the "successful test transaction" MSG91 asks for |
| **API and route** | Send SMS (`POST https://control.msg91.com/api/v5/flow`) via identity's own `POST /v1/otp/request`, not a hand-made request. The route is the one MSG91 assigns the DLT-mapped flow, expected to be transactional or OTP, domestic; read from the logs |
| **Recipient** | **One** owner-approved tester number (`+91…`), owned by the owner or a consenting tester, and the only number on the allowlist. Never a customer number |
| **Template and sender** | The DLT-approved sign-in template (§6 D5, D8) and its approved header. The message carries a real one-time code that expires in 5 minutes and is never used again |
| **Expected cost** | **1 SMS credit** (one segment of up to 160 GSM characters; the draft is about 90). At list price about ₹0.25 + 18% GST if the wallet has balance; from the startup balance if MSG91 has granted it. **Which one applies is unknown.** If the wallet is empty, the smallest pack is ₹1,250 + GST: **a purchase needing approval** |
| **How (one SMS, enforced)** | Identity runs on the test machine against a throwaway PostgreSQL (the CI-pinned image), with:<br>• `CS_IDENTITY_OTP_PROVIDER=msg91`<br>• the authkey in a 0600 file outside the repository, placed by the owner and never displayed<br>• the flow id and variable<br>• **an allowlist of exactly the one tester number**<br>• **`CS_IDENTITY_OTP_SIGNIN_HOURLY_CAP=1`**<br>One `POST /v1/otp/request` for that number. **The cap makes a second SMS in that hour impossible,** even by mistake. **Nothing is retried:** a failure is recorded and reported, not repeated. Before the live run, the same setup runs with the mock to prove it |
| **Security and privacy** | • **The code** is never logged and never shown; the tester confirms receipt without sharing it.<br>• **The number** appears only in the allowlist file and MSG91's logs. Identity logs MSG91's request id, never the number or code.<br>• **The authkey** is read from its file only by identity; it is recommended to whitelist the test machine's IP (API Security) and to rotate the key afterwards.<br>• **The database** and files are deleted afterwards. |
| **Success evidence** | 1. Identity: `202` and the log line `OTP message accepted by the provider … provider_request_id=<id>` (acceptance only).<br>2. MSG91 SMS Logs (panel or API with `requestId=<id>`): `status`, `deliveryDate`, `deliveryTime`, `senderId`, `route`, `DLT_TE_ID`, `credit`, with the number masked in the record.<br>3. The tester's confirmation of receipt: the time and the header shown.<br>4. Balance before and after.<br>**"Accepted" without "Delivered" is reported as exactly that.** |
| **Stop conditions** | Any error code (§3.4), a demo-text SMS, Pending after 4 hours, or Failed: record, report, do not repeat |

**Before approval, the owner provides:**
- the tester number;
- confirmation that the account is live (KYC) and that DLT steps D3–D8 are complete;
- the flow id and variable name;
- the authkey, as a file on the test machine;
- whether the balance is startup or paid;
- the deadline from MSG91's email.

## 9. Draft reply to MSG91 (send only after §8 succeeds, and only with the owner's approval)

> Subject: Re: [the original subject] — API integration and test transaction completed
>
> Dear MSG91 team,
>
> We have completed the MSG91 API integration for Lily AI. Our identity service now delivers sign-in one-time codes through MSG91's Send SMS (Flow) API, using our DLT-registered template [DLT Template ID] and sender ID [header], on the domestic route.
>
> We completed a test transaction on [date, time IST]: request ID [request id], status [Delivered] at [delivery time], to our own test number.
>
> The account [account name/ID] was created through [incubator/partner name]'s startup link, and our startup form was submitted on [date]. Could you please confirm the activation of the Startup Benefits on this account?
>
> Thank you,
> [Name], [role], Lily AI
> [contact]

**Every bracket must be filled from real evidence.** If delivery was not confirmed, the email must not say "Delivered".

## 10. The owner's remaining approvals and inputs

1. **Confirm the account facts** (§7): new account, partner link, startup form, corporate email, one account per domain, website. Also the deadline and the exact wording of MSG91's email.
2. **Complete KYC** on the MSG91 account, if it is still in demo.
3. **Approve the DLT registration** (paid) after the naming review (F26): entity, header, the sign-in template, and the PE–TM chain. Then map them on MSG91 (§6 D3–D8).
4. **Choose the tester number** for the single test, and confirm consent.
5. **Approve the one test transaction** specifically (§8): the number, the purpose, the route, about 1 credit, and whether it uses startup or paid balance. Approve a wallet top-up only if the balance is empty.
6. **Place the authkey** in a file on the test machine (never in chat or email). Optionally enable API Security for that machine's IP.
7. **After success,** approve sending the §9 email.
8. **Separately, and later:** MSG91 activation for staging (R1, with R2), and the startup badge on the website (a publishing action).
9. **Before M2:** the independent human review (rule 8b); O13 for deletion codes.
