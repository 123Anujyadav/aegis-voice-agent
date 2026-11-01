# Android release: permissions and data inventory

- **As of:** 2026-10-26, `main` `0eaeb91` plus #187.
- **Purpose:** evidence for the Google Play **Data safety** form and the **privacy policy**. Every row is traced to code, the merged release manifest, the release dependency tree, or a backend schema.
- **What this is not:**
  - It does not claim legal compliance.
  - It does not treat a permission as proof that data is uploaded.
  - It does not say "never shared" unless the whole flow shows it.
- **Nothing here has been submitted.** Section 6 lists the owner's questions.

**Status key:**
- **VERIFIED:** current release behaviour, shown by code and build output.
- **WHEN CONFIGURED:** happens only once the release build is given hosts or keys that the default build does not have.
- **DEBUG ONLY:** never in a release build.
- **PLANNED:** in ADRs or domain docs, not implemented.
- **UNVERIFIED:** a provider's own behaviour, not shown by our code.
- **DECISION:** needs the owner, legal or privacy review.

## 1. What the release build is

| Fact | Evidence |
|---|---|
| **Permissions** (merged release manifest, `:app:processReleaseMainManifest`) | `INTERNET`; `READ_CONTACTS`; and `com.callscreen.DYNAMIC_RECEIVER_NOT_EXPORTED_PERMISSION` (AndroidX core's own signature permission, for its non-exported receivers). Nothing else: no phone state, call log, SMS, microphone, location or notifications. CI now fails if this set changes without `android/app/release-permissions.txt` (#187). |
| **Components** | `MainActivity` (launcher); `PreFilterScreeningService` (exported only behind `BIND_SCREENING_SERVICE`, which only the system holds); AndroidX `InitializationProvider` (not exported); `ProfileInstallReceiver` (behind `DUMP`, system and adb only); Play Core's `PlayCoreDialogWrapperActivity` (not exported). |
| **Third-party SDKs** (`releaseRuntimeClasspath`) | Google Play Integrity (`com.google.android.play:integrity`, with Play Services basement and tasks and Play Core common); OkHttp and Okio; Timber; AndroidX, Jetpack Compose, Hilt and Dagger; kotlinx. **No analytics, crash-reporting, advertising, attribution or Firebase SDK.** |
| **Default configuration** | `callscreen.identityBaseUrl`, `callscreen.edgeApiBaseUrl` and `callscreen.playIntegrityCloudProjectNumber` are **empty**. A release built from `main` with defaults therefore **makes no network request at all**: sign-in reports itself unavailable, and no Play Integrity token is requested. The rows marked WHEN CONFIGURED describe a launch build with hosts set. |
| **Logging** | A release build plants no Timber tree and no production code logs directly (`CallScreenApplication`, `ReleaseLoggingGuardTest`), so **it writes nothing to logcat**. |
| **Backup** | `android:allowBackup="false"`, and the caller list is in `noBackupFilesDir`: nothing is copied to Google backup. On Android 12+, `android:dataExtractionRules` also excludes every domain from cloud backup and from device-to-device transfer (`res/xml/data_extraction_rules.xml`), which `allowBackup` alone no longer stops. |
| **Defect fixed by #187** | `INTERNET` was missing, so on a device every server call failed with `socket failed: EPERM`. That was proven by `NetworkPermissionTest` on the Android 15 emulator, which fails without the permission and passes with it. |

## 2. Permissions and what triggers them

| Permission | Triggered by | Purpose | Data access | Status |
|---|---|---|---|---|
| `INTERNET` | Install-time (a normal permission, no prompt) | HTTPS to the configured identity and edge-api hosts (`core:network`) | Network only | VERIFIED declared. Traffic only WHEN CONFIGURED |
| `READ_CONTACTS` | **Only** the subscriber tapping "Allow contacts access" on the blocklist notice (`BlockingNotice`), to block saved contacts too. Never in onboarding. | Android passes a saved contact's call to the screening service only if the app holds this permission (AOSP `CallScreeningService.onScreenCall`; emulator T10a/T10b) | **The app never reads the contacts provider** (`BlockingStatusTest` source guard: no `ContactsContract`, no contacts URI). Contacts are not stored or sent. | VERIFIED |
| Call screening **role** (`ROLE_CALL_SCREENING`; a role, not a permission) | The subscriber choosing "Continue" on A09b, or "Turn on blocking" or "Switch back" later | Lets Android ask the app about each incoming call | The incoming number, per call (§3) | VERIFIED |

## 3. Data the app handles

| # | Data | Evidence (source) | Purpose | Destination | Occurs in the release build? | Retention and deletion | Open question |
|---|---|---|---|---|---|---|---|
| D1 | **The subscriber's phone number** (typed at sign-in) | `PhoneVerificationViewModel` → `IdentityClient.requestOtp` (`POST /v1/otp/request {msisdn}`) | Sign-in (OTP) | Identity backend. It **stores only an HMAC-SHA-256 pseudonym** (migration `0004_msisdn_pseudonyms`), not the number. | WHEN CONFIGURED (identity host set) | Pseudonymised OTP and rate rows are deleted by housekeeping after expiry plus 1 h, or the 15-min window plus 1 h (`pgstore/housekeeping.go`) | **OTP delivery:** no SMS provider is implemented (`otpdelivery`), so no code is sent today. A provider would receive the number and the code: a **third party** (§5). |
| D2 | **One-time code** | `IdentityClient.enrol` (`POST /v1/otp/validate`) | Sign-in | Identity: compared with `code_hash` (only the hash is stored) | WHEN CONFIGURED | The challenge row is deleted after expiry plus 1 h | none |
| D3 | **Device key** (EC P-256, Android Keystore, non-exportable; StrongBox if available) | `KeystoreProofKey` (`core:security`) | Proves each request comes from this device (DPoP) | Private key: **never leaves the Keystore**. Public key (JWK) and signatures go to identity and edge-api in the `DPoP` header; identity stores the key thumbprint on the session. | Key: created at first sign-in, WHEN CONFIGURED. Headers: WHEN CONFIGURED | Session rows (with thumbprint) are deleted 1 h after expiry (24 h). DPoP replay rows are deleted after expiry. The key stays on the device until uninstall. | **DECISION:** Data safety "Device or other IDs" covers an app-generated key thumbprint. Confirm how to declare it. |
| D4 | **Session and tokens** (session id, access token, refresh token, expiry) | `EncryptedSessionStore` / `DataStoreBlobStore` (`core:datastore`) | Staying signed in | **Device:** AES-256-GCM with a Keystore key, in DataStore `callscreen_identity_session`. **Identity:** session row and refresh-token hash. | WHEN CONFIGURED | Device: cleared on sign-out (`IdentityAuthenticator.signOut`) or when unreadable. Server: deleted after expiry plus 1 h, together with its superseded refresh-token hashes (`ON DELETE CASCADE`, migration 0007). | **DECISION:** no screen calls `signOut()` yet, and there is no server-side account deletion (§6, Q1). |
| D5 | **Play Integrity token** | `GooglePlayIntegritySource` (Standard API, `requestHash` bound to the DPoP proof) → `Integrity-Token` header at enrolment, and on refresh at most every 6 h | Device integrity at sign-in (ADR-0031, Proposed) | Google Play services (on device) issues it; sent to identity. **Identity's verifier is dormant** (#164): the token is not yet sent to Google. | WHEN CONFIGURED (Cloud project number set) | The token itself is not stored. Once the verifier is wired, identity also keeps a **device attestation** (`dev_<key thumbprint>`, when attested, whether Play-certified) with **no expiry** (`app/integrity.go`). It is deleted with the account (#188); its retention is an open item (`ACCOUNT-DELETION.md` §5 D6). | **UNVERIFIED:** what Google Play services collects to produce a verdict. Confirm Google's Data safety guidance for Play Integrity. |
| D6 | **Edge-api call** (`GET /v1/me`) | `EdgeApiClient` (`core:auth`, using `core:network`'s HTTP client) | Account check | Edge-api: access token and DPoP proof. **Response:** `sub` and the device thumbprint. | WHEN CONFIGURED | Replay records expire | none |
| D7 | **Incoming caller's number** (per call) | `PreFilterScreeningService` / `PreFilter` | Decide: reject a blocked number, allow anything else | **On the device, in memory only.** Not stored, logged or sent by the app. Android's own call log records a blocked call as blocked (a system record, not the app's). | VERIFIED (when the role is held) | Not retained by the app | none |
| D8 | **Blocklist and allowlist** (numbers, optional label, source, date) | `CallerListStore` → `no_backup/caller_list.json` | The subscriber's own lists | **Device only**, app-private, not encrypted at the application level, excluded from backup. **No sync.** | VERIFIED | Until the subscriber removes an entry (5 s undo) or uninstalls. An unreadable file is kept until the subscriber chooses "Start again". | **DECISION:** a server-side allowlist (readiness §2b) would change this row |
| D9 | **SIM state and operator name** | `SimSummary` / `TelephonyManager` (no permission) | Shown on the SIM screen | **Displayed only.** Not stored or sent. | VERIFIED | Not retained | none |
| D10 | **"Role held before" flag** | `PreferencesFlag` (SharedPreferences `callscreen_screening_role`) | Detecting the role being taken by another app (F14) | Device only | VERIFIED | Until uninstall | none |
| D11 | **Crash and diagnostic data** | No SDK; release logs nothing | n/a | None | **Not collected** (VERIFIED: no SDK, no logging) | n/a | **PLANNED:** a crash-reporting tree via the backend (`CallScreenApplication` comment). It would need declaring when built. |

## 4. Debug builds only (never in release)

- **Design previews:** 16 prototype screens, sample data only, labelled. A release graph does not register them (`DesignPreviewGatingTest`).
- **Logging:** Timber `DebugTree` writes to logcat.
- **Start-screen extra:** the `startDestination` intent extra (#180).
- **Not shipped in release:** Compose tooling and the test manifest are `debugImplementation`.

## 5. Planned, not implemented (nothing below happens today)

From the ADRs. These must be added to the Data safety form and the policy **when built**:

| Flow | Source | Data and destination when built |
|---|---|---|
| Call forwarding to the AI (Stage 2) | ADR-0002 | Unanswered callers' audio reaches the telephony gateway (carrier → platform number). **Callers' voices**: personal data of non-subscribers. |
| Speech-to-text, LLM and text-to-speech | ADR-0005, ADR-0006, ADR-0007 (ElevenLabs, Cartesia; Sarvam evaluated) | Call audio and text sent to **third-party AI providers**. Cross-border only with consent (ADR-0012 §5.4). |
| Transcripts and recordings | ADR-0012 §5.1–5.2 | Transcripts (opt-in, 90 days by default); audio (opt-in, **off**, 30 days by default); consent records in identity. |
| Call metadata, history, fraud verdicts | ADR-0012 retention table; fraud domain | Server-side, 12 months for call metadata, 90 days for fraud verdicts. A reputation feed would be k-anonymised. |
| OTP SMS | `otpdelivery` (no provider); India DLT | Phone number and code sent to an **SMS provider** |
| Push notifications | FCM (not integrated) | FCM token and notification payloads, sent to **Google Firebase** |
| Server-side allowlist sync | Readiness §2b (recommendation only) | Allowed numbers as keyed hashes. Needs an ADR-0012 amendment. |

## 6. Questions the owner must answer before submitting

1. **Account deletion (Play policy).** **Update:** in-app deletion is implemented in #188 (identity `POST /v1/account/delete`) and #189 (Android), and security-reviewed in #191; web deletion has an implementation-ready plan but is not built. See `ACCOUNT-DELETION.md` for the per-data semantics, the review, the web plan (§3) and the decision register D1–D8 (§5). The original question follows. Sign-in creates an account, but there is no account-deletion path: identity has only `/v1/otp/request`, `/v1/otp/validate` and `/v1/token/refresh`, and no in-app sign-out or delete screen. **Options:**
   - (a) Add an identity deletion endpoint, an in-app "Delete account", and a web deletion page before launch.
   - (b) Launch with sign-in disabled, so no account exists.

   **Consequence:** Play requires (a) for apps with accounts. Server data already expires within about 25 h. The audit table keeps session ids under LEGAL_HOLD (ADR-0010 §10), and **the policy must say so**.
2. **Data safety: data collected** (when hosts are configured): the **phone number** (Personal info, for account management and security); a **device or other ID** (the app-generated key thumbprint). Confirm both, and that **nothing is "shared"**. Today the only recipients are the operator's own services; an SMS provider would be a service provider.
3. **Data safety: data not collected:** contacts (never read); call logs (none read, only Android's own); caller numbers (processed on the device and never sent); the lists (on the device only); SIM details (displayed only); crash and diagnostics (none). Confirm.
4. **Play Integrity:** confirm with Google's documentation how Play Integrity data is declared (D5, UNVERIFIED).
5. **Encryption in transit:** yes. The clients refuse non-HTTPS origins; certificate pinning applies once pins are set (ADR-0010).
6. **Privacy policy text** (owner or legal) must cover:
   - **Contacts permission:** asked only on request; contacts never read or uploaded.
   - **Screening on the device:** caller numbers are checked on the phone and not sent anywhere.
   - **Phone number:** stored server-side as a pseudonym.
   - **Retention:** OTP and session data expires within about 25 h; audit records are kept under legal hold.
   - **The DPDP Grievance Officer** (ADR-0012).
7. **Before enabling any planned flow (§5):** update this inventory, the Data safety form and the policy first. Callers' voices and AI providers are a material change, and need the ADR-0012 consents built first.
8. **Independent review:** the DPDP and privacy review (readiness §3) should check this inventory before submission.
