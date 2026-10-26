# Account deletion

- **As of:** 2026-10-26.
- **Purpose:** Google Play requires an app that creates accounts to let users delete them **in the app** and request deletion **on the web** (`ANDROID-DATA-INVENTORY.md` §6 Q1).
- **This document covers:**
  - what an account is;
  - what deletion does to each kind of data, as implemented and tested;
  - the in-app endpoint;
  - the web deletion design, which is not implemented;
  - the decisions only the owner can make.

## 1. What "an account" is

There is no account table. A person is a **subscriber id**: `usr_` and 32 hex characters, an HMAC-SHA-256 of the phone number under identity's key (`app.LegacySubscriberID`). Everything identity holds about a person is reached through that id (`services/go/identity/internal/pgstore/migrations/0001`–`0008`). **No other service stores per-subscriber data today** (checked across every `services/go/*` schema), so deletion is identity's alone.

## 2. In-app deletion (implemented, #188)

`POST /v1/account/delete`. The headers and body are the same as refresh: a `DPoP` header, and the body `{"sessionId", "refreshToken"}`.

- **Authentication** is exactly a proof-bound refresh's, with nothing weaker:
  - the session must exist, be unrevoked and be unexpired;
  - the refresh token must be the session's **current** one, compared as a keyed hash in constant time;
  - the session must be **bound to a device key**;
  - the DPoP proof must verify, with `htu` = the deletion URL and `htm` = POST, be fresh and not replayed, and be signed by the **enrolled** key.

  A superseded refresh token is treated as reuse: the session is revoked and nothing is erased (RFC 9700 §4.14.2, as on refresh).
- **Scope:** the erasure covers **every session of the subscriber, on every device**, not only the requesting one.
- **Responses:**
  - **204, no body:** erased.
  - **204 again** for a retry naming the erased session with a fresh proof by the **same** key, so a client that lost the answer can safely retry. Any other key gets 401.
  - **400 `ERR_INVALID_DPOP_PROOF`.**
  - **401 `ERR_INVALID_TOKEN`:** nothing is erased.
  - **503 `ERR_TEMPORARILY_UNAVAILABLE`:** deletion is not configured, or the replay store is unavailable.
  - **500 `ERR_INTERNAL`:** a storage failure; the transaction rolled back and nothing was erased or audited (§2a).
