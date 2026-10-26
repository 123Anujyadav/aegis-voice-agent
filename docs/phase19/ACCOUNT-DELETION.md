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
  - **503 `ERR_TEMPORARILY_UNAVAILABLE`:** deletion is not configured, or a dependency failed.
- **Canonical URL:** the refresh canonical URL's origin plus `/v1/account/delete` (`bootstrap.AccountDeletionCanonicalURL`). It needs no new configuration and cannot point at another host. Proofs share refresh's replay store: a proof for one endpoint cannot pass the other, because their `htu` differ.
- **Atomic:** one database transaction, so either all of it happens or none of it does.

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
| Access tokens already issued | (not stored) | **Valid until expiry, at most 15 min** | edge-api does not consult sessions (ADR-0029). The token names only the subscriber id, and edge-api holds no subscriber data. |
| Data on the phone | Android | **Session cleared by the app after a confirmed 204** (#189; also on a 401) | Session store. The device key stays in the Keystore until uninstall; the blocklist and allowlist are local and not account data. |
| Carrier call forwarding | carrier | **Not applicable yet** | P-ID-4 requires clearing forwarding **before** erasure. Forwarding is not implemented; when it is, the erasure must clear it first. |

**Tests (#188):**
- **Application layer (`internal/app/erasure_test.go`):** the scope (both devices erased, another subscriber untouched); each refusal erasing nothing; superseded-token reuse; retry idempotency only for the same key; a replayed proof refused; re-registration still possible.
- **Repository contract (`internal/pgstore`), run on the memory and PostgreSQL repositories:**
  - the shared contract case (`AccountErasure`);
  - a PostgreSQL-only test of attestations, delegations, the append-only record (UPDATE, DELETE and TRUNCATE refused) and housekeeping deleting receipts but never records.
- **HTTP (`internal/httpapi`):** 204, the retry, and every refusal code.

## 3. Web deletion (designed, NOT implemented)

Play requires a public web link through which a user can request deletion **without the app**. Doing that securely means proving control of the phone number, which needs **SMS delivery**, and no OTP provider exists (`ANDROID-DATA-INVENTORY.md` §5). Recommended design, reusing identity's existing OTP:

| Step | API (proposed) | Behaviour |
|---|---|---|
| 1 | `POST /v1/account/deletion/otp` `{"msisdn"}` → `202 {"requestId", "expiresInSeconds"}` | The same rate limits, delivery and challenge storage as sign-in. **Same response whether or not an account exists** (no account enumeration). |
| 2 | `POST /v1/account/deletion/confirm` `{"requestId", "code"}` → `204` | The code is verified exactly as at sign-in. The subscriber id comes from the challenge, then the same `EraseSubscriber` transaction runs. **204 even if no account existed**, so the page cannot be used to probe numbers. Wrong codes count toward the same lockout. |

**Hosting requirements:**
- a static HTTPS page, on a host **independent of the app and the Play listing** (it must work for someone who has uninstalled the app), linked from the Play Console "Delete account URL" field and the privacy policy;
- it calls identity's API cross-origin, which needs a CORS allowlist for that exact origin on the two endpoints, or same-origin routing;
- no cookies and no analytics, and the page stores nothing;
- its text states what is deleted and what survives (§2 table), and links the privacy policy.

**Blocked on:**
- an SMS/OTP provider and India DLT registration;
- a hosting decision and account (provider not chosen; **cost unverified**);
- the owner's choice of web approach (§5 D5).

**Local alternative for development:** identity's `OtpSender` interface with a fake sender, as the tests already use. No public endpoint or page is deployed by this work.

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
- The device key stays in Android Keystore until the app is uninstalled; it identifies no one once the server's records are gone.

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

## 5. Decisions for the owner (not decided here)

| # | Decision | Current behaviour | Options and consequences |
|---|---|---|---|
| D1 | **May an erased phone number sign up again?** The domain says "An erased Subscriber cannot be reactivated" and that erasure "tombstones the identifier permanently". But the id is a deterministic hash of the number, so a tombstone would **block that number forever**, including for its next owner when Indian operators recycle it. | **Allowed:** signing in again creates a fresh account with none of the old data (tested). | *Block:* matches the domain text, but permanently refuses recycled numbers, and needs a tombstone check at sign-in. *Allow (current):* recycled numbers work; the domain text needs amending. A random subscriber id (ADR-0030 D30.3) would remove the conflict. |
| D2 | **What the LEGAL_HOLD erasure record keeps,** and for how long | The subscriber id (keyed hash), timestamps and exceptions, **never deleted** | *Keep the id (current, as the domain specifies):* "was X's account erased?" can be proven, but whoever holds the key and the number can link the record to them. *Keep only a separate one-way erasure hash or no subject:* less linkable, but erasure is harder to prove for a specific person. *Set a fixed end date:* needs a legal basis. **Needs the DPDP review.** |
| D3 | **The audit log's LEGAL_HOLD in practice** | Never deleted (approved ADR-0012) | Confirm the intended duration. The log holds only random ids, so after erasure it is not linkable within identity. |
| D4 | **Step-up re-authentication** before deletion | A device-bound session proof (the strongest credential the app holds); no fresh OTP | *Add an OTP step:* stronger against a stolen, unlocked phone, but needs SMS and blocks deletion if SMS is down. *Keep (current):* works offline from SMS. |
| D5 | **Web deletion approach** | Not implemented (§3) | *Self-service OTP flow (§3, recommended):* immediate and verifiable, but needs SMS and hosting. *A support request processed by an operator:* no SMS needed, but needs an operator tool, a manual identity check and a response SLA. |
| D6 | **Device attestations have no expiry** | Kept until the subscriber deletes their account | Add a retention period (for example, the session lifetime). A separate retention issue, raised by this trace. |
| D7 | **Grace period** | None: deletion is immediate and irreversible after confirmation | A grace period with undo needs a pending state and a scheduled erasure. Not required by Play. |

## 6. External setup and credentials still needed

| Resource | Why | Cost | Local alternative |
|---|---|---|---|
| SMS/OTP provider + India DLT | Web deletion (§3), and sign-in itself | Unverified (per message) | Fake `OtpSender` in tests |
| Public static hosting for the web deletion page | A Play requirement | Unverified (provider not chosen) | None needed for development |
| Play Console "Delete account URL" | A Play requirement | Included in the Play Console account | None |
| Deployed identity with migration 0008 | The endpoint must be live | Part of the deployment (SRE) | Local PostgreSQL (used for these tests) |

No credential, account, hosting or production resource was created for this work.
