# Production readiness tracker

- **As of:** 2026-11-01, `main` `0d1ed04`.
- **Verdict: not production-ready.** Nothing has been deployed anywhere, no physical device has run the app, and no Play release exists. Builds and CI passing are not readiness (§1).
- **Method:** every entry cites the code, test, workflow or document it rests on. It comes from an audit of the repository at `7f60bac`, re-checked against each later merge. Nothing is estimated, and there are no percentages.
- **IDs:** task and blocker IDs are the ones defined in [`ROADMAP-INVENTORY.md`](ROADMAP-INVENTORY.md) (C, T, OD, L, E, X, H) and [`EXTERNAL-DEPENDENCIES.md`](EXTERNAL-DEPENDENCIES.md) (O). This document does not redefine them. F-numbers are this document's own findings (§4).
- **Reviews:** the findings in §4 come from an AI-performed review. They are not an independent human review (`ARCHITECTURE_FREEZE.md` §6.1).

## 1. Evidence levels

| Level | Means | Where it exists today |
|---|---|---|
| **Implemented** | Code on `main` | Most areas (§2) |
| **Automated tests** | Run in CI on every relevant PR and on `main`: unit, Robolectric, real PostgreSQL, and the live contract against local binaries | Backend; Android JVM and Robolectric tests; `android-identity-contract` |
| **Emulator** | The instrumented tests (`app/src/androidTest`) on an Android emulator: in CI by `android-device-tests.yml` (#206, `bba198d`), or by hand | Screening, the network-permission check, the Keystore key, and sign-in, refresh and edge-api `GET /v1/me` with real Keystore keys against a local HTTPS fake of identity (`SessionOnDeviceTest`, #207). Not against real hosts |
| **Physical device** | Any run on a real phone | **None** (X3) |
| **Staging-verified** | Exercised on deployed staging hosts | **None.** No staging hosts exist (X5) |
| **Production-verified** | Exercised in production | **None** |

A runbook is not evidence that an operation works. Every runbook in `docs/runbooks/` says it has not been exercised.

## 2. Summary

| # | Area | Implemented | Automated tests | Emulator | Physical device | Staging | Production |
|---|---|---|---|---|---|---|---|
| 1 | Authentication and account lifecycle | Yes, except SMS delivery | CI | — | None | None | None |
| 2 | Android session and edge-api integration | Yes | CI | `SessionOnDeviceTest` (#207): API 35, 26 and 28 in CI | None | None | None |
| 3 | DPoP and device-key security | Yes | CI | Keystore key test | None | None | None |
| 4 | Phone verification and calling permissions | Partly: no code can be delivered (E2) | CI | Role tests | None | None | None |
| 5 | Call screening and reliability | Yes, on-device prefilter | CI | Instrumented and manual cases | None | n/a, on-device | None |
| 6 | Account deletion and data erasure | In-app yes; web flow built but dormant | CI | — | None | None | None |
| 7 | Backend security and API correctness | Yes, with gaps (§3.7) | CI | n/a | n/a | None | None |
| 8 | Infrastructure, deployment and observability | Templates only | Partial | n/a | n/a | **Nothing deployed** | None |
| 9 | Android compatibility and performance | Yes, no baseline profile | CI: the minified build | API 35, 28 and 26 in CI | None | n/a | None |
| 10 | Automated tests and failure recovery | Yes | CI | #206, `bba198d` | None | None | None |
| 11 | Play Store, privacy and release validation | Bundle build only | CI with a throwaway key | — | None | Never run for real | None |

## 3. Areas

Each area lists the evidence, then the remaining blocker and what it depends on, then the next concrete action.

### 3.1 Authentication and account lifecycle (identity)

- **Implemented:**
  - OTP request and enrolment with a device proof;
  - refresh with rotation and reuse detection;
  - per-number rate limits and lockout;
  - strict input limits, per-request deadlines and panic recovery (`services/go/identity/internal/httpapi`, `internal/app`).
  - Session lifetime is a fixed 24 hours (`internal/app/identity.go`, `ExpiresAt`).
- **Automated tests:**
  - unit, HTTP and real-PostgreSQL suites (`identity-postgres.yml`, including the identity-to-edge lifecycle test);
  - the Android client's live contract (`android-identity-contract.yml`);
  - the repeated shuffled race run (`hardening.yml`, #206, `bba198d`).
- **Emulator:** `SessionOnDeviceTest` (#207): enrolment and refresh signed by a real Keystore key, the session sealed by a real Keystore AES key, against a local HTTPS fake of identity. Passed on API 35, 28 and 26 in CI. Not against a real identity host
- **Physical device:** none.
- **Staging and production:** none.
- **Blocker:**
  - **C2/E2:** there is no SMS provider, so every code request answers 503;
  - C4 (Play Integrity dormant); C9/OD-1; C12; C13;
  - O3, O4 (session lifetime), O5;
  - H1 (ADR-0032, Proposed).
  - Depends on the owner choosing an SMS provider and completing DLT registration.
- **Next action:** the owner chooses the provider (E2). Then C2 implements the adapter against the contract from #198.

### 3.2 Android session and edge-api integration

- **Implemented:**
  - an encrypted session store and single-flight refresh (`IdentityAuthenticator`);
  - cold-start routing and the A73 notice (C16, #202);
  - session confirmation with `GET /v1/me`, at most hourly and never at launch, where a temporary failure never signs the user out (C17, #203);
  - a Keystore failure while signing keeps the session instead of crashing (C18, #204, `d6cfa8d`).
- **Automated tests:**
  - JVM and Robolectric suites in `pr-android`;
  - `SessionCheckWiringTest`, which runs the real authenticator and edge client against local servers;
  - the live contract (`EdgeLiveContractTest`).
- **Emulator:** `SessionOnDeviceTest` (#207): enrolment, refresh, a restart and edge-api `GET /v1/me` with a token-bound proof, on API 35, 28 and 26 in CI; local fakes, not real hosts
- **Physical device:** none.
- **Staging and production:** none (T3 is blocked on X5 and E2).
- **Blocker:**
  - **F1:** a refused refresh proof signs the user out. Proposed in [`F1-REFRESH-PROOF-REJECTION.md`](F1-REFRESH-PROOF-REJECTION.md); high-risk, so it needs the owner and OD-12.
  - F3: a corrupt session file is never recovered (owner).
  - F8: there is no sign-out action (owner).
- **Next action:** the owner decides F1 (H7), F3 (OD-13) and F8 (OD-14). T3 waits on X5 and E2.

### 3.3 DPoP and device-key security

- **Implemented:**
  - a non-exportable Keystore P-256 key, in StrongBox when available, destroyed after a confirmed deletion;
  - server checks of `htm`, `htu`, `ath`, `iat` and `jkt`;
  - a shared PostgreSQL replay store that fails closed (`packages/go/dpop`, `services/go/edge-api/internal/auth`);
  - edge-api per-request deadlines, so a stalled replay store answers 503 (C19, #205, `4a78f97`).
- **Automated tests:**
  - Go unit tests and real-PostgreSQL replay tests across instances;
  - Android unit tests;
  - the race runs (#206, `bba198d`).
- **Emulator:** `DeviceKeyKeystoreTest`.
- **Physical device:** none. Real StrongBox hardware has never been exercised.
- **Staging and production:** none. Key rotation has never been exercised (T5, E3).
- **Blocker:**
  - E8 (no external cryptography review);
  - C10/O17/H2 (access tokens are not revoked);
  - O1/H4.
- **Next action:** the owner arranges the external review (E8). T5 waits on staging.

### 3.4 Phone verification and calling permissions

- **Implemented:**
  - the onboarding flow;
  - the screening-role request (A09b);
  - `READ_CONTACTS` as the only contacts permission;
  - the release permission list, pinned and checked in CI.
- **Automated tests:**
  - ViewModel and Robolectric screen tests;
  - the release permission check.
- **Emulator:** the role tests (`ScreeningRoleStatesTest`) and `NetworkPermissionTest`.
- **Physical device:** none.
- **Staging and production:** none.
- **Blocker:**
  - **E2:** no one can receive a code;
  - OD-1/C9; L6.
  - The notification permission (A09a) is not implemented. The app posts no notification today.
- **Next action:** E2, as in §3.1.

### 3.5 Call screening and reliability

- **Implemented:**
  - an on-device prefilter in `CallScreeningService`, which fails open on error and is exported only to the system;
  - caller lists in `noBackupFilesDir`;
  - recovery from a corrupt list.
- **Automated tests:** the prefilter, list store and role-watch unit tests.
- **Emulator:** the instrumented screening tests, and 14 manual cases on an API 35 emulator (`ANDROID-SCREENING-DEVICE-VALIDATION.md`). API 28 also ran by hand.
- **Physical device:** none. No carrier has been tested (T2).
- **Staging and production:** none.
- **Blocker:**
  - **T2**, blocked on X3 (a phone) and X4 (SIMs);
  - C7/OD-2/H3; C8/X4; OD-3; OD-11.
- **Next action:** the owner provides a phone and SIMs (X3, X4); then run sheet M1–M12.

### 3.6 Account deletion and data erasure

- **Implemented:**
  - in-app deletion (Settings);
  - erasure in identity (migration `0008`);
  - the web deletion flow, built but dormant (`0009`);
  - the device key destroyed after a confirmed deletion;
  - app data excluded from cloud backup and from device-to-device transfer (C21, #207, `3ef3b84`);
  - an unreadable stored session no longer crashes the deletion screen: it shows "not signed in" and deletes nothing (#207, `3ef3b84`).
- **Automated tests:**
  - unit, HTTP, real-PostgreSQL and end-to-end lifecycle tests;
  - the live contract (`IdentityLiveContractTest.accountDeletion`);
  - `DataExtractionRulesTest`.
- **Emulator and device:** none.
- **Staging and production:** none. `ACCOUNT-DELETION.md` records that deletion has never run against a live server.
- **Blocker:**
  - **C1/E10:** the web deletion page Play requires;
  - C3/O13;
  - residual risks R1, R2, R4 and R6, and D1–D9 (O9–O17);
  - C15/O12.
- **Next action:** the owner decides E10 (the frontend stack and domain for the web page).

### 3.7 Backend security and API correctness

- **Implemented:**
  - **identity:** strict input, fixed error text and deadlines;
  - **edge-api:** DPoP-only authentication, refusal to start without it, security headers and request deadlines (C19).
- **Automated tests:** on every PR:
  - gitleaks;
  - govulncheck, golangci-lint and pip-audit;
  - the Android dependency audit (F9, #206, `bba198d`: OSV-Scanner 2.7.0, 133 release libraries, no advisories);
  - Dependabot.
- **Staging and production:** none.
- **Blocker:** C13/E6: there is no per-source rate limiting, and edge-api has none at all.
- **Next action:** routine: decide where edge-api's limiter sits once E6 (the edge or proxy) is chosen. Until then, limits in-process only.

### 3.8 Infrastructure, deployment and observability

- **Implemented:**
  - only a generic Kubernetes template (`infra/k8s/base`);
  - a Go Dockerfile without digest pinning;
  - local Compose.
  - There is no Terraform.
  - identity and edge-api expose metrics; tracing goes to a log exporter only.
- **Automated tests:**
  - `container-images.yml` (#206, `bba198d`) builds the identity and edge-api images on every relevant PR and checks them: non-root, the static binary starts in the scratch image, the commit is logged, and each behaves as documented with no configuration. Nothing is pushed.
  - Nothing deploys anything.
  - No alert rules exist anywhere. Under Accepted ADR-0013, dashboards and alert rules are operator artefacts kept outside this repository, so they wait on E5 (the observability backend). The signals they need exist: for example `identity_security_audit_failures_total`, which identity's code says should page.
  - **The base images are pinned by tag only.**
    - The Dockerfile leaves digests to Renovate (`pinDigests: true` in `renovate.json5`), but Renovate has never opened a PR in this repository.
    - Dependabot's `docker` entry has `open-pull-requests-limit: 0`.
    - So nothing would keep a digest current, and the tag is rebuilt upstream: on 2026-11-01 the registry's digest for `golang:1.26.10-alpine3.23` differed from one pulled earlier the same day.
    - Pinning without an updater would freeze base-image security fixes.
- **Staging and production:** **nothing deployed.**
- **Blocker:**
  - X5, E3, E4, E5;
  - `INTERNAL-TESTING-SETUP.md` B1 and C1. The per-service manifests are in owner-held, uncommitted work.
- **Next action:** the owner chooses the staging account and budget (X5), decides F12 (OD-15), and chooses how base images are kept current: enable the Renovate app, which is an external integration, or allow Dependabot `docker` update PRs. Digest pinning follows that choice.

### 3.9 Android compatibility and performance

- **Implemented:**
  - minSdk 26, compile and target SDK 36;
  - R8 and resource shrinking on release and staging;
  - `profileinstaller` present, but no baseline profile.
- **Automated tests:** the minified staging build and an "R8 actually minified" check.
- **Emulator:** in CI (`android-device-tests.yml`), all on the default emulator profile (320×640, 1536 MB):
  - API 35: 19/19 on #207's head `ad2e51a` (run 41291943846; merged as `3ef3b84`) and on `3e16208` (#209, run 41327024741);
  - API 28 (Android 9): 15/15 in 3 of 3 runs on `3e16208` (#209), and again in 3 of 3 runs on `eaf550b` (#211: runs 41332401211, 41332404100, 41332406795) after a second symptom of the same keyboard race was fixed (F13);
  - API 26 (Android 8.0, minSdk): 12/12 on `bba198d` (run 41290918130, the first run at minSdk), 15/15 on `3e16208` (run 41327021294) and 15/15 on `eaf550b` (run 41332409322).
  - **The R8-minified staging build** (#211): launched on API 35 (run 41332404494), API 28 ×3 and API 26 on `eaf550b`, alive after 15 s, no fatal exception, window on screen. A launch smoke check, not a test of features.
  - By hand earlier: API 35 and API 28.
- **Physical device:** none.
  - API 36 has never run on any device.
  - The minified build has run only as a launch smoke check on emulators (#211), never on a phone.
- **Staging and production:** none.
- **Blocker:**
  - X3 (a physical phone);
  - F10 (no baseline profile; generating one needs a device).
- **Next action:** routine: extend the minified check from launch to the main screens (onboarding, Protection lists) on the CI emulator.

### 3.10 Automated tests and failure recovery

- **Implemented:**
  - CI workflows for Go, Python, Android, contracts, security, real PostgreSQL and the Android live contract;
  - the required checks `ci-gate` and Build & Test (strict).
- **Automated tests:**
  - fail-closed tests: store outages, replay-store failure, unavailable dependencies, a stalled replay store (C19);
  - the race runs over the auth path (C20, #206, `bba198d`);
  - instrumented tests on an emulator (#206, `bba198d`).
- **Staging and production:** none.
- **Concurrency correctness** is covered:
  - **Refresh rotation:** concurrent refreshes rotate exactly once in the service (`app/refresh_race_test.go`, `app/refresh_proof_test.go`). On real PostgreSQL, rotation is a compare-and-swap that holds across two store instances (`pgstore/contract_test.go`, `ReplaceSessionIsACompareAndSwap`).
  - **OTP:** concurrent validation consumes a code once, and concurrent wrong guesses are all counted (`app/otp_race_test.go`).
  - **Real PostgreSQL:** races between erasure and sign-in (`pgstore/erasure_signin_race_test.go`), and housekeeping across replicas (`pgstore/housekeeping_test.go`).
- **Blocker:** there are no load, chaos or fault-injection tests for identity or edge-api. Load figures mean something only on representative hosts (X5); a shared CI runner would give numbers that measure the runner.
- **Next action:** after X5, a load and failover test against staging, with targets the owner sets (OD-10).

### 3.11 Play Store, privacy and release validation

- **Implemented:**
  - `release-android.yml` builds and checks a signed bundle in the `play-release` Environment, without uploading;
  - the same script runs in CI with a throwaway key;
  - `allowBackup="false"` and data-extraction rules (C21).
- **Automated tests:** the release script in CI with a throwaway key.
- **Staging and production:** **never run for real** (no secrets, X2).
- **Blocker:**
  - X1, X2 (the Play Console and the upload key);
  - C1/E10;
  - L1–L6: the privacy policy, Data safety and the other legal items, none written;
  - OD-8.
- **Next action:** the owner creates the Play Console account and upload key (X1, X2) and engages legal (L1–L6).

## 4. Findings from the audit

Defects and gaps found on 2026-11-01 that the inventory did not track. **Class** as in the inventory: R (routine, Claude Code may do it), O (owner decision), H (high-risk: owner and an independent human review).

| # | Finding | Evidence | Class | Status |
|---|---|---|---|---|
| F1 | **A refused refresh proof signs the user out, whatever the reason:** a wrong phone clock, a server `htu` misconfiguration, or another key. Identity itself ends nothing on a refused proof | [`F1-REFRESH-PROOF-REJECTION.md`](F1-REFRESH-PROOF-REJECTION.md) | **H** | **Proposed** (H7): A + B + E. Not implemented |
| F2 | **A Keystore failure while signing a proof crashed the app** | `IdentityClient.kt` | R | #204, `d6cfa8d` |
| F3 | **A corrupt session file is never recovered.** There is no `corruptionHandler`; outside the splash, enrolment would always fail to store | `core/datastore/.../AndroidSessionStorage.kt`; `EncryptedSessionStore.read` | O (it would delete the stored session) | Open (OD-13). The crashes it caused are guarded without deleting anything: the splash (#202) and the deletion screen (#207, `3ef3b84`) |
| F4 | **edge-api had no per-request deadline** | `packages/go/platform/server.go` | R | #205, `4a78f97` |
| F5 | **The repeated race runs excluded identity and edge-api** | `.github/workflows/hardening.yml` | R | #206, `bba198d` |
| F6 | **Instrumented tests never ran in CI** | `pr-android.yml` (compiles only) | R | #206, `bba198d` |
| F7 | **No `dataExtractionRules`:** on API 31+, `allowBackup="false"` does not stop device-to-device transfer | `AndroidManifest.xml` | R | #207, `3ef3b84` |
| F8 | **No sign-out action in the UI.** `IdentityAuthenticator.signOut()` has no production caller | `ANDROID-DATA-INVENTORY.md` | O (UX) | Open (OD-14) |
| F9 | **No vulnerability scan of the Android dependencies**, only Dependabot alerts | `.github/workflows/` | R | #206, `bba198d`: OSV-Scanner 2.7.0, 133 release libraries, no advisories |
| F10 | **No baseline profile**, although `profileinstaller` is a dependency | `android/settings.gradle.kts` | R | Open; needs a device |
| F11 | **Identity does not audit a refresh with the current token and another key's proof,** the signature of a stolen token used without the key | `acceptRefreshProof` (`identity.go`) | H (part of F1 option E) | Proposed with F1 |
| F12 | **identity started with no configuration reports ready.** With no `CS_IDENTITY_*` variable it boots "inactive": no application port, and `/readyz` answers **200** ("degraded"). It fails closed, since nothing is served, but a production pod that lost its configuration would pass its readiness probe. edge-api refuses to start instead. Shown by the image smoke check | `services/go/identity/cmd/server/identity.go` (`configureIdentity`, `errIdentityInactive`) | O (it changes identity's documented activation contract) | Open (OD-15). Option: when the platform's `Environment` setting (`packages/go/platform/config.go`; default `development`) is `staging` or `production`, an inactive identity refuses to start |
| F13 | **`ProtectionListsTest` failed intermittently on the API 28 emulator.** Cause, from device logs and full screen dumps: a **test race**. The field's keyboard can still be arriving after the test types, and `Espresso.closeSoftKeyboard()` does nothing until it is visible. Two symptoms: a Back that closed the late keyboard instead of navigating (3 of 12 runs before #209), and a tap computed before the keyboard panned the window (`ADJUST_PAN`), which landed on the sheet's scrim and dismissed it without moving the entry (run 41330429579, after #209). Not an app defect. Found by the CI emulator (F6) | `ProtectionListsTest`; runs listed in #209 and #211 | R | Fixed in the test: #209 settles the keyboard before Back; #211 settles it after every text entry. In #211's acceptance runs the settle step met the keyboard in flight after each entry and closed it first |
| F14 | **The four held Dependabot PRs (#182–#185) re-run full CI whenever Dependabot rebases them.** On 2026-11-01 at 15:35 all four were rebased; #184 and #185 alone queued about 186 `pr-go` jobs, and other CI waited behind them for the account's concurrent-job limit | `gh run list` on 2026-11-01; `.github/dependabot.yml` (`github-actions`, no `rebase-strategy`) | O (the PRs are owner-held) | Open (OD-18). Options: decide #182–#185, or set `rebase-strategy: disabled` for that ecosystem. Not changed; their runs were not cancelled |

### 4.1 Recommended answers to the owner decisions in this document

These are recommendations from an AI-performed review, offered so that each decision can be taken quickly. None is applied.

| Decision | Recommendation | Why | If accepted |
|---|---|---|---|
| **H7** (F1) | Options A + B + E of [`F1-REFRESH-PROOF-REJECTION.md`](F1-REFRESH-PROOF-REJECTION.md) | Identity revokes nothing on a refused proof, so a local sign-out protects nothing; clock skew is the likeliest cause | High-risk: also needs the independent human review (OD-12) |
| **OD-13** (F3) | Give the session DataStore a corruption handler that replaces an unreadable file with an empty one | The record in it cannot be decrypted anyway. The app already clears an unreadable **record** (`STORED_SESSION_UNRECOVERABLE`); this extends that to an unreadable **file**. The user signs in again | Routine once decided: one handler and a test with a corrupt file |
| **OD-14** (F8) | A local "Sign out" in Settings that calls `IdentityAuthenticator.signOut()` | It clears the stored session; access tokens expire within 15 minutes. **Caveat:** identity has no logout route, so the server session stays live until its 24-hour expiry, with its refresh token deleted from the phone | Routine for the local action. A server logout route is an identity API change, high-risk, and a separate decision |
| **OD-15** (F12) | When `Environment` is `staging` or `production`, an inactive identity refuses to start | Matches edge-api, and a misconfigured pod then fails its rollout instead of passing readiness | Routine once decided: a bootstrap check and a test |
| **OD-16** | Allow Dependabot `docker` update PRs, then pin digests | Dependabot is already installed and configured for these directories; Renovate would be a new external app | Routine once decided: a config change and a digest pin |

## 5. Critical path

1. **Internal testing (Play):**
   - E2 (SMS provider and DLT), then C2;
   - X5 with E3 and E4 (staging hosts, keys, database, pins);
   - X1 and X2 (the Play Console and the upload key);
   - OD-10 (what "done" means).

   All are owner or external items. Then T3 on a physical device (X3).
2. **Production:** everything above, plus:
   - C1/E10 (the web deletion page);
   - L1–L6 (privacy policy, Data safety, legal reviews);
   - E5 (an observability backend) and alerting;
   - T2 (carriers);
   - D1–D9;
   - the high-risk decisions H1–H6 and F1, which wait on OD-12 (an independent human reviewer).
3. **Routine work that can proceed now**, without owner input or paid services: the next actions marked "routine" in §3.

## 6. Product capabilities (owner direction, 2026-11-01)

These seven capabilities belong to the owner's **six-phase product roadmap**, within its existing phases; there is no seventh phase. That roadmap is not recorded in this repository, so this document does not restate, renumber or replace it, and places no capability in a phase. It records only what exists today and what each capability needs before it can be built, so that work starts when its prerequisites are ready. The six languages are a requirement: Hindi, English, Tamil, Telugu, Malayalam and Kannada.

Delivery stays Android-first, with platform boundaries kept compatible with a later iOS client: shared logic in plain Kotlin contracts, platform pieces behind interfaces ([`CROSS-PLATFORM-FRONTEND-ASSESSMENT.md`](../architecture/CROSS-PLATFORM-FRONTEND-ASSESSMENT.md)).

| Capability | What exists | Waits on |
|---|---|---|
| **Multilingual (app):** Hindi, English, Tamil, Telugu, Malayalam, Kannada | Only default strings (`res/values`), no translations. The release keeps the locales `en, hi, bn, ta, te, mr` (`AndroidApplicationConventionPlugin.kt`, `localeFilters`), so **Malayalam and Kannada strings would be stripped**, and Bengali and Marathi, not in the owner's list, are kept. Typography covers Indic scripts (`docs/design/03-typography.md`) | **OD-17** (the language set and `localeFilters`); translations, an L-item for legal text |
| **Multilingual (voice)** | ADR-0005 targets Indian English, Hindi and Hinglish, with Bengali, Tamil, Telugu and Marathi as expansion; Malayalam and Kannada are not named | **OD-17:** accepted ADR-0005 (Google Cloud STT v2, Deepgram, Sarvam) and ADR-0007 (ElevenLabs, Cartesia, Sarvam) choose **paid** speech vendors, while the owner's rule is free and open-source only. Verified free alternatives and options: [`SPEECH-PROVIDER-POLICY-CONFLICT.md`](SPEECH-PROVIDER-POLICY-CONFLICT.md) |
| **Post-call reports and the required Android screens** | UX specifies `A27` (post-call summary) and related screens (`docs/ux/android/03-screen-inventory.md`, `04-screens-screening-and-calls.md`). Today the app screens with an on-device prefilter only; no call reaches an agent | Calls through the voice pipeline |
| **Automated evaluation pipeline** | `packages/go/evaluation`, `evalstore`, `evalsubjects`, `docs/evaluation/` | Test recordings with consent; OD-17 option C |
| **Conversation outcome analytics** | `services/go/analytics`, `packages/go/evaluation` | Calls; I7 (identifiers, not content, on Kafka) |
| **Privacy-preserving memory** | `packages/go/memory`, `docs/memory/` (consent is structural, INV-MEM-2) | Consent records (OD-4); I2, I7, I8 |
| **Continuous learning and personalization** | No implementation | Memory, evaluation and consent above; ADR-0012 |
| **Safe model improvement** | `packages/go/governance` | The evaluation pipeline and memory; any training on subscriber data is a personal-data decision (high-risk, OD-12) |

None of these is on the critical path in §5, and none is started by this document.

## 7. Keeping this current

Update this file in the same PR as any change to a status above, as the inventory is. A status moves to "staging-verified" or "production-verified" only with a recorded run: date, commit and host.
