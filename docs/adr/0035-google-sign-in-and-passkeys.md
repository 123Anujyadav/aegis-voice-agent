# ADR-0035: Google Sign-In and passkeys for M1; phone number as a separate verification capability

- **Status:** **Accepted for release milestone M1 on internal assurance (2026-11-13, repository owner, rule 8a); independent human review outstanding, and required before M2 (rule 8b).** High-risk. **Not accepted for M2 or production.**
  - **Scope accepted:** D35.1–D35.12, the invariant decisions below (INV-ID-7 option A), and the INV-ID-8 ruling with its limitations. **Passkeys (D35.5)** are accepted as the direction; their implementation waits for ADR-0037, which stays Proposed until the passkey phase.
  - **Earlier status (2026-11-13, same pull request):** Proposed: high-risk.
- **Date:** 2026-11-13
- **Deciders:** the repository owner (see `README.md`, "Approval")
- **Independent human review:** **outstanding.** Required before M2 (rule 8b); OD-12 open. Accepted for M1 on internal assurance only.
- **Consulted:** none
- **Informed:** —
- **Related:** supersedes ADR-0010 §5(a) (SMS OTP as the enrolment method; the phone number as the identity) **for M1 only**. Replaces the email-first direction of ADR-0032 (withdrawn by the owner, 2026-11-13). Leaves ADR-0033 (Accepted for M1) and the MSG91 integration unchanged. Library decisions: [ADR-0036](0036-google-id-token-verification-library.md) (Google ID tokens; Android Credential Manager; accepted for M1) and [ADR-0037](0037-webauthn-verification-library.md) (passkeys; Proposed until the passkey phase). Owner decisions: [`../phase19/AUTH-DECISIONS-2026-11-13.md`](../phase19/AUTH-DECISIONS-2026-11-13.md).

---

## Context

- **The owner's direction (2026-11-13):** Google Sign-In and passkeys are the authentication direction for M1, so that 3–4 real testers can register and sign in on physical Android phones. A phone number becomes a separate, later verification capability. **The MSG91 SMS integration stays exactly as merged** (#301, #316, #317) and must stay usable for future phone verification.
- **What is accepted today:**
  - **ADR-0010 (Accepted, frozen):** "The subscriber's identity in this product **is their phone number**" (§1); SMS OTP at enrolment and re-verification (§5(a)); a non-exportable Keystore P-256 device key (§5(c)); 15-minute access tokens. It rejected a hosted OAuth/OIDC identity provider on residency and fit (§8, Option 2). It anticipates "Passkey-style re-enrolment" only as a later stage (§14, Phase 3).
  - **ADR-0033 (Accepted for M1, rule 8a):** identity generates, hashes and verifies every code; SMS providers only deliver. The F-1 controls (tester allowlist, global hourly SMS cap) apply to sign-in SMS.
  - **ADR-0029/0030/0031** (Proposed, decided under delegation): DPoP-bound EdDSA access tokens, opaque rotating refresh tokens; HMAC key separation and stored random subscriber ids (D30.3, owner decision O2); Play Integrity at issuance.
- **What was proposed but not accepted:** ADR-0032 (2026-10-31 direction, withdrawn by the owner on 2026-11-13): passwordless email code primary, Sign in with Google as an alternative, SMS deferred to phone-ownership verification. No passkeys. Its analysis of Google sign-in (D32.1, D32.3–D32.7) is sound and is reused here.
- **What the code does today (`main` `942534c`):**
  - **The account is the number.** The subscriber id is `usr_` + HMAC(MSISDN) (`internal/app/subscriber_directory.go`, `LegacySubscriberID`). There is no account table; an "account" is the sessions sharing a subscriber id. A stored, random-id directory exists but is not wired.
  - **Sessions are method-neutral.** Once a subscriber id and a verified DPoP key exist, session creation, the 15-minute DPoP-bound access token, refresh rotation with reuse detection, account deletion and edge-api's `GET /v1/me` work for any `usr_` id.
  - **Abuse controls are keyed to the number:** per-number request and failure limits, the lockout, and the F-1 allowlist and cap.
  - **There is no logout or revocation endpoint** (OD-14 open). A session lives 24 hours (absolute).
  - **No Google, OIDC, WebAuthn or passkey code or dependency exists**, on the server or in the Android app.