- **Canonical URL:** the refresh canonical URL's origin plus `/v1/account/delete` (`bootstrap.AccountDeletionCanonicalURL`). It needs no new configuration and cannot point at another host. Proofs share refresh's replay store: a proof for one endpoint cannot pass the other, because their `htu` differ.
- **Atomic, and authorized inside the transaction:** one database transaction locks the requesting session, re-checks its current refresh token, revocation and expiry, and only then erases, so either all of it happens or none of it does. A request that lost a race (a concurrent duplicate, rotation or revocation) erases nothing and is answered from the receipt (§2a F2, #191).

### Deletion semantics, by data

| Data | Store | On deletion | Why |
|---|---|---|---|
| Sessions (all devices) | `identity_sessions` | **Deleted** | The account |
| Superseded refresh-token hashes | `identity_refresh_tokens_superseded` | **Deleted** (`ON DELETE CASCADE`) | Belong to the sessions |
| Pending one-time-code challenges | `identity_otp_challenges` | **Deleted** | Keyed to the subscriber |
| Device attestations of the subscriber's keys | `identity_device_attestations` | **Deleted** | Device data. Note: they otherwise have **no expiry** (§5 D6). |
| Guardian delegations involving the subscriber | `identity_guardian_delegations` | **Deleted** (either party) | Nothing writes them today; deleted for completeness |
| **The erasure record** | `identity_erasures` (new, 0008) | **Written and kept: LEGAL_HOLD, append-only** | "The record of an erasure must outlive the erasure" (`docs/domain/10-identity.md` ErasureRequest). ADR-0012 classes erasure audit as LEGAL_HOLD. It holds the subscriber id, when the erasure was requested and completed, and its **exceptions** (what was deliberately left, and why). A CHECK refuses anything but a `usr_` id, so a number cannot be stored. |
| Erasure receipt | `identity_erasure_receipts` (new, 0008) | **Written**, deleted by housekeeping once the session would have expired (≤ 24 h + 1 h) | Answers a retry. It holds a random session id and the key thumbprint. |
| Security audit log | `identity_security_audit` | **Kept: LEGAL_HOLD, append-only**, plus one new event `identity.account.deleted` naming the requesting session | ADR-0010 §10, ADR-0012. It never held a number or subscriber id, only random request and session ids. Once the sessions are deleted, nothing in identity links those ids to the person. |
| OTP rate-limit rows | `identity_otp_rate_state`, `_requests`, `_failures` | **Kept until housekeeping deletes them** (rate window 15 min + 1 h) | Keyed by the number's pseudonym, not the subscriber; they are abuse controls. |
| DPoP replay rows | `identity_dpop_replay` | **Kept until expiry** (minutes) | They block replay, including replay of the deletion proof itself. |
| Access tokens already issued | (not stored) | **NOT revoked: still admitted by edge-api until expiry, at most 15 min** (asserted end to end, §2a) | edge-api does not consult sessions (ADR-0029). The token names only the subscriber id, and edge-api holds no subscriber data (R1). |
| Data on the phone | Android | **Session cleared by the app after a confirmed 204** (#189; also on a 401). **The device key is destroyed after a confirmed 204, and only then** (#192). | Session store and Keystore key. Every unconfirmed outcome keeps the key, because a retry needs it. The next sign-in from the phone enrols a new key (R3). The blocklist and allowlist are local and not account data. |
| Carrier call forwarding | carrier | **Not applicable yet** | P-ID-4 requires clearing forwarding **before** erasure. Forwarding is not implemented; when it is, the erasure must clear it first. |

**Tests (#188):**
- **Application layer (`internal/app/erasure_test.go`):** the scope (both devices erased, another subscriber untouched); each refusal erasing nothing; superseded-token reuse; retry idempotency only for the same key; a replayed proof refused; re-registration still possible.
- **Repository contract (`internal/pgstore`), run on the memory and PostgreSQL repositories:**
  - the shared contract case (`AccountErasure`);
  - a PostgreSQL-only test of attestations, delegations, the append-only record (UPDATE, DELETE and TRUNCATE refused) and housekeeping deleting receipts but never records.
- **HTTP (`internal/httpapi`):** 204, the retry, and every refusal code.

## 2a. Security review of the merged flow (2026-10-26)

**Scope:** identity's endpoint and service (`internal/app/erasure.go`), the PostgreSQL transaction (`internal/pgstore/erasure.go`), migration `0008`, the retry receipt, the Android client, authenticator and screen. **Baseline:** `main` `ff96fea`.

### Defects found and fixed (#191)

| # | Defect | Evidence before the fix | Fix | Test |
|---|---|---|---|---|
| F1 | **OkHttp silently resent a request with the same single-use DPoP proof.** If a reused connection dropped after the request was sent, the identity client's default `retryOnConnectionFailure` resent it. A deletion whose answer was lost would come back as 400 `ERR_INVALID_DPOP_PROOF` (the proof is a replay), and the app would say the device check failed, although the account was gone. The same applied to enrolment (an orphaned session), refresh (a forced sign-out) and OTP requests (a second SMS). | A local probe against MockWebServer (`DISCONNECT_AFTER_REQUEST` on a pooled connection): 3 requests for 2 calls, the second proof sent twice. | `IdentityClient.defaultHttpClient` sets `retryOnConnectionFailure(false)`. Every app client is built from it (identity and edge-api). The failure is reported as `Network`, and the app retries with a fresh proof, which the receipt answers. | `IdentityClientTest` "a request is never resent with the same single-use proof…": 2 requests, proofs 1 and 2, `Network`. |
| F2 | **The authorization was checked outside the erasure transaction.** Two concurrent deletions of one session both passed and both erased: **two** LEGAL_HOLD erasure records (append-only, so they can never be corrected) and two audit events. A deletion also erased after a concurrent refresh had rotated the token it presented. | A mutation test with the in-transaction check removed (which is the behaviour on `ff96fea`): "2 erasure records, want 1", "2 deletion audit events", and a rotated-token deletion returned success. | The repository locks the requesting session (`SELECT … FOR UPDATE`) and re-checks the current token hash, revocation, expiry and subscriber **inside** the erasure transaction. If anything changed, nothing is erased (`ErrSessionConflict`), and the request is answered from the receipt: a concurrent duplicate by the same key gets 204, anything else 401. | `erasure_race_test.go` (concurrent duplicates → 1 record, 1 event, both 204; rotation between read and erase → 401, nothing erased). Contract case `AccountErasure` on memory **and PostgreSQL** (superseded token, another subscriber, unknown session, a duplicate erasure). Mutation-checked on both backends. |
| F3 | **The app claimed "Nothing was deleted" when it could not know.** After an attempt whose answer was lost, a later refusal (503, a proof refusal, rate limiting, or 401 once the receipt has expired) says nothing about the earlier attempt. | Copy in `AccountDeletionCopy.message`. | Each message now says the deletion is **not confirmed**, and the session-ended message says the phone cannot confirm whether the account was deleted. | `AccountDeletionViewModelTest` "no failure message claims nothing was deleted". |

### Defects found and fixed (#192)

| # | Defect | Evidence before the fix | Fix | Test |
|---|---|---|---|---|
| F4 | **A sign-in in flight could outlive the erasure** (was R5). Sign-in consumes its code, then (after the Play Integrity check, a network call) saves the session, in separate steps. An erasure committing in between deleted the subscriber's sessions, and the sign-in then created a **new session for the erased account**. | A deterministic app test runs a real deletion exactly between consumption and save: without the fix the sign-in returns a session (mutation-verified). | `UserSession.ErasureFence` (a write precondition, never stored) is set to when validation began. `SaveSession` refuses (`ErrSubscriberErased`, answered as a spent code, 401 `ERR_INVALID_OTP`) if an erasure of that subscriber was requested at or after it. On PostgreSQL, the erasure transaction and a fenced save **serialise on a per-subscriber advisory lock** (`pg_advisory_xact_lock(hashtextextended('identity_erasure:'+id))`), so neither can miss the other. A code requested after the erasure still signs in (D1 unchanged). | `TestDeleteAccount_ASignInInFlightCannotOutliveTheErasure`; PostgreSQL `…ASignInWaitsForAnErasureInProgressAndIsRefused` and `…AnErasureWaitsForASignInInProgressAndDeletesItsSession` (each forces the interleaving by holding the lock and waiting until `pg_stat_activity` shows the call blocked on it; removing either side's lock fails them). |
| F5 | **The phone kept its device key after a confirmed deletion** (was R3). A re-registration from the same phone enrolled the same key thumbprint, which the retry receipt holds for ≤ 25 h, linking the new account to the erased one; and the access token issued before the deletion stayed presentable from the app for up to 15 min. | `KeystoreProofKey.loadOrCreate` had no delete; the app's `DeviceKey` was a `lazy` that could not be reset. | `core:security` `DeviceProofKey` (with `KeystoreProofKeys`) can `discard()` the key. `IdentityAuthenticator` calls `onAccountDeleted` **only on a confirmed 204**, after clearing the session, under its lock; the app wires it to `discard`. No other outcome (no answer, 503, 400, 500, 401) destroys the key, so a retry is always signed by the enrolled key. | `DeviceKeyAfterDeletionTest` (core:auth: every unconfirmed outcome keeps the key and the retry is signed by it; the 204 destroys it; 401 keeps it; re-registration enrols a new key; mutation-verified). `DeviceProofKeyTest` (core:security). **On the Android 15 emulator, real Keystore:** `DeviceKeyKeystoreTest`: after discard the old key cannot sign, and the next key differs. |

### Verified behaviour (with the test that shows it)

| Property | Result | Evidence |
|---|---|---|
| Unauthorized deletion: another key, no proof, wrong `htu`, a wrong or superseded refresh token, an unbound session | Refused, nothing erased; a superseded token revokes the session (reuse) | `TestDeleteAccount_RefusalsEraseNothing` (9 cases), `…SupersededRefreshTokenIsReuse`, `TestHTTP_AccountDeletionRefusals` |
| Invalid signature or replayed proof | 400, nothing erased | `…RepeatIsIdempotentForTheSameKeyOnly` (replayed proof), HTTP refusals |
| Retry after a lost answer, same key | 204 from the receipt, no second record or audit event | `…RepeatIsIdempotentForTheSameKeyOnly`; live Android client against the real binary (`IdentityLiveContractTest.accountDeletion`, CI) |
| Retry signed by another device | 401 | Same tests |
| Atomicity: a failure mid-transaction | **Rolled back**: the last statement (the receipt) failing its CHECK leaves sessions, superseded hashes, challenges, attestations, delegations, records and receipts exactly as they were | `TestErasure_Postgres_RollbackAttestationsDelegationsRecordAndHousekeeping` (real PostgreSQL) |
| A storage failure | The error is returned (HTTP 500 `ERR_INTERNAL`), nothing erased, nothing audited; a later attempt succeeds | `TestDeleteAccount_AStorageFailureErasesAndAuditsNothing` |
| Erasure across devices and related data | Every session of the subscriber, superseded hashes, challenges, attestations of current keys, delegations either side; nobody else's | `…ErasesTheSubscriberEverywhereAndNobodyElse`, contract and PostgreSQL tests |
| **Access token already issued** | **Still admitted by edge-api until it expires. NOT revoked.** The lifetime is at most 900 s. | `TestE2E_IdentityTokenAdmittedByEdgeThroughTheRealLifecycle` step 6c: the real identity binary on PostgreSQL deletes (204), refresh of the session is refused (401), and edge-api's `GET /v1/me` with the old token and a fresh proof still answers 200; `exp − iat ≤ 900`. |
| Android: cancel, ambiguous failure, retry, confirmed success | Cancel sends nothing; no answer keeps the session and says "not confirmed"; the retry presents the same token with a fresh proof; success only on 204 | `AccountDeletionViewModelTest`, `AccountDeletionUiTest`, `IdentityAuthenticatorTest` (#189), plus F1 and F3 |
| Logs | The deletion path logs only the error code and path | `httpapi.go` (`identity request failed`: `code`, `path`) |

### Residual risks

| # | Risk | Why it remains | Bound and evidence | What would close it |
|---|---|---|---|---|
| R1 | **An access token issued before deletion is not revoked by the server** | edge-api verifies tokens locally, without calling identity or a shared session store: ADR-0010 §9 and §13 (**Accepted**), ADR-0029 D11.2. Server-side revocation needs an architecture decision (D9). | ≤ 15 min (asserted). **Mitigated for the app (#192):** the token is DPoP-bound, and the app destroys the key after a confirmed deletion, so it cannot present the token again; an edge-api test shows the old token with the phone's new key is refused (401). It is **not** closed against a modified client that kept a copy of the key, which Keystore makes non-exportable. edge-api holds no subscriber data (`GET /v1/me` returns the subject and key thumbprint). | D9 |
| R2 | **The erasure record and the deletion audit event are joinable** | Both are written with the same instant. The record holds the subscriber id; the event holds the requesting session id, which also names that session's refresh-reuse audit events, if any. Removing or coarsening either would change what LEGAL_HOLD audit contains, which is D2/D3, not an engineering change. | By design: the record *is* the erasure's audit (domain `ErasureRequest`, ADR-0012). The subscriber id is a keyed HMAC of the number: linking it to a person needs identity's key **and** the number. With F5, the retry receipt's key thumbprint no longer matches any later enrolment. | D2 and D3 (privacy/legal review). |
| R3 | ~~A re-registration from the same phone is linkable to the erased account~~ | **Fixed by F5 (#192).** A re-registration with the same number still gets the **same** subscriber id while ids are derived from the number (D1). | — | D1 for the shared subscriber id |
| R4 | **Attestations of devices whose sessions were already housekept escape erasure** | Erasure finds devices through the subscriber's current sessions. Attestations have no expiry and no subscriber column. | **Latent:** attestations are written only by the Play Integrity verifier, which nothing wires (`SetIntegrityVerifier` has no caller), so none exist today. The verifier writes the key-derived device id, the time and a certified flag (no subscriber id or number). With F5, a discarded key's `dev_` id never recurs, so an orphan cannot be joined to a later enrolment. | D6 (a retention tied to the sessions using the device) **before** the verifier is wired (E1). |
| R5 | ~~A sign-in in flight during the erasure can create a session afterwards~~ | **Fixed by F4 (#192).** | — | — |
| R6 | **Carrier forwarding is not cleared before erasure (P-ID-4)** | Forwarding is not implemented. | Not applicable today. | Must be implemented with forwarding provisioning. |

## 3. Web deletion: server flow implemented and DORMANT (#193); page NOT built; nothing deployed

Play requires a public web link through which a user can request deletion **without the app**. Proving control of a phone number needs **SMS**, and no OTP provider exists (`otpdelivery.go`: "No SMS provider is implemented in this repository").

### 3.0 Status (#193)

**Implemented, in identity, and dormant:**
- `POST /v1/account/deletion/code` and `/confirm` (`internal/app/web_erasure.go`, `internal/httpapi`), as specified in §3.1.
- Migration `0009`:
  - the challenge `purpose`;
  - `identity_deletion_code_receipts`;
  - `identity_deletion_code_budget`;
  - housekeeping of both tables.
- Sign-in and deletion now share **one** code check (`checkCode`).
- A deletion code cannot sign in, and a sign-in code cannot delete.

**Dormant:**
- Deletion codes are issued only with a `DeletionCodeSender` **and** an hourly budget (`SetDeletionCodeBudget`). Production configures **neither**: no provider exists, and production configures no OTP sender at all.
- Every request therefore answers 503 `ERR_OTP_DELIVERY_UNAVAILABLE`, for every number alike.
- The only senders are test fakes:
  - the recording senders in the unit tests;
  - the end-to-end harness's stdout sender (`E2E-DELETION-CODE`).

  **None is a production integration.**
- Enabling it needs O13 (D5), E2 (a provider implementing `SendDeletionCode` with a registered DLT deletion template), and a budget chosen by the owner or SRE.

**Tests (local, all executed):**
- **Application, `web_erasure_test.go`** (10 tests):
  - unavailable without a sender or budget;
  - erasure on every device;
  - a number without an account looks the same;
  - purpose binding both ways;
  - wrong codes, attempt exhaustion and expiry;
  - the number's request limit and lockout shared with sign-in;
  - the hourly budget;
  - retry after a lost answer;
  - concurrent confirms erasing once;
  - an undelivered code withdrawn.
- **Repository, on memory and PostgreSQL:**
  - contract case `WebAccountErasure`;
  - existing challenges default to sign-in, and the purpose CHECK holds;
  - concurrent confirms consume once (forced interleaving);
  - housekeeping;
  - the migration chain pins 0009's scope.
- **HTTP, `account_deletion_web_test.go`:**
  - 503 unconfigured;
  - same-shape 202 with and without an account, and only the account holder gets a code;
  - 204, the retry, and the refusals;
  - 429 shared with sign-in, and 503 once the budget is spent.
- **End to end:** edge-api's lifecycle test, step 6d, runs the real identity binary on PostgreSQL:
  - a second number enrols;
  - deletion codes are requested for it and for the already-erased number, and the answers have the same shape;
  - exactly one code arrives;
  - it fails against the other request;
  - confirm → 204, retry → 204, refresh → 401;
  - no code reaches the number without an account.

**The page is NOT built: a concrete blocker, not an omission.**
- The repository has **no web stack and no static hosting**.
- `package.json` states Node exists only for tooling, and that `web/` is reserved for the admin console "in a later phase". The frontend stack is an owner decision (readiness §4, §7).
- `infra/` has no ingress or static-asset hosting.
- Building the page now would mean choosing that stack and its hosting. The page's specification (§3.2: steps, states, wording, accessibility, data minimization) is complete, and the API it calls is implemented and tested.
- **Minimum next step after E10 and the stack decision:** one static HTML page with no framework and no third-party script. It is served same-origin with identity (§3.3), and makes two `fetch` calls with the states of §3.2. A browser test runs against the local identity with the fake sender (§3.4).

### 3.1 Server API (identity; implemented in #193 as specified here)

| Step | Endpoint | Behaviour |
|---|---|---|
| 1 | `POST /v1/account/deletion/code` `{"msisdn"}` → `202 {"requestId","expiresInSeconds"}` | **The same response whether or not an account exists** (no enumeration). A code is **sent only if the number's subscriber id has a session** (an account with nothing stored has nothing to delete). This saves SMS spend and makes the endpoint worthless for SMS pumping to arbitrary numbers. The send runs **after** the response is decided, so timing does not reveal the difference. The number's **existing** OTP rate limit and lockout apply, **shared** with sign-in (same pseudonym key), so deletion cannot be used to bypass sign-in limits. With no sender configured at all, the answer is 503 `ERR_OTP_DELIVERY_UNAVAILABLE` for every number (configuration, not per number, so no leak). |
| 2 | `POST /v1/account/deletion/confirm` `{"requestId","code"}` → `204` | The code is checked exactly as at sign-in: the attempt limit, the shared lockout and a constant-time hash comparison. The challenge must have **purpose `account_deletion`**: a sign-in code cannot delete an account, and a deletion code cannot sign in. On success, the **same `EraseSubscriber` transaction** runs for the challenge's subscriber id, every session on every device. The erasure record names the request, and the audit event `identity.account.deleted` carries the random `requestId`. Wrong or unknown codes get 401 `ERR_INVALID_OTP`, the same as a request that never existed. |
| 2′ | A retry of step 2 after a lost answer | The challenge is erased with the account, so the erasure writes a **receipt keyed by `requestId`** (the code hash; it expires with the challenge). The same `requestId` and code then get 204 again; anything else 401. |

**Storage (migration `0009`, forward-only like `0001`–`0008`):**
- `identity_otp_challenges.purpose TEXT NOT NULL DEFAULT 'sign_in' CHECK (purpose IN ('sign_in','account_deletion'))`. Existing rows stay sign-in.
- A receipt table like `identity_erasure_receipts`, keyed by `req_…`, housekept at expiry.
- `EraseSubscriber` gains a variant whose in-transaction check is "the challenge is still unconsumed" instead of the requesting session (the F2 pattern).

**Interfaces:** `OtpSender.SendOtp(ctx, msisdn, code)` is unchanged. DLT needs a **separate registered template** for the deletion message ("Your Aegis account deletion code is {#var#}. If you did not ask to delete your account, ignore this message."), so the sender takes a message **purpose** selecting the template. The fake sender used by tests and the E2E harness (`stdoutOtpSender`) implements it. **No provider API is assumed.**

### 3.2 The page

- **Two steps**, no account details shown: (1) the number, then "If an account exists for this number, we have sent a code" (never "we sent a code to your account"); (2) the code, then a final confirmation stating what is deleted and what is kept (the §2 table, in plain words), then **Delete permanently**.
- **States:**
  - sending;
  - code sent (neutral wording);
  - wrong code, with the attempts left for **this** code;
  - locked or rate limited, using `Retry-After`;
  - service unavailable;
  - deleted, shown only on 204;
  - not confirmed (no answer; "try again; if it already went through, trying again confirms it", as in the app).
- **Didn't get a code:** after the rate limit allows, request another, plus a support contact, needed for people who have lost the number (D5).
- **Data minimization:**
  - no cookies, no analytics, no third-party scripts;
  - nothing is stored in the browser;
  - the number is sent once and pseudonymised at identity's boundary, as today;
  - server logs keep only the code and path (as `httpapi` does now).
- **Accessibility (WCAG 2.2 AA):**
  - visible labels;
  - `autocomplete="tel"` and `autocomplete="one-time-code"`, `inputmode="numeric"`;
  - errors tied to their fields (`aria-describedby`) and announced (a live region);
  - focus moves to each new step's heading;
  - no client-side time limit; when a code expires, the page offers a new one;
  - works at 400 % zoom and with a screen reader;
  - the text is plain language, in the app's languages.

### 3.3 Hosting, availability and links

- **URL:** a stable HTTPS URL on a domain the owner controls, for example `https://<owner domain>/delete-account`. It goes into the Play Console "Delete account URL" field, into the privacy policy, and into the app's deletion screen as the alternative.
- **Recommended: same origin.** Serve the static page under the **same public host as identity's API**, by path routing at the load balancer to a static bucket. Then no CORS is needed, there is one certificate, and the page is up exactly when deletion can work.
- **Alternative: a separate static host.** It needs a CORS allowlist on identity for that **one** origin, on the two endpoints only (`POST`, `Content-Type`), with no credentials.
- **TLS:** HTTPS only, HSTS, and a strict CSP (`default-src 'self'`; script and connect to self or identity only).
- **Monitoring:**
  - an uptime check on the page and on `/v1/account/deletion/code` (expecting 202 or 503, never 5xx otherwise);
  - per-endpoint outcome metrics (the existing `endpointFor` and outcome labels);
  - alerts on the 5xx rate, on deletion-code sends against a **global SMS budget** (a circuit breaker that stops sends above an hourly ceiling, so SMS pumping cannot run up cost), and on the confirm-failure ratio.
- **Abuse controls available without new providers:**
  - the per-number limits and lockout (existing);
  - sends only to numbers with an account (3.1);
  - `+91` only (DLT);
  - the global SMS budget.

  Per-source-address limits **are not possible yet**: forwarding headers are not trusted (E6). A bot challenge (CAPTCHA) is an **optional external provider** (cost unverified), for the owner to decide.

### 3.4 Tests (all local; no provider)

- **Application:**
  - purpose binding (a sign-in code cannot delete; a deletion code cannot enrol);
  - an identical response for numbers with and without an account, and **no send** for the latter (fake sender call count);
  - the shared lockout;
  - erasure across devices;
  - receipt retry; a wrong code after success refused;
  - concurrent confirms produce one erasure (the F2 pattern).
- **Repository:** the `0009` migration chain; the purpose CHECK; receipt housekeeping; the contract case on memory and PostgreSQL.
- **HTTP:** byte-identical 202 bodies; status codes; no CORS headers unless configured; problem codes.
- **End to end:**
  - the existing process harness (`TestE2EIdentityProcess`, stdout sender) runs code → confirm → the app's session refresh refused → edge-api residual window;
  - a headless-browser test of the page against that local identity. This adds a Node test dependency (Playwright), a tooling decision.

### 3.5 What blocks it

- **For the page:** the frontend stack (owner) and hosting (E10); §3.0.
- **For enabling the implemented server flow:** O13 (D5) and an hourly budget value (owner or SRE).
- An SMS/OTP provider, plus India DLT registration **including the deletion template** (E2), with an adapter implementing `DeletionCodeSender`.
- A domain, hosting and TLS for the page (new E10; cost unverified).
- The owner's choice of web approach (D5) and of a bot challenge.
- The privacy policy URL.
- Deployment of identity with `0008` and `0009`.

No account was created, nothing was purchased, configured or published.

## 4. Android (implemented, #189)

**Entry point:** the Settings tab has an **Account** section (a heading) with a **Delete account** button. It is reachable whether or not the phone is signed in.

**The screen** (`feature/settings` `AccountDeletionScreen`, `AccountDeletionViewModel`):

| State | What the user sees |
|---|---|
| Checking | "Checking this phone's sign-in…", with a progress indicator |
| No identity server in this build | Accounts are not available, so there is nothing to delete. No delete button. |
| Not signed in | There is no account to delete from this phone; sign in with its number first. No delete button. |
| Ready | What deletion does (server account on every phone; sessions, codes and device records; this phone signed out) and **what stays** (the erasure record with a coded id, the security log, the local blocklist and allowlist, short-lived security records). "This cannot be undone." A **Delete my account** button. |
| Confirming | A dialog: "Delete your account permanently?", with **Delete permanently** and **Cancel**. Nothing is sent before this. |
| Deleting | "Deleting your account…" (a polite live region), with a progress indicator. A double tap sends one request. |
| Deleted | **Only after the server's 204:** "Your account has been deleted". **Done** returns to the Welcome screen and clears the back stack. |

**Failures never show success:**
- **No answer, or an answer outside the contract:** "We could not confirm the deletion… If it already went through, trying again confirms it." The session is kept, so the retry presents the same credentials and the server's receipt answers 204.
- **Unavailable, rate limited, or a device-proof refusal:** says that nothing was deleted, and keeps the session.
- **401, the session already ended:** the phone is signed out and told to sign in again to delete.

**Client and session handling:**
- `IdentityClient.deleteAccount` POSTs `{sessionId, refreshToken}` with a DPoP proof for `origin + /v1/account/delete`. It sends no integrity token, so no Play Integrity quota is spent. Only 204 is success; a redirect is not followed.
- `IdentityAuthenticator.deleteAccount` runs under the same lock as refresh, so it always presents the current refresh token. It clears the stored session only on 204, or when the server says the session has ended.
- The device key is destroyed after a confirmed deletion, and only then (F5, #192). Every unconfirmed outcome keeps it, because a retry must be signed by it.

**Tests:**
- `IdentityClientTest`: the request, the proof URL, no integrity token, 204 only, each refusal, no redirect, input refused locally, network failure, and 204 not accepted on other endpoints.
- `IdentityAuthenticatorTest`: clears only after 204; every failure keeps the session and the retry presents the same token; a 401 clears the session; no session sends nothing; deletion is serialised with refresh.
- `AccountDeletionViewModelTest`: confirmation, cancel, double tap, network failure then retry, each refusal, session ended, and no session or no server.
- `AccountDeletionUiTest` (Robolectric): the Settings entry (a heading and a clickable button), the consequences, cancel deletes nothing, loading then success then one navigation, network failure shown, no session offers no delete.
- `IdentityLiveContractTest.accountDeletion`: the real client against the **real identity binary and PostgreSQL** in CI (`android-identity-contract.yml`):
  - another key is refused and erases nothing;
  - the enrolled key deletes;
  - the retry is confirmed;
  - another key's retry is refused;
  - the session is gone.

**Not run:** an emulator or device run of the screen against a live identity server. No identity server is deployed, and debug builds have no identity URL, so on a device the screen shows "Accounts are not available".

## 5. Decision register (recommendations, NOT approvals)

Every row is **open**. A recommendation is engineering's reading of the existing specifications and code; it is **not** an approval, and the current behaviour stays until the named approver decides. The tracker rows are O9–O17 in `EXTERNAL-DEPENDENCIES.md`. No ratified document (ADR-0010, ADR-0012, the domain model) is changed here.

**Kinds:**
- **Owner approval:** a product or security decision only the owner can make.
- **Privacy/legal review:** the owner decides **on qualified advice** (E9); engineering does not change retention without it.
- **Governance:** it changes an Accepted ADR or the domain model, so it needs an amendment through that document's process.
- External dependencies and their costs (all **unverified**) are in §6, not here.

| # | Decision | Kind | Status | Recommended | Approver |
|---|---|---|---|---|---|
| D1 | May an erased number sign up again? | Owner approval + privacy review; **governance** (domain model INV-ID-7) | Open: **the code conflicts with the domain text** | Allow (current); amend INV-ID-7 to tombstone the *subscriber id*, which needs random ids (ADR-0030 D30.3) | Owner + privacy review (E9); domain-model amendment |
| D2 | What the erasure record keeps, and for how long | **Privacy/legal review** | Open: **privacy/legal review** | Keep the content (it is what the domain specifies); set an end date on legal advice | Owner on DPDP legal advice (E9) |
| D3 | Audit-log retention in practice | **Privacy/legal review**; **governance** (ADR-0012) if a period is set | Open: **privacy/legal review** | Keep ADR-0012's LEGAL_HOLD; ask legal for a concrete period | Owner on legal advice (E9); an ADR-0012 amendment if it changes |
| D4 | A fresh SMS code before deletion | Owner approval | Open | No SMS step-up. Optionally, the phone's own screen-lock check before the final confirmation. | Owner (security/product) |
| D5 | Web deletion approach | Owner approval; external (E2, E10) | Open. The self-service server side is **built and dormant** (#193); building it approves nothing. | Self-service OTP (§3), **plus** a support channel for a lost number | Owner; depends on E2 and E10 |
| D6 | Device-attestation retention | Owner approval (retention) | Open (**latent**: none are written today) | Delete an attestation once no session uses its key, plus a margin | Owner (retention) |
| D7 | Immediate deletion or a grace period | Owner approval | Open | Immediate (current), while identity holds no user content | Owner (product) |
| D8 | UX spec A53d's "type the last four digits" confirmation | Owner approval (UX spec) | Open: **the spec cannot be implemented as written** | Keep the explicit dialog; amend A53d, or decide to keep the last four digits on the phone | Owner (UX spec) |
| D9 | Revoke access tokens at edge-api after deletion? | **Governance** (ADR-0010 §9/§13 Accepted; ADR-0029 D11.2) + owner approval | Open | Not now: keep stateless verification; the app-side key destruction (F5) already makes the token unusable by the app. Add an erased-subject check **when a protected route first returns or changes personal data** | Owner, through an ADR-0010 amendment |

### D1 · Re-registration after deletion
- **Evidence:**
  - The domain model says the subscriber lifecycle "Ended by `ErasureRequest` completion, which tombstones the identifier permanently", that "An erased Subscriber cannot be reactivated", and **INV-ID-7**: "An erased Subscriber's identifiers are tombstoned and never reused" (`docs/domain/10-identity.md`).
  - The subscriber id is **deterministic**: `usr_` + HMAC(number) (`app.LegacySubscriberID`).
  - Signing in again re-creates the **same** id with no data (`TestDeleteAccount_TheNumberCanSignUpAgain`).
- **The conflict:** tombstoning a deterministic id means **refusing the number forever**, including for the next person to hold it after the operator recycles it.
- **Options:**
  - *(a) Block:* matches INV-ID-7 literally, but locks out recycled numbers. It needs a tombstone lookup at sign-in, keyed by the HMAC, which itself keeps the number linkable forever.
  - *(b) Allow (current):* recycled numbers work, but the LEGAL_HOLD record then names a live account's id (R3), and INV-ID-7 is not met.
  - *(c) Random subscriber ids (ADR-0030 D30.3, O2):* the erased id is tombstoned and never reused, while the number can return as a **new** id. This meets INV-ID-7's intent.
- **Recommendation:** (b) now, and (c) as the target. Amend INV-ID-7 to say the erased **SubscriberId** is never reused, and that the MSISDN may return after erasure.
- **Consequences:** under (b), the residuals R3 and R5 remain until (c).
- **Approver:** the owner, with the privacy review. Changing the domain model is the owner's.
- **Next action:** the owner chooses. If (c), it follows O2's migration plan.

### D2 · The erasure record's contents and retention
- **Evidence:**
  - The domain `ErasureRequest` is LEGAL_HOLD, with `subscriberId`, `requestedAt`, `completedAt`, `scopes` and `exceptions`.
  - ADR-0012 §5.3 (Accepted): "Audit logs (auth, access, erasure) · `LEGAL_HOLD` · Fixed".
  - `identity_erasures` holds the subscriber id, the two times and the exceptions, is append-only, and never ends.
- **Privacy:** the id is a keyed HMAC. It is linkable to a person only with identity's key **and** the number, or by joining with the deletion audit event (R2).
- **Options:**
  - keep (current);
  - store a separate one-way erasure key instead of the subscriber id (less linkable, but proving "this person's erasure" then needs the number at proof time);
  - add an end date.
- **Recommendation:** keep the content, since it is what the domain specifies. Ask the DPDP review for a **concrete** retention period (for example, the limitation period for complaints) and implement it as a scheduled deletion of records older than that.
- **Approver:** the owner, on legal advice (E9). **Flag: privacy/legal review required.**
- **Next action:** add D2 to the privacy-review brief.

### D3 · Audit-log retention
- **Evidence:**
  - ADR-0012 (Accepted) sets audit logs to LEGAL_HOLD with no period.
  - `identity_security_audit` is append-only (`0005`) and holds an event type, times, and the random `req_`/`sess_` ids. It holds no number and no subscriber id.
- **Recommendation:** keep it as ADR-0012 says, and ask legal for a concrete period, so that "LEGAL_HOLD" is not read as "forever".
- **Consequence:** a period would need an **ADR-0012 amendment through its governance**, not an edit here.
- **Approver:** the owner, on legal advice. **Flag: privacy/legal review required.**
- **Next action:** add D3 to the privacy-review brief.

### D4 · Step-up verification before deletion
- **Evidence:**
  - Deletion needs the current refresh token **and** a proof by a non-exportable device key (INV-ID-3), so only the enrolled app on that phone can delete.
  - The SMS for a step-up code goes to the SIM, which is **usually in the same phone**: an attacker holding the unlocked phone receives it too.
  - Requiring SMS would block deletion whenever SMS is down. No provider exists today.
- **Recommendation:**
  - **No SMS step-up.** It adds little against the realistic threat (an unlocked, stolen phone) and adds a dependency.
  - If more friction is wanted, require the phone's **own screen-lock check** (device PIN or biometric through `BiometricPrompt` with device credential) before the final confirmation. It works offline, and it stops someone holding an unlocked phone.
- **Consequence:** a thief who also knows the PIN can delete. The harm is bounded today, because identity holds no user content (D7).
- **Approver:** the owner (security/product).
- **Next action:** the owner decides. A screen-lock check is a small Android change.

### D5 · Web deletion approach
- **Option (a), self-service OTP (§3):**
  - **Abuse:** per-number limits, shared lockout, sends only to existing accounts, and an SMS budget breaker; no per-IP limits until E6.
  - **Accessibility:** a two-step form.
  - **Operating burden:** low (no human in the loop).
  - **Provider dependency:** SMS and DLT, including a new template.
- **Option (b), a support-managed request** (email or form, an operator verifies and erases):
  - **Abuse:** social engineering of the operator.
  - **Verification:** an operator cannot verify control of the number without SMS either. A callback works, but needs telephony and a script.
  - **Operating burden:** a console tool with an audit trail, a response SLA, and staff.
  - **Provider dependency:** none, besides support tooling.
- **Recommendation:** (a), **plus** (b) only as the fallback for people who no longer hold the number. They cannot prove control by SMS anyway, so support handles them case by case with the audit trail the console spec requires.
- **Approver:** the owner.
- **Depends on:** E2 (SMS/DLT) and E10 (hosting).

### D6 · Device-attestation retention
- **Evidence:**
  - `identity_device_attestations` has no expiry.
  - It is written only by the Play Integrity verifier, which is not wired (R4), so no rows are written today.
  - Erasure deletes the attestations of the subscriber's **current** keys.
- **Recommendation:**
  - Housekeeping deletes an attestation once **no session references its key** and it is older than the session lifetime plus a margin (24 h + 1 h, as for sessions).
  - Its purpose is integrity at issuance and refresh; once no session uses the key, it serves none.
  - This also closes R4.
- **Approver:** the owner (retention policy).
- **Next action:** a small housekeeping step and its test, once approved. **Before** the verifier is wired (E1).

### D7 · Immediate deletion or a grace period
- **Evidence:**
  - The domain `ErasureRequest` has no grace state (REQUESTED → IN_PROGRESS → COMPLETED).
  - The UX spec A53d asks for high friction **before** erasure, not an undo after it.
  - Identity holds no user content today (no calls, transcripts or settings server-side; `ANDROID-DATA-INVENTORY.md`). A wrongful deletion therefore loses only sessions, and the person can sign in again (D1 (b)).
- **Recommendation:** **immediate** (current). Revisit when server-side user content exists. A grace period then needs a pending state, an undo that is itself authenticated, a scheduled erasure, and the user told the exact date. Users and Play expect "delete" to delete.
- **Approver:** the owner (product).

### D8 · UX spec A53d confirmation (raised by this review)
- **Evidence:**
  - `docs/ux/android/07-screens-settings-premium-business.md` A53d: "type the last four digits of the number to confirm".
  - Neither the app nor identity keeps the number. The app holds none after sign-in, and identity holds only its HMAC, which cannot check four digits.
  - The implementation uses an explicit "Delete permanently" dialog.
- **Options:**
  - amend A53d to the explicit dialog, optionally with D4's screen-lock check;
  - keep the last four digits on the phone at sign-in. That is new personal data on the device, for little security value, since the person holding the phone usually knows its number.
- **Recommendation:** amend A53d.
- **Approver:** the owner (UX spec).

### D9 · Access tokens after deletion (raised by #192)
- **Evidence:**
  - Tokens are self-contained, 15 minutes, and verified by edge-api locally with no call to identity (ADR-0029 D11.2; ADR-0010 §9 and §13, **Accepted**: "Stateless verification … requires no shared session store").
  - The end-to-end test shows an erased account's unexpired token is still admitted (with a proof by the original key), and refused with any other key.
  - After F5, the app destroys that key on a confirmed deletion.
  - edge-api's only protected route, `GET /v1/me`, returns the subject and key thumbprint and nothing else.
- **Options:**
  - *(a) Status quo plus F5 (current):* no availability cost. The residual is a client that kept the key, which Keystore makes non-exportable, for ≤ 15 min, against a route with no subscriber data.
  - *(b) An erased-subject check at edge-api:* refuse a token whose `sub` was erased at or after its `iat`. edge-api already writes to PostgreSQL on every request (DPoP replay), so it adds one indexed read and **no new availability dependency**. But it couples edge-api to identity's erasure data, against ADR-0010 §13. A token re-issued after a re-registration within the same second could be refused.
  - *(c) A session or version check per request:* real revocation of any session, but it puts identity's state on every request path. That is the explicit opposite of ADR-0010 §9.
  - *(d) A shorter lifetime (for example 5 min):* it narrows the window for everyone. It triples refresh traffic, and with it the integrity-token requests at their quota (ADR-0031 D31.4). ADR-0029 change.
- **Recommendation:** (a) now. Adopt (b) through an ADR-0010 amendment **before** any protected route returns or changes personal data, because only then does the window expose anything.
- **Approver:** the owner, through ADR governance.

## 6. External setup and credentials still needed

| Resource | Why | Cost | Local alternative |
|---|---|---|---|
| SMS/OTP provider + India DLT | Web deletion (§3), and sign-in itself | Unverified (per message) | Fake `OtpSender` in tests |
| Domain, public static hosting and TLS for the web deletion page (E10) | A Play requirement (§3.3) | Unverified (provider not chosen) | Local identity plus the fake sender (§3.4) |
| India DLT template for the **deletion** message | DLT requires each message template registered | Unverified | Fake sender |
| Optional bot challenge (CAPTCHA) for the public form | Abuse control while per-source limits are impossible (E6) | Unverified; owner's choice (§3.3) | Not needed locally |
| Privacy policy URL | Linked from the page and Play Console | Owner/legal | None |
| Play Console "Delete account URL" | A Play requirement | Included in the Play Console account | None |
| Deployed identity with migration 0008 | The endpoint must be live | Part of the deployment (SRE) | Local PostgreSQL (used for these tests) |

No credential, account, hosting or production resource was created for this work.