- **The M1 criterion is method-agnostic:** "a tester signs in on a physical phone against staging" (OD-10). This ADR does not change M0–M3 acceptance criteria.
- **External facts that gate this work** (none exists yet): the permanent package name (D2, after the naming review F26; today `com.callscreen`); a Google Cloud project with an OAuth consent screen and client ids; an HTTPS domain (B3) for the passkey relying-party id and Digital Asset Links; staging (R2); Play App Signing (X1/X2); physical phones with Google accounts.

## Decision Drivers

1. **Security:** no client-side claim is trusted; every credential is verified server-side; every session stays bound to the device's non-exportable key (I5).
2. **No account takeover through linking:** never link by email, name or any unverified attribute.
3. **No change to the MSG91/OTP path,** and no loss of its future use for phone verification.
4. **Small, reversible M1 slice:** Google first, then passkeys, each behind its own gate and switch.
5. **Privacy and residency:** store the minimum; send nothing personal abroad (ADR-0012).
6. **No enumeration oracle** (domain security constraint 2).
7. **Abuse bounded at M1:** only the owner's testers can create accounts, and creation is capped.

## Considered Options

1. **Google Sign-In and passkeys on a stored random account id, with phone verification as a separate capability** (this proposal).
2. **ADR-0032 as written:** email one-time code primary, Google alternative.
3. **Keep SMS OTP as the only sign-in** (ADR-0010 unchanged; M1 through MSG91 and DLT).
4. **A hosted identity provider** (Firebase Authentication, Cognito, Auth0).
5. **Passkeys only.**

## Decision Outcome

**Chosen: Option 1** (accepted for M1, 2026-11-13). Each part is numbered so it can be referred to and, later, amended separately.

### D35.1 One canonical account id, stored and random

- New accounts get `usr_` + 32 lowercase hex characters from 128 random bits (`crypto/rand`), the same shape the erasure table already requires. It is stored in a new `identity_accounts` table (account id, created at, status) and never derived from any identifier.
- **Existing number-derived ids are not migrated or changed.** The OTP route keeps its current derivation, untouched.
- This is ADR-0030 D30.3 (owner decision **O2**), generalised beyond the number: **its acceptance is a prerequisite.**
- **O2, as accepted by the owner (2026-11-13):** "Every account created by Google Sign-In or a passkey receives a stored account id, `usr_` followed by 32 lowercase hexadecimal characters from 128 bits of a cryptographically secure random source, recorded in `identity_accounts` and never derived from any identifier. Existing number-derived ids are not migrated or changed, and the SMS route keeps its derivation. Authentication methods and verified numbers refer to the account id; none is the account's key."

### D35.2 Authentication methods attached to the account

- A new `identity_auth_methods` table: account id, kind (`google` or `passkey`), a **lookup pseudonym**, created at, last used at, revoked at. **Unique on (kind, lookup pseudonym)** among non-revoked rows: one Google identity or one passkey credential belongs to at most one account.
- **Google:** the pseudonym of the issuer and subject together, HMAC under a key used for nothing else (ADR-0030 D30.1 purpose separation). **The email is never a key and is not stored.** Neither are the name or picture.
- **Passkeys:** a separate `identity_passkeys` table holding only what verification needs: the credential id, the COSE public key, the signature counter, the AAGUID, the backup-eligible and backup-state flags, the transports, and timestamps. **No private key exists on the server.**
- An account may hold one Google identity and several passkeys (limit: 10).

### D35.3 The phone number is a separate verification capability

- **Verified phone** is its own record, not an account key: a future `identity_phone_verifications` table (account id, number pseudonym, verified at, method `sms_otp`, revoked at), unique on the number pseudonym among active rows.
- **Google Sign-In never marks a number verified. Passkey registration never marks a number verified.** Nothing in this ADR claims Google proves phone ownership.
- **Future path (not built now):** an authenticated, DPoP-bound "attach a verified number" flow that reuses, unchanged:
  - the MSG91 adapter and its AuthKey file (`CS_IDENTITY_OTP_MSG91_AUTH_KEY_FILE`);
  - the IPv4-only option (`CS_IDENTITY_OTP_MSG91_IPV4_ONLY`);
  - the F-1 tester allowlist and global hourly SMS cap;
  - identity's code lifecycle (generate, hash, expire, verify; ADR-0033);
  - the DLT PE ID, header, template id and MSG91 flow id when they exist.

  It needs its own ADR, because attaching a number to an existing account is a different operation from today's sign-in.
- **Blocker for that future ADR (owner, 2026-11-13): the legacy phone-account collision.** Today's SMS route derives an account from the number. If such a legacy account exists for a number that a Google or passkey account later tries to verify, INV-ID-2 forbids two active accounts on one verified number. The phone-verification ADR must decide, before any number is attached, whether to refuse, merge under the person's explicit control, or retire the legacy account. **Phone verification cannot ship until it does.**
- **Today's SMS sign-in route** (`/v1/otp/request`, `/v1/otp/validate`) creates a number-derived account. **Its code is not changed.** While this ADR is in force the app does not offer it as a sign-in, because beside Google and passkey accounts it would create a second, separate account for the same person (ADR-0032 P4). Staging keeps `CS_IDENTITY_OTP_PROVIDER=none`.

### D35.4 Google Sign-In

- **Client:** Android Credential Manager with Google's ID option (ADR-0036). It returns a Google **ID token**. Lily AI never uses Google access tokens, never requests Google API scopes, and holds no Google credential.
- **Nonce bound to the device key:** the app sends `nonce = base64url(SHA-256("lily.identity.google-nonce.v1\0" + jkt + "\0" + r))`, where `jkt` is its DPoP key thumbprint and `r` is 32 fresh random bytes. This follows ADR-0032 D32.3: a stolen ID token cannot be redeemed with another device key.
- **Endpoint:** `POST /v1/auth/google` with `{idToken, nonceSeed}` and a DPoP proof for that exact URL.
- **Server checks, all required, failing closed:**
  - the RS256 signature against Google's published keys (cached per their cache headers; a key-fetch failure refuses);
  - `iss` is `https://accounts.google.com` or `accounts.google.com`;
  - `aud` equals the configured server client id; `azp`, when present, is one of the configured Android client ids;
  - `exp` in the future and `iat` not in the future, with at most 60 s skew;
  - `nonce` equals the value recomputed from the DPoP key and `nonceSeed`;
  - `sub` present;
  - the token not seen before: its hash is reserved until `exp` (single use).
- **Then:** find the account by the Google pseudonym; if none exists, create one only when the M1 gate allows it (D35.8). Issue Lily AI's own session through the existing issuance: a DPoP-bound access token and a refresh token. **No new token claim:** edge-api is unchanged.
- **Errors are generic:** one answer for every invalid token, and no answer that reveals whether an account exists.

### D35.5 Passkeys (WebAuthn)

- **Relying party:** RP ID = the registrable domain of the production identity host (B3). **Origins:** `android:apk-key-hash:<base64url SHA-256 of the signing certificate>` for each allowed signing certificate (Play App Signing; the upload certificate for internal testing; never a debug certificate in production configuration), and later the web origin if a web client exists.
- **Digital Asset Links:** `https://<RP ID>/.well-known/assetlinks.json` declaring the package and certificate fingerprints with `delegate_permission/common.get_login_creds` (and `common.handle_all_urls` if app links are used). No development domain in production configuration.
- **Registration only from a signed-in session** (DPoP-bound access token plus a fresh proof). Options: `residentKey: required` (discoverable), `userVerification: required`, `attestation: none`, ES256 (and RS256), `excludeCredentials` listing the account's existing passkeys.
- **Sign-in without a username:** empty `allowCredentials`, so the request reveals nothing about which accounts exist.
- **Challenges:** 32 random bytes, stored server-side with their purpose, the account (for registration), the requesting DPoP key thumbprint, and a 5-minute expiry; **consumed atomically once**. The verify request must carry a DPoP proof from the same key, so an assertion relayed to another device cannot mint a session for an attacker's key.
- **Server verification** (ADR-0037): client data type and challenge, origin, RP ID hash, user-present and user-verified flags, the signature with the stored public key, the credential not revoked, and the signature counter: a counter of 0 (synced passkeys) is accepted; otherwise a counter that does not increase is refused and audited as a possible clone.
- **Revocation:** a signed-in user can delete any of their passkeys; a deleted credential is refused at once.
- **Platform reach:** passkeys need Android 9 (API 28) or later with Google Play services (to be confirmed at implementation); the app's minSdk is 26, so on Android 8 only Google Sign-In is offered.

### D35.6 Account linking

- **Explicit and authenticated only:** a method is added from a signed-in session, and adding it requires completing that method's own authentication in the same flow.
- **Never automatic:** never by email, `email_verified`, display name, picture, phone number or any other attribute.
- **Refused:** linking an identifier already attached to another account (**no merge**); removing the last method.
- **Account collision:** a Google identity that already has an account signs into that account; it is never attached to a second one.

### D35.7 Recovery and device replacement

- **New phone, same person:** Google Sign-In on the new phone signs into the same account with a new DPoP device key; synced passkeys (Google Password Manager) also work there. The old device's sessions can be revoked (D35.9).
- **Lost method:** any other linked method recovers the account; users are encouraged to hold both Google and a passkey.
- **Lost every method:** **at M1 the account is lost** (no support-led recovery; owner decision 7, 2026-11-13). The consequences, stated plainly:
  - **The person cannot sign in again** to that account, and nobody at Lily AI can restore access.
  - **They cannot delete it themselves:** in-app deletion needs a signed-in session, and web deletion for Google-only accounts is an M2 item (D35.10). Its data stays stored until a deletion path exists. At M1 the accounts hold only the owner's testers' test data. Erasing such an account would need an operator-run erasure, **for which no tool exists today**.
  - **A new sign-up creates a new, empty account**, if the gate allows it. Nothing from the lost account carries over.
  - **Before M2** the owner decides a recovery position and a deletion path for unreachable accounts (DPDP: the right to erasure).
- **Verified phone as recovery** is out of scope until the phone-verification ADR.

### D35.8 M1 abuse gate: tester allowlist and hourly account-creation cap

- **Tester allowlist (Google):** at M1, a new account is created only for a Google identity whose verified email (`email_verified` true) is on the owner's tester list. The list is a mounted file, held in memory only as pseudonyms, compared case-insensitively; **the email is used for this gate only, never as an identifier, and is not stored.** Existing accounts are also checked on every Google sign-in while the gate is configured.
- **Global hourly account-creation cap:** atomic in PostgreSQL across replicas (the same pattern as migration 0010), failing closed on a store error.
- **Passkeys inherit the gate:** they can only be registered from an account that passed it.
- **Fail closed:** with Google enabled, startup refuses without both the list and the cap. Refusals are one generic answer.
- **Not a replacement for F-1:** the SMS controls stay exactly as they are.

### D35.9 Sessions, logout and revocation

- **Unchanged:** the token profile (ADR-0029), DPoP binding on every request, refresh rotation with reuse detection, the 24-hour absolute session (O4 stays open), Play Integrity at issuance (ADR-0031).
- **New (OD-14 decided as option B by the owner, 2026-11-13):**
  - `POST /v1/session/logout`: revokes the current session (refresh token plus a DPoP proof from its key);
  - `POST /v1/session/revoke-all`: revokes every session of the account, from a signed-in session.
- **Known limit:** an access token stays valid for up to 15 minutes after revocation (it carries no session id and edge-api does not consult sessions; O17). Closing it is a separate decision.
- **Android:** a sign-out action calls logout, clears the encrypted session store, and calls Credential Manager's `clearCredentialState()`.
- **Dated note, 2026-11-14 (rule 11; the text above is unchanged): backend implemented (G3)** under the owner's G3 decisions of 2026-11-14, in `services/go/identity` (`app/session_signout.go`, migration `0012_session_revocations`). Both endpoints take the refresh request's body and a DPoP proof for their own canonical URL, and **check the proof and the session's key before anything can change**, so a refused proof never revokes, whatever refresh token comes with it. Revoke-all includes the calling session and revokes sessions only (sign-in methods are kept). It records each operation with the revocations in one transaction, serialised with sign-ins and erasures on the account's lock, so a retry is answered 204 without revoking a session signed in since. Logout retries are 204 too. No new rate limit (owner decision 4). The known limit above (O17) is unchanged. Refresh and account deletion keep their reuse handling before the proof (decision **R1**, recorded in [`REFRESH-TOKEN-THREAT-ANALYSIS.md`](../phase19/REFRESH-TOKEN-THREAT-ANALYSIS.md) §3.1, is a separate future decision). Android sign-out is not implemented.

### D35.10 Erasure

- Account deletion also deletes the account's methods, passkeys, open challenges and phone verifications, in the same erasure transaction.
- **Re-signup after erasure:** see INV-ID-7 below (two options).
- **Web deletion for a Google-only account** needs Google sign-in on a web page (OD-8 front end): **an M2 item.**

### D35.11 Audit and rate limits

- New audit event types fitting the existing append-only log's constraint (`^identity(\.[a-z_]+)+$`, no subscriber or identifier values): `identity.account.created`, `identity.google.signed_in`, `identity.passkey.registered`, `identity.passkey.authenticated`, `identity.passkey.revoked`, `identity.passkey.counter_regressed`, `identity.session.logged_out`, `identity.session.revoked_all`, `identity.auth.refused`.
- **Never logged:** ID tokens, emails, nonces, challenges, credential ids, public keys, assertions.
- Per-account and global rate limits on every new endpoint, reusing the reservation pattern; no per-IP limit until the trusted-proxy model exists (E6).

### D35.12 What does not change

- The MSG91 adapter, its IPv4-only option, AuthKey file handling, OTP generation, hashing, storage and verification, OTP rate limits, the F-1 tester allowlist and hourly SMS cap, DLT configuration, and the existing OTP and Twilio code and tests.
- The existing identity endpoints and database schema: new tables and routes are additions only.
- The token profile, edge-api, the device key (I5) and Play Integrity handling.

### Domain invariant changes (each needs the owner's separate sign-off, rule 5)

| Invariant | Current wording | Accepted for M1 (owner, 2026-11-13) | Consequence |
|---|---|---|---|
| **INV-ID-1** | "A Subscriber has exactly one active MSISDN" | "A Subscriber has **at most one active verified** MSISDN" | Accounts can exist without a number. Every feature that needs a proven number (call forwarding, telephony, number-based protection) must check verification state, never assume it |
| **INV-ID-2** | "An MSISDN maps to at most one active Subscriber" | "A **verified** MSISDN maps to at most one active Subscriber" | A number can be verified on one account only. **The legacy phone-account collision is a blocker for future phone verification** (D35.3): a number-derived account from today's SMS route for the same number must be refused or reconciled before any number is attached |
| **INV-ID-3** | "A device credential is generated on-device, is non-exportable, and never enters our domain" | **Unchanged** | Passkeys do not replace the DPoP device key; every session stays bound to it |
| **INV-ID-7** | "An erased Subscriber's identifiers are tombstoned and never reused" | **Option A (chosen by the owner, 2026-11-13):** "An erased Subscriber's **account id** is tombstoned and never reused; its authentication identifiers and verified numbers are deleted." | The person may sign up again with the same Google account, as a new, empty account (matches today's code for numbers; O9). No permanent list of erased identities is kept |
| **INV-ID-8** | "Every `PERSONAL` attribute is residency-bound and does not leave the India region except under the four-condition consent gate" | **Wording unchanged.** Ruling with limitations: see "Google Sign-In data flow and the INV-ID-8 ruling" below | Not an unconditional finding. Google's own processing is an external privacy dependency (EPD-1); the privacy notice and data-flow review are required before M2 |
| **INV-ID-9** | "A Subscriber cannot be `ACTIVE` with zero `TRUSTED` Devices" | **Unchanged** | Every sign-in enrols a DPoP device key |
| **INV-ID-10** | "There is no password, passphrase or knowledge factor anywhere in this context" | **Wording unchanged.** Ruling (accepted by the owner, 2026-11-13): "Passkeys do not breach INV-ID-10: user verification (biometric, PIN or pattern) happens on the person's device and only its result reaches identity, as a signed flag; identity never receives, stores or checks a knowledge factor. Google Sign-In is federation, not a knowledge factor held by this context." | No password or PIN is ever stored or checked by Lily AI |

### Google Sign-In data flow and the INV-ID-8 ruling

The complete flow, so the ruling's limits are visible. "Google" means Google Play services on the phone and Google's servers.

| # | From → to | What is sent | Where it is processed, and under whose terms |
|---|---|---|---|
| 1 | **Lily AI app → Google** (on the phone, through Credential Manager and Google Play services) | The server client id (public); the nonce, a SHA-256 hash that reveals nothing about the person; the request options. **Google Play services identifies the calling app by its package name and signing certificate** to match the Android OAuth client. The app sends no Lily AI account data | Google Play services and Google's servers, under Google's terms and privacy policy and the person's Google account settings |
| 2 | **Google → the app** | A Google ID token (a signed JWT): `iss`, `aud` (server client id), `azp` (Android client id), `sub` (the Google account's stable identifier), `iat`, `exp`, `nonce`; and, as Google provides them, `email`, `email_verified`, `name`, `given_name`, `family_name`, `picture` (an image address), possibly `hd`. Credential Manager also exposes display fields from the same account | On the phone. **The app neither stores nor displays these fields**; it forwards the token once |
| 3 | **The app → Lily AI identity** | The ID token as received (so the claims above, including email and name), the nonce seed, and a DPoP proof (the device's public key and a signature) | Identity, **which must run in India** (ADR-0008, `ap-south-1`; staging is not yet provisioned) |
| 4 | **Identity, in India** | Verifies the token. **Fetches Google's public signing keys** from Google's key endpoint (the request carries no personal data; Google sees the server's address and timing). Uses the verified email **only** to check the M1 tester list, in memory. **Stores:** the account id, a pseudonym of `iss`+`sub`, session rows, and audit events without identifiers. **Stores and logs no token or claim** | India. The key fetch goes to Google's servers outside Lily AI's control |
| 5 | **Google, independently** | The person's Google account; the sign-in event; that the person signed in to "Lily AI" (Google lists connected apps in the person's account); the consent screen showing Lily AI's name and privacy-policy address | **Google's own processing, under Google's terms and privacy policy, in locations Lily AI does not govern.** Recorded as **external privacy dependency EPD-1** |

**The ruling (accepted by the owner, 2026-11-13; INV-ID-8 wording unchanged):**

> "INV-ID-8 is unchanged. Ruling for M1, with its limitations: for Google Sign-In, identity itself transfers no personal attribute out of India. It receives the Google ID token (which carries the person's Google subject identifier and may carry their email address, name and profile-picture address) only to verify it in India, uses the verified email only for the M1 tester gate, keeps no claim, logs none, and stores only a pseudonym of the Google issuer and subject. The person's use of their Google account, including Google recording that they signed in to Lily AI, is processed by Google under the person's own relationship with Google, in locations Lily AI does not govern: an external privacy dependency (EPD-1). The ruling holds only while identity runs in India, stores and logs no token claim, and sends Google nothing but requests for its public signing keys. It is not a privacy-law conclusion: the privacy notice and a data-flow review (L1, L4; the DPDP review E9) are required before M2 and before any public release."

**Limitations, stated plainly:**
- Steps 1, 2 and 5 happen between the person and Google. Lily AI does not control where Google processes them.
- The ruling depends on identity being deployed in India and on the implementation storing and logging no claim; both are checked in the implementing pull requests and at staging.
- **Required before M2 and before any public release:** the privacy notice (naming Google Sign-In and EPD-1), the Play Data safety form, and a data-flow review by privacy and legal (L1, L4; E9).

### Consequences

**Positive**
- Testers can sign up without SMS, DLT or MSG91 activation; M1 no longer waits on R1 for authentication.
- Phishing-resistant sign-in (passkeys) and no shared secret on the server.
- The phone number becomes an explicit, verifiable capability instead of an assumption.
- MSG91 and the OTP lifecycle are preserved for phone verification.

**Negative**
- A high-risk change to the identity model: supersedes ADR-0010 §5(a) for M1 and changes INV-ID-1, INV-ID-2 and (option A) INV-ID-7.
- **Google becomes a dependency for account creation at M1.** A Google outage blocks new sign-ins (existing sessions continue). Testers need Google accounts.
- **A compromised Google account is a full takeover** of a Google-only account (mitigated by passkeys, revoke-all, and audit).
- New external setup: Google Cloud project and OAuth clients, the domain and Digital Asset Links, per-signing-certificate configuration for every build type.
- New dependencies (ADR-0036, ADR-0037), a larger APK (the 2% size gate must be re-baselined), and new privacy disclosures.
- SMS sign-in is not offered while this is in force.

**Neutral**
- The token profile, edge-api and the 24-hour session are unchanged.

### Confidence

**Medium.** The verification steps are standard and well documented; the design reuses the existing DPoP-bound session machinery. Uncertainty: the Android passkey origin and Digital Asset Links details must be confirmed against current platform documentation at implementation, the dependency footprint must be measured (ADR-0036/0037), and the physical-device path depends on external setup.

### Revisit Trigger

- Google sign-in failures (excluding user cancellation) above 2% of attempts over 7 days.
- Any confirmed account takeover through a linked method.
- Any passkey counter regression (`identity.passkey.counter_regressed`) not explained by a known authenticator bug.
- A market launch outside India, or a requirement for a sign-in method without a Google account.
- Before M2: the independent human review (rule 8b).

## Options in Detail

### Option 2: ADR-0032 as written (email code primary)
- **Rejected for M1:** needs an email delivery provider (another vendor and approval), keeps a one-time code as the primary factor, and adds no phishing resistance. Its Google analysis is reused here.

### Option 3: SMS OTP only (ADR-0010 unchanged)
- **Not chosen as the M1 direction:** needs DLT registration, KYC and paid SMS (R1) before any tester can sign in. **It stays available:** nothing in the MSG91 path changes.

### Option 4: a hosted identity provider (Firebase Authentication, Cognito, Auth0)
- **Rejected,** on ADR-0010 §8's grounds: residency (accounts held outside India), lock-in, and a second session system beside the DPoP-bound one. Verifying Google's ID token directly needs no hosted provider.

### Option 5: passkeys only
- **Rejected for M1:** a passkey must be created from an existing account, and Android 8 devices (minSdk 26) cannot use passkeys. Google Sign-In gives the first, recoverable account.

## Dependency sequence

**Google Sign-In first:**
1. **Owner decisions:** accept this ADR for M1 (rule 8a), the invariant sign-offs, O2, ADR-0036; decisions 3–7 of the summary.
2. **External, owner-approved:** the permanent package name (D2/F26); a Google Cloud project with the OAuth consent screen, a server (web) client id and Android client ids per package and signing certificate (debug, upload, Play App Signing).
3. **Backend:** migrations for accounts and methods; the account directory; a separate session-issuance helper (the OTP code is not refactored); `POST /v1/auth/google` with the M1 gate; tests against a local fake issuer with test keys.
4. **Logout and revoke-all** (OD-14 B), with tests.
5. **Android:** Credential Manager behind an interface; the "Continue with Google" screen; sign-out; the client id as a build value with its release preflight; strings and translation worksheets; the APK size baseline.
6. **Staging (R2) and the domain (B3):** a reachable HTTPS identity endpoint for phones.
7. **Physical devices:** sign-up, sign-in, restart persistence, logout and revoke-all on the testers' phones.

**Passkeys second:**
8. **Owner:** accept ADR-0037; the RP ID (the B3 domain).
9. **External:** host `assetlinks.json` on the RP ID with the release and upload fingerprints.
10. **Backend:** challenges and passkey tables; registration, authentication and deletion endpoints; tests with a software authenticator.
11. **Android:** passkey creation (from settings, signed in) and sign-in; device tests.
12. **Physical devices:** registration, sign-in, deletion, a second passkey, device replacement.

**Later, separate ADR:** attaching a verified phone number through the existing MSG91 path.

## Security impact (summary; detail in each decision)

- New trust in Google's ID-token signatures and in authenticator assertions, both verified server-side with maintained libraries (ADR-0036/0037).
- Replay: single-use ID tokens, single-use challenges, DPoP proof replay table, and key-bound nonces and challenges.
- Takeover: no automatic linking; refusal of identifier reuse; revoke-all; audit events; the M1 allowlist.
- Enumeration: generic errors; usernameless passkey sign-in.
- Unchanged: device-bound sessions (I5), refresh reuse detection, erasure fence.
- Residual: Google-account compromise; the 15-minute access-token window after revocation (O17); no recovery without a method at M1.

## Privacy impact

- **Stored:** the account id; pseudonyms of the Google subject; passkey credential ids, public keys, counters, AAGUIDs and flags; timestamps.
- **Not stored:** email, name, picture, Google tokens, private keys.
- **External privacy dependency EPD-1:** Google's own processing of the person's Google account and sign-in events (data flow step 5), under Google's terms, in locations Lily AI does not govern.
- **Disclosures** (privacy policy, Play Data safety): Google Sign-In as a sign-in method; passkeys. Privacy and legal items L1–L5 (ADR-0032) apply. `ANDROID-DATA-INVENTORY.md` gains Credential Manager and Google's ID library.

## Approval record

| Field | Record |
|---|---|
| Decision | **Accepted for M1 on internal assurance (rule 8a), 2026-11-13:** D35.1–D35.12; passkeys (D35.5) as the direction, implemented only after ADR-0037 is accepted. **Not accepted for M2 or production** |
| Classification | **High-risk** (the authentication model, identities and sessions; supersedes part of an Accepted ADR; domain invariants) |
| Version | The pull request #318 head at merge; this file's SHA-256 is recorded in the merge commit message |
| Review trigger | For superseding ADR-0010 §5(a) for M1: **no §16 trigger fired.** The owner ruled that the basis is the product direction of 2026-11-13 (rule 2) |
| Owner decision | 2026-11-13, by the owner's explicit written decisions in the Claude Code session of 2026-11-13, recorded by pull request #318: accept ADR-0035 for M1; accept ADR-0036; keep ADR-0037 Proposed until the passkey phase; accept O2 as worded (D35.1); withdraw ADR-0032's email-first direction; stop offering SMS one-time codes as a sign-in and keep the MSG91 code for future phone verification; the M1 Google tester allowlist and a cap of 5 new accounts per hour; OD-14 option B; no support-led recovery at M1 with its consequences documented (D35.7). See [`AUTH-DECISIONS-2026-11-13.md`](../phase19/AUTH-DECISIONS-2026-11-13.md) |
| Independent human review | **Outstanding** (OD-12). Required before M2 (rule 8b) |
| Invariant sign-offs | 2026-11-13, the owner, for M1: **INV-ID-1** and **INV-ID-2** as worded above (with the legacy phone-account collision as a blocker for phone verification); **INV-ID-7** option A as worded above; **INV-ID-8** wording unchanged, with the ruling and limitations above; **INV-ID-10** wording unchanged, with the ruling above. INV-ID-3 and INV-ID-9 unchanged |
| Other approvals | Privacy and legal (L1–L5; DPDP review E9): **required before M2**, including the INV-ID-8 data-flow review and EPD-1 |
| AI involvement | Drafted by Claude Code (AI-performed analysis); not reviewed by a human |
| Does not | Authorise any external setup (Google Cloud project, OAuth clients, package-name change, domain, staging), deployment, production use, or any change to MSG91, OTP, Twilio, the IPv4-only option, or any existing endpoint or schema. Each implementation step is its own pull request |
| Supersedes | ADR-0010 §5(a), **for M1 only** (ADR-0010 gets a dated note; its text is unchanged). Replaces ADR-0032's email-first direction (ADR-0032 gets a dated note) |

## References

- ADR-0010, ADR-0012, ADR-0029, ADR-0030, ADR-0031, ADR-0032 (and [review packet](reviews/0032-architecture-review-packet.md)), ADR-0033, ADR-0036, ADR-0037
- `docs/domain/10-identity.md` §10.12; `ARCHITECTURE_FREEZE.md` §6.1–§6.2
- `docs/phase19/OD-10-DECISION-2026-11-09.md` (M1 criterion); `docs/phase19/OWNER-DECISION-PACKET-2026-11-09.md` (OD-14)
- `docs/phase19/MSG91-INTEGRATION.md`, `docs/phase19/OTP-PROVIDERS.md`
- W3C Web Authentication Level 3; RFC 9449 (DPoP); OpenID Connect Core 1.0 §3.1.3.7 (ID token validation)
