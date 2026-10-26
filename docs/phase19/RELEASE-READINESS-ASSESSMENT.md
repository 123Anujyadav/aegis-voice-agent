# Android / Google Play release-readiness assessment

- **As of:** 2026-10-24.
- **Evidence:** `main` at `2ab335a`, plus the PRs named below. Every claim
  cites a repository path, a commit or PR, CI evidence, or official
  documentation.
- **Not claimed: Google Play readiness.** This document lists what stands
  between the repository and a Play submission.

**Status key:**

| Status | Meaning |
|---|---|
| **VERIFIED** | Proven by repository and CI evidence |
| **MERGED, NOT INTEGRATED** | Code is on `main`, but no real external system has exercised it |
| **OPEN** | A PR or CI is pending |
| **DEFECT** | A code fix is needed |
| **EXTERNAL** | Blocked on setup outside the repository |
| **OWNER** | Needs an owner decision |
| **REVIEW** | An independent review is required |

## 1. Build and release configuration

| Item | Status | Evidence |
|---|---|---|
| Debug build and JVM unit tests in CI | **VERIFIED** | `pr-android` (#136). `assembleDebug` succeeds and `testDebugUnitTest` runs for every module (CI logs, #136–#144). |
| Minified (R8) build | **VERIFIED** | #145 (squash `33aa9c7`, head `f414802`) found that `staging` **had never been minified**: it was created before `release` set R8, so `initWith` copied the defaults. Fixed. CI (`pr-android`, job 120836170651) ran `:app:minifyStagingWithR8` (09:31:43–09:32:41Z), and the assertion that `app/build/outputs/mapping/staging/mapping.txt` exists and is non-empty passed. The step now runs on every Android PR. |
| Resource shrinking | **VERIFIED** (task evidence, AGP 8.9.1) | `staging` inherits `isShrinkResources = true` from `release` (`AndroidApplicationConventionPlugin.kt`). On AGP 8.9.1 (#147 job 120873962833, #148), CI runs `:app:minifyStagingWithR8` → `:app:convertShrunkResourcesToBinaryStaging` → `:app:optimizeStagingResources` → `:app:packageStaging`: R8 shrinks the resources, and the shrunk set is converted and packaged. **Limitation:** on AGP 8.7.3 (#145, #146) no shrink task appeared, so shrinking before #147 is unconfirmed. No artifact-level check (for example, comparing APK resource counts) exists. |
| `app/proguard-rules.pro` | **VERIFIED** | It was missing, although `release` names it. Added in #145 (empty, with an explanation): no keep rule was needed for the R8 build to pass. |
| **Target API 36** | **VERIFIED** (build) | Google Play: "New apps and app updates must target Android 16 (API level 36) or higher", effective 2026-08-31, with extensions to 2026-11-01 ([target-sdk requirements](https://developer.android.com/google/play/requirements/target-sdk), updated 2026-10-16). #147 (squash `84d6741`, head `0c6efa4`): AGP 8.7.3 → 8.9.1 (the minimum for API 36, per [about-agp](https://developer.android.com/build/releases/about-agp)); `compileSdk` and `targetSdk` 36; Gradle 8.12 and JDK 17 kept. AGP 8.9 deprecated `resourceConfigurations`, so it was migrated to `androidResources.localeFilters`, not suppressed. CI job 120873962833: all modules compiled, 24 unit-test tasks ran, `:app:minifyStagingWithR8` ran, and the mapping assertion passed. **Not run on a device.** |
| Android 16 behavior changes | **VERIFIED** (no change needed) | Checked against [behavior-changes-16](https://developer.android.com/about/versions/16/behavior-changes-16): `MainActivity` already calls `enableEdgeToEdge()`; there is no `onBackPressed` or `KEYCODE_BACK`; there are no orientation or resizability locks; the manifest declares no permissions or receivers, and one service: the call screening service (#169), which only the system can bind. |
| **Release workflow** | **EXTERNAL** (code done) | `.github/workflows/release-android.yml` builds and verifies a signed `.aab` (§6). It is tested through the same script on every Android PR, with a throwaway key. It needs the owner's upload key, the protected `play-release` Environment and its secrets. |
| Release signing and upload key | **EXTERNAL** | No release signing config exists anywhere, which is correct: there must be no key in Git. It needs an upload key held in CI secrets, with Play App Signing managed by Google. Secret names will be defined by the release workflow; none exist yet. |
| Version code and name | **VERIFIED** | #148 (squash `2ab335a`, head `2217a45`): `CS_VERSION_CODE` and `CS_VERSION_NAME` (non-secret environment variables) are validated by `ReleaseVersion` (`android/build-logic/convention/src/main/kotlin/ReleaseVersion.kt`). Unset gives the developer defaults. Set but invalid fails the build; before #148, an invalid code silently became versionCode 1. The code must be 1 to 2,100,000,000, Play's maximum ([versioning](https://developer.android.com/studio/publish/versioning)). The name must be 1–64 characters of `[0-9A-Za-z.+_-]`, starting with a letter or digit. CI job 120881371351: all 5 `ReleaseVersionTest` cases are listed PASSED; the JUnit report is required to contain at least one test; and `CS_VERSION_CODE=not-a-number` fails `:app:help` with the validation message. **Gap:** no test accepts an exactly-64-character name. No release has set either value yet. |
| Certificate pinning (ADR-0010, accepted) | **Code done; EXTERNAL** (the pins) | `CertificatePins` and `IdentityClient.defaultHttpClient(pins)` (core:network), wired through `EndpointConfig.httpClient` from `callscreen.identityCertPins` and `callscreen.edgeApiCertPins`. It is tested against a real TLS server: the pinned key connects; another CA-valid key is refused and nothing is sent. **No pins are set**, so the app is unpinned today. The pins need the production hosts' keys (the current one and a backup), which do not exist yet. **Correction (#180):** the app-module test of this wiring (`EndpointConfigTest`) never ran before #180. The application build convention did not enable JUnit 5, so the task passed with 0 tests. Run for the first time, its pin case failed on a non-canonical test fixture (the code was correct). The fixture is fixed, CI now fails any module whose tests do not run, and the core:network pinning tests always ran. |
| Backup | **VERIFIED** | `android:allowBackup="false"` in `app/src/main/AndroidManifest.xml`. |
| Cleartext traffic | **VERIFIED** (platform default) | There is no network-security config. Cleartext is blocked by default at this `targetSdk`, and the clients additionally refuse non-https origins (`EndpointConfig`, `IdentityClient`, `EdgeApiClient`). |

## 2. Identity on Android

| Item | Status | Evidence |
|---|---|---|
| Device-key proofs (DPoP) in the server's exact profile | **MERGED, NOT INTEGRATED** | #137 (`3dc06e5`). Its vectors are shared with the Go tests. **Contract-tested against the real identity service** in CI (`android-identity-contract`, `IdentityLiveContractTest`). That is identity run locally on throwaway PostgreSQL, **not a deployed environment and not a device**. |
| Identity HTTP client | **MERGED, NOT INTEGRATED** | #139 (`ed71365`), tested with MockWebServer, **and against the real identity service** in CI (`android-identity-contract`). The tests cover OTP request; a wrong code refused; enrolment with the access token's `cnf.jkt` equal to the device key; a refresh signed by another key refused without rotating; refresh rotation; and reuse of the rotated-away token ending the whole session. No deployed identity and no device are involved. |
| Encrypted session store (Keystore AES-GCM plus DataStore) | **MERGED, NOT INTEGRATED** | #140 (`4f0c67f`). `androidx.security-crypto` is avoided: it is deprecated per [the AndroidX releases page](https://developer.android.com/jetpack/androidx/releases/security). |
| Orchestration (single-flight refresh) | **MERGED, NOT INTEGRATED** | #141 (`da8edb8`): 8 concurrent callers make 1 refresh (test). |
| Phone-verification UI | **MERGED, NOT INTEGRATED** | #142 (`4a6bbe6`). The Hilt graph was validated in CI. |
| Edge-api client (`GET /v1/me`) | **MERGED, NOT INTEGRATED** | #144 (`2dfcdb6`), and **contract-tested against the real edge-api and identity services** in CI (`android-identity-contract`, `EdgeLiveContractTest`). The app's authenticator signs in, and edge-api admits the identity-issued token with this device's proof: `/v1/me` returns the token's `sub`, and `deviceKeyThumbprint` equals the device key. Another key's proof is refused (`ProofRejected`). Both services run locally behind loopback TLS: **not a deployed environment and not a device**. edge-api checks the token's signature and claims and the proof, plus the replay table, and looks up no session (`services/go/edge-api/internal/auth/auth.go`). So a revoked session's access token is still admitted until it expires (15 minutes, ADR-0029), and the test does not assert otherwise. |
| Play Integrity client | **MERGED, NOT INTEGRATED** | #143 (`7179f03`). **Never called Google.** |
| App wiring (Play Integrity plus edge in the app graph) | **MERGED, NOT INTEGRATED** | #146 (squash `868f887`, head `819b97f`): one device key and one authenticator; strict https-origin and project-number parsing (`EndpointConfig`); no Play Integrity work and no token without a valid project. All values default to empty, so nothing is reachable until they are configured. **Correction (#180):** `EndpointConfigTest`'s parsing cases did not run in CI until #180 (0 app-module tests were executed). They pass now. |
| Identity and edge hosts | **EXTERNAL** | `callscreen.identityBaseUrl` and `callscreen.edgeApiBaseUrl` (non-secret) are unset, so sign-in and edge calls are unavailable. **No host is chosen.** |
| Play Integrity, end to end | **EXTERNAL** | Needs: the Cloud project linked in Play Console; `callscreen.playIntegrityCloudProjectNumber` (an identifier); a credential for identity's server verifier (`internal/playintegrity`, unwired); an app installed from Play. Default quota 10,000/day ([setup](https://developer.android.com/google/play/integrity/setup)); **the price is not stated there, so it is unverified.** |
| OTP delivery | **EXTERNAL** | No provider. India DLT registration is needed. Billed per message (cost unverified). |
| FCM | **EXTERNAL** | Interface only. A Firebase project is needed (cost unverified). |

## 2a. The core function: on-device call screening (ADR-0002 Stage 1)

**Emulator validation (2026-10-25):** see `ANDROID-SCREENING-DEVICE-VALIDATION.md`. On Android 15's real Telecom stack (emulator, synthetic numbers), all 14 cases behaved as designed:
- blocked calls were rejected and logged by the screening service;
- allowed and unknown callers rang;
- blocks survived an app restart;
- a corrupted list was kept and callers were screened;
- role loss stopped blocking;
- a saved contact reached the service only with `READ_CONTACTS`;
- answers took 1–65 ms against Android's 5 s limit.

**Screens on a device (#181):** 11 instrumented tests pass on the Android 15 emulator, through real navigation and Hilt with a fixture list file. They found and fixed four UI defects: snackbars dismissed immediately (so no Undo or move statement), an unnamed Add button, the wrong move direction, and a missing allowlist caveat. Carrier behaviour is **not** verified (that document's §5 is the manual sheet). **Android 9 emulator:** the instrumented suite passes 8/8, the fallback notice shows, and a listed number rings with no screening-service bind, as the app states.

Found on 2026-10-25: **the app cannot screen a call yet.** `core:telephony`,
`core:permissions`, `core:notifications` and `core:database` contain no code,
and the manifest declares no `CallScreeningService`. ADR-0002 (accepted) makes
the on-device pre-filter Stage 1 of the product. Without it, a release is a
sign-in shell.

| Item | Status | Evidence |
|---|---|---|
| Pre-filter decision (`PreFilterDecisionService`, docs/domain/11-consumer.md §11.6) | **Code (#167); not on a device** | `core/telephony/.../prefilter/PreFilter.kt`. The order is: a number that cannot be normalised is screened; the allowlist rings through (P-CO-3); the blocklist rejects, even for a contact (§11.16, constraint 5); a contact rings through; anything else is screened. A failing lookup degrades to screening, never to rejection (§11.15). Covered by JVM tests in `PreFilterTest`. |
| Number normalisation (E.164) | **Code (#167)** | `PhoneNumbers.normalise` accepts `+`/`00` international forms and the Indian national forms. Anything uncertain (short codes including 112, local landlines without an area code) gives null, so it is screened and can never match a block. |
| Blocked caller: reject, not silence | **Derived from the platform; needs device and carrier validation** | Android: a silenced call "will, however, still be sent to the default dialer app if it is not blocked". It stays unanswered, so the carrier's forward-on-no-reply (ADR-0002 §5) would send a known-bad caller to paid screening. A rejected call is ended "as if the user had manually rejected it". Only no-reply forwarding is provisioned, so a rejected call is not forwarded, which is what ADR-0002 §5 requires ("no forwarding"). `SILENCE` is kept, but nothing produces it until a reputation mapping exists. *Source: AOSP `telecomm/java/android/telecom/CallScreeningService.java`, read 2026-10-25.* |
| Contacts and `READ_CONTACTS` | **Code (#175): opt-in, least privilege; needs privacy review** | Android passes a call from a saved contact to the screening service only if the app holds `READ_CONTACTS` (AOSP `CallScreeningService.onScreenCall`). So without it, a saved contact rings and a block on it has no effect. #175 declares the permission and requests it **only** from the blocklist notice's "Allow contacts access" button, which the subscriber taps to block saved contacts too. It is not requested in onboarding. Declining keeps everything else working: the notice then points to Settings. **The app never reads the contacts provider and never uploads contacts.** The permission only changes which calls Android passes to the service. A test fails if production code references `ContactsContract` or the contacts content URI, and the manifest test allows exactly this one permission. With access, a contact that is not blocked is allowed like any caller. **Privacy:** no contact data is read, so no `CONTACT_SYNC` consent (docs/domain/10-identity.md) is recorded. The only data seen is the caller's number, already processed under core screening. The independent DPDP review should confirm this. The privacy policy and Play's permission disclosure must name the permission and its purpose (owner). **Deviation from the UX ladder** (docs/ux/01 §1.5 #3, A09c/A10): those request contacts for "people you know ring through", which Android already does without the permission. #175 asks only where the permission does something. |
| Screening deadline | **Platform fact** | The service must respond within 5 seconds, and "the user's device will not begin ringing until the response is received". So the decision is local, with no network call (`EvaluatePreFilter`, §11.10). |
| Android 8 and 9 (no screening role) | **Code (#175): honest fallback; `minSdk` kept at 26** | `ROLE_CALL_SCREENING` exists from API 29, and before it Android binds the screening service only for the default dialer. Nothing in the repository makes API 26–28 incompatible, so `minSdk` stays at 26. On those versions: onboarding skips A09b (#170); the service allows every call (#169); and **the blocklist now says** "This version of Android doesn't let apps block calls, so numbers on this list aren't blocked on this phone", with no action. The same notice reports a declined or revoked role ("Blocking is off …", with "Turn on blocking"). `BlockingStatus` and each notice are tested. **Verified on an Android 9 emulator (#181)**: fallback notice shown, a listed number rings, service never bound. **Not verified on a physical Android 8/9 phone.** |
| Reputation input | **OWNER (mapping)** and **code (feed)**; safe default kept | No reputation feed reaches the device, and no band-to-decision mapping is specified (docs/domain/15-fraud-detection.md). Until both exist, nothing produces `SILENCE`, and an unknown, unparseable or unlookup-able number is `SCREEN`, never `REJECT`. `PreFilterTest` holds both (`noSilenceYet`, `unknownFormIsScreened`, `failingLookupsDegradeToScreen`). No reputation source is invented. |
| `CallScreeningService` and its manifest entry | **Code (#169); not on a device** | `core/telephony/.../screening/PreFilterScreeningService.kt`, declared in `core/telephony/src/main/AndroidManifest.xml`. The entry is exported only behind `BIND_SCREENING_SERVICE`, which only the system holds, and declares no user permission (a JVM test parses the manifest and enforces both). The service decides off the main thread and answers every incoming call; any failure answers "allow". Outgoing calls are not screened. The caller list is one `@Singleton` in no-backup app storage, shared with the screens that will edit it. **Inert until the role is held.** No screen asks for the role yet, but a user could grant it in Settings ("Caller ID & spam app"). With an empty caller list, every call is then allowed exactly as today. |
| Role request (`ROLE_CALL_SCREENING`, onboarding A09b) | **Code (#170); not on a device** | `feature/onboarding/.../ScreeningRoleScreen.kt`. It uses the four-block contract (`docs/ux/01-cross-surface-conventions.md` §1.5). The enables, never and if-declined lines are the UX spec's words, and `ScreeningRoleCopyTest` compares them with the spec's table. "Continue" opens the system role request; either answer, or "Not now", moves on. Navigation shows A09b only when the role can be requested and is not held (`ScreeningRole.status`), otherwise it skips it. The inaccurate `PermissionsEducationScreen` ("Call Log & Phone State", "Microphone & Audio") is **removed**. **Not done:** the "previously denied" past-tense variant, the A09 analytics events (no analytics pipeline exists), A09a (notifications), and F14 (detecting loss of the role, banner A75). |
| Blocklist and allowlist screens (A32, A33, A35) | **Code (#171); not on a device** | `feature/protection/.../CallerListScreen.kt` and `CallerListViewModel`, reached from Protection. They edit the same `CallerListStore` the screening service reads, so a block applies to the next call, offline (P-CO-2). A move between lists is stated in a snackbar (P-CO-1). A removal can be undone for 5 s and is restored exactly. Unreadable lists are shown as such and change only on the subscriber's "Start again". The blocklist's first-run empty state states the offline guarantee. Rows are announced digit by digit; remove targets are 48 dp. Each change is saved before it is shown (local, so not optimistic). **#174 adds** labels (the subscriber's own note, 40 characters of plain text, on this phone only; it moves with the number between lists; stored format 2, and format 1 is still read) and the A32d detail sheet. The sheet shows only stored facts (list, number, label, source, date), says what the list does, and offers edit label, move to the other list, and remove (which hands over to the list's 5-second undo). The allowlist states that an unanswered call may still be forwarded by the carrier. Rows and the sheet are tested on the JVM with Robolectric (`CallerListUiTest`). #173 fixed a crash from #171: `feature:protection` lacked the Hilt plugin, so the screen could not be created. **Not done:** the long-press sheet, the "cleared" empty tier, skeleton loading, the analytics events, and navigation to A26 (caller profile, which has no data yet). |
| Losing the role to another app (F14, banner A75) | **Code (#172); not on a device** | `ScreeningRoleWatch` (core:telephony) remembers that the app held the role. `CallsFeedRoute` checks on each foreground and after the role request returns. While another app holds the role, A20 shows the spec's banner ("Another app is now screening your calls. …", held to the spec by `ScreeningRoleBannerCopyTest`) with "Switch back", which reopens the system role request. It is not a full-screen block: forwarding is unaffected. A subscriber who never granted the role sees no banner. **Not done:** the `haptic.confirm` when the role is regained. |
| "Ring through" is not "never screened" | **Finding; needs server sync (code) and an owner decision** | `RING_THROUGH` (allowlist, contacts) and `SCREEN` give the same answer on the handset: the call rings. If it is **not answered**, the carrier's no-reply forwarding sends it to the platform either way. Then an allowed number or a contact is screened by the AI, unless the gateway knows the subscriber's lists. That needs the lists synced to the server (no endpoint exists), and the contacts case conflicts with "contacts are never uploaded" (docs/domain/11-consumer.md §11.16). The UX flow's "rings through (no screening)" holds only while the subscriber answers. The screens therefore promise only what the handset guarantees. |
| Caller list storage (block and allow, P-CO-1/P-CO-2, INV-CO-1) | **Code (#168); not on a device** | `core/telephony/.../callerlist/`. `CallerList` is the aggregate: a number is on at most one list, a move is one change and is stated, and the subscriber's own number cannot be blocked. Short codes, including emergency numbers, cannot be listed. `CallerListStore` is the pre-filter's lookup. A change is saved (atomic replace, fsync) before the pre-filter sees it, with no network. A stored list that cannot be read is **kept untouched**: meanwhile it matches nothing, so callers are screened, not rejected, and changes are refused until the subscriber chooses to discard it. Bounded at 10,000 entries, so the first read fits the 5-second deadline (an engineering bound). Stored in app-private storage, not encrypted at the application level: no spec requires it, and `allowBackup` is false. **Not done:** sync to the server (no endpoint exists) and `lastAttemptAt`. |
| Call forwarding provisioning (`**61*<DID>**<seconds>#`, verified with `*#61#`) | **Code, after a DID pool exists (external)** | ADR-0002 §5. Needs platform DIDs and the carrier matrix (`docs/runbooks/carrier-matrix.md`, a launch blocker per ADR-0002). |
| UX flow F2 diagram | **Documentation finding** | `docs/ux/android/08-user-flows.md` draws the pre-filter after "not answered". On Android the pre-filter runs before the phone rings; carrier forwarding happens after. |

### 2b. Recommendation: the forwarding gap for allowed numbers and contacts (not implemented)

**The gap (§2a):** an allowed number or a contact that rings unanswered is forwarded by the carrier (CFNRy) and screened
by the AI. The handset cannot prevent it: CFNRy is network-side, and the pre-filter's only levers are allow, silence and
reject.

**Existing commitments:**
- "Contacts are read on-device and never uploaded" (docs/domain/11-consumer.md §11.16, constraint 1).
- Blocks are enforced on-device (constraint 5).
- Sync is "eventual" (P-CO-2).
- ADR-0012 §5.1 lists the recorded consents. There is no consent for storing the subscriber's lists on the server, and no
  retention entry for them.

**Options:**
1. **Keep the lists local (current).** It is honest if the UI says so. #174 states the caveat on the allowlist and in
   every allowed entry.
2. **Sync only the allowlist, as subscriber-scoped keyed hashes, for the gateway to check on a forwarded call.**
   - Never the blocklist: blocked calls are rejected on the handset and never forwarded, so the server gains nothing.
   - Never contacts: constraint 1.
   - Needs an ADR-0012 amendment: a purpose, a recorded consent, and retention for the life of the account. It also needs an
     edge-api endpoint and gateway support, plus an owner decision on what the gateway then does with an allowed caller.
     The gateway cannot hand the call back to the handset.
3. **Ask the handset in real time when a forwarded call arrives.** It needs FCM (not set up) and a reply within the
   ring-to-answer budget (ADR-0011). It fails whenever the phone is offline.

**Recommendation:** keep option 1 now. If the owner wants allowed callers to bypass the AI, option 2 for the allowlist
only, after the ADR-0012 amendment. Contacts stay excluded either way. Nothing server-side is built until then.

## 3. Security, operations and reviews

| Item | Status | Evidence |
|---|---|---|
| Restricted or unverified session permissions | **OWNER** | ADR-0031 D31.5: the tier is recorded, not enforced. |
| ADR-0029, ADR-0030 D30.3 (with amendments), ADR-0031 ratification | **OWNER** | The briefs are in `docs/phase18/` and `docs/phase19/`. |
| SIM-swap policy and device-history retention; OTP backoff and session lifetime | **OWNER** | `docs/phase19/SIM-SWAP-CONTROLS-REVIEW.md`, ratification brief §3a |
| Signing-key provisioning and the staging rotation exercise | **EXTERNAL** (SRE) | The runbook (`docs/runbooks/access-token-signing-key-rotation.md`) has never been exercised. |
| Point-in-time database recovery | **EXTERNAL** (SRE) | Required before ADR-0030 stage 3. |
| Tracing (X-Ray) P1 and P2 checks | **EXTERNAL** (SRE) | ADR-0028 |
| Load-balancer proxy topology (per-source rate limiting) | **EXTERNAL** (SRE) | No `X-Forwarded-For` header is trusted. |
| Independent cryptographic review | **REVIEW** | `docs/phase19/ADR-0029-DEVIATION-ANALYSIS.md`: "No external cryptographic review" |
| Privacy review (DPDP) | **REVIEW** | Retention R, and audit attribution |
| Play Console listing, Data safety form, privacy policy | **EXTERNAL** (owner) | Not represented in the repository |

## 4. Cross-platform

Recommendation: Kotlin Multiplatform with Compose Multiplatform, after the
Play launch. See
[CROSS-PLATFORM-FRONTEND-ASSESSMENT.md](../architecture/CROSS-PLATFORM-FRONTEND-ASSESSMENT.md).
The decision is the owner's.

## 5. Shortest path to a first Play internal-testing release

1. #145 (R8), #146 (app wiring), #147 (API 36) and #148 (version validation) are merged.
2. **Owner/SRE:** choose the identity and edge hosts, and deploy staging.
3. **Owner:** set up the Play Console account and app, the upload key (held
   as a CI secret) and Play App Signing.
4. **Engineering:** a release workflow: `bundleRelease`, signed with the
   upload key from CI secrets, with `CS_VERSION_CODE`/`CS_VERSION_NAME`
   supplied.
5. **Owner:** link the Play Integrity Cloud project, and choose the OTP
   provider and complete DLT. Without an OTP provider, nobody can enrol.
6. **Engineering:** the call-screening service, role request and caller-list
   storage (§2a). Internal testing can start without it, but a production
   listing would ship an app that cannot screen calls.

## 6. Release workflow and signing: implemented, awaiting the owner's key

**Status:** implemented as `.github/workflows/release-android.yml`.
- **What it is:** manual (`workflow_dispatch`), in the `play-release` GitHub Environment.
- **What it does:** builds `:app:bundleRelease`, proves that the signer is the upload key's certificate, and keeps the `.aab` and its R8 mapping as artifacts. **It does not upload to Play.**
- **How it is tested:** its build and signature check are `tools/scripts/android-release-bundle.sh`, which `pr-android` runs on every Android PR with a throwaway key. So the path is tested, except for where the secrets come from.
- **What it needs from the owner:**
  1. an upload key (generated offline);
  2. the `play-release` Environment's protection (required reviewers);
  3. the four secrets below.
- **Until then:** it fails at its first check, naming the missing secrets, and builds nothing.

**How the keys relate** ([Play App Signing](https://support.google.com/googleplay/android-developer/answer/9842756)):
- The **app signing key** is held by Google. It signs the APKs delivered to users, and new apps are enrolled by default.
- The **upload key** is held by us. It signs the bundle we upload, and Google uses it to verify the uploader.
- Google recommends the two keys be different.
- A lost or compromised upload key can be reset from Play Console with a new certificate. Losing it does not lock the app.

**The workflow** (`release-android.yml`, as implemented):
1. **Trigger:** manual `workflow_dispatch` only, in the `play-release` GitHub Environment. The owner adds its protection (required reviewers) **before** adding the key.
2. **Inputs:**
   - `CS_VERSION_CODE`: 1–2,100,000,000, strictly greater than the last uploaded code;
   - `CS_VERSION_NAME`: for example `1.0.0`.
   - Both are non-secret. The build rejects invalid values.
3. **Build:**
   - `tools/scripts/android-release-bundle.sh` decodes the upload keystore from an Environment secret into a private (mode 700) temporary directory;
   - run `bundleRelease` (R8, as staging already proves) with the upload-key signing config. **Implemented:** the convention plugin creates it **only** when all four `CS_UPLOAD_*` variables are set (`ReleaseSigning`), fails on a partial set, and leaves release unsigned when none is set.
     `pr-android` proves it on every Android PR: it builds a signed `bundleRelease` with a throwaway key generated and deleted inside the job, and checks that the bundle's signer certificate is that key's.
   - the keystore is deleted on exit, whatever happens (a `trap`), and the script fails unless the bundle's signer is the upload key's certificate.
4. **Output:** a signed `.aab` workflow artifact, uploaded to Play's internal-testing track **by the owner**. Automated upload through the Play Developer API would need a service-account credential, and it is deferred until the owner authorises it.

**Secret names**, read by `release-android.yml`. They are **not created**: the owner adds them to the `play-release` Environment, never to Git or to chat:

| Name | Secret? | Purpose |
|---|---|---|
| `ANDROID_UPLOAD_KEYSTORE_BASE64` | secret | The upload keystore, base64 |
| `ANDROID_UPLOAD_KEYSTORE_PASSWORD` | secret | The keystore password |
| `ANDROID_UPLOAD_KEY_ALIAS` | not sensitive, kept beside the others | The key alias |
| `ANDROID_UPLOAD_KEY_PASSWORD` | secret | The key password |

**Prerequisites (owner):**
- a Play Console developer account (the registration fee is **not verified here**);
- the app created in Play Console;
- an upload key generated by the owner offline with `keytool` and stored as the secrets above. **No key is ever generated or committed by automation.**

## 7. Remaining blockers by kind

Re-verified against the repository on 2026-10-24, last at `main` `0208a88`.

| Blocker | Kind | Repository evidence |
|---|---|---|
| Release workflow (`bundleRelease`, upload-key signing) | **External setup (owner)**; the code is done | `release-android.yml` plus `tools/scripts/android-release-bundle.sh` (§6). Remaining: the upload key, the `play-release` Environment's protection, and its four secrets. Then a first manual run, and the owner's upload of the `.aab` to internal testing. |
| Certificate pins for the identity and edge-api hosts | **External setup (owner or SRE)**, then configuration | Two `sha256/` SPKI pins per host (the current key and a backup) in `callscreen.identityCertPins` and `callscreen.edgeApiCertPins`. The code is done; the pins wait for the hosts' keys. |
| Identity and edge-api hosts | **External setup (owner)** | `callscreen.identityBaseUrl` and `callscreen.edgeApiBaseUrl` default to empty (`android/app/build.gradle.kts`). These are public configuration, not secrets. |
| Server-side Play Integrity verifier: integration into identity | **Owner decision**, then **external setup**, then **code** | The adapter `services/go/identity/internal/playintegrity/playintegrity.go` exists; its tests run only against a local fake of `decodeIntegrityToken` (`playintegrity_test.go`), and it has **never called Google**. It is **not wired** into identity's authentication flow: nothing outside the package imports it, and `SetIntegrityVerifier` (`internal/app/integrity.go`) is never called, so every session is recorded `unverified`. Wiring a real verifier would switch on ADR-0031 D31.3, which refuses a token Google rejects with 400 `ERR_INVALID_INTEGRITY_TOKEN`. ADR-0031 is **Proposed, owner ratification pending**, and the verdict thresholds (`IntegrityPolicy`) are security policy, so both are the owner's. Then external setup: the Google Cloud project linked in Play Console, and a credential for the verifier. Workload identity federation is implemented and dormant (#164): the code refuses service-account keys (ADR-0008, no static cloud credentials). Its approval (memo §12 B1) and the federation setup are the owner's and SRE's. |
| Play Integrity on the client: Cloud project number | **External setup (owner)** | `callscreen.playIntegrityCloudProjectNumber` (public) is unset, so the app requests no integrity token (#146). |
| OTP provider and India DLT | **External setup (owner)**, then **code** | `services/go/identity/internal/otpdelivery/otpdelivery.go`: "No SMS provider is implemented in this repository." |
| FCM | **External setup**, then **code** | No Firebase SDK in any `go.mod` or Android build file. `notification-fanout` is a Phase 2 shell (`cmd/server/main.go`). |
| **Call screening (the core function)** | **Code** (in progress; #167–#175), then **device and carrier validation** | §2a. The pre-filter decision (#167), caller-list storage (#168), the `CallScreeningService` (#169) and the role request (A09b, #170) and the blocklist and allowlist screens (#171) are implemented and tested. Role-loss detection (F14) is also done (#172). List sync and forwarding provisioning are not. An unanswered allowed or contact call is still forwarded to screening (§2a). Forwarding needs a DID pool (external). |
| Placeholder data shown as fact | **Resolved: onboarding, Protection and the calls feed (#179); the remaining prototype screens (#186)** | Every displayed value was traced to its source. **#179:** SIM shows what `TelephonyManager` reports (no permission) or "not available", with no forwarding claim; forwarding (A07) is "Not available yet" (the old `*71` code was a US code with an invented number, and the button dialled nothing); setup complete says whether blocking is actually on; Welcome claims only on-device blocking and conditional assistant screening; personas say they are not saved and never skip the announcement; Protection shows no score, counts or threats; the feed says "No call data yet". **#186** (`PROTOTYPE-SCREENS-AUDIT.md`): the four screens a release build can reach (Assistant home, voice setup, voice session, Settings) are honest, and Settings is a real setup checklist. The 16 unreachable prototype screens are debug-only design previews under a visible label, and a release graph does not register them (tested). **Tests:** Robolectric UI tests per screen, and `ReleaseCopyGuardTest` fails if a removed value or claim returns. **Wording assumption** (owner may revise): honest empty or unavailable states, not hidden sections. |
| Play Console account, upload key, privacy policy, Data safety form | **External setup (owner)** | Not represented in the repository. The policy and the form must cover the one runtime permission, `READ_CONTACTS` (#175). It is used only so Android passes calls from saved contacts to the screening service, at the subscriber's request. Contacts are never read or uploaded. **Inputs prepared:** `ANDROID-DATA-INVENTORY.md` (#187) traces every permission, data flow, destination and retention rule, and lists the questions to answer before submitting. |
| Account deletion (Play policy for apps with accounts) | **In-app: code (#188 backend, #189 Android), security-reviewed (#191). Web: owner decision and external setup** | `ACCOUNT-DELETION.md`. Identity `POST /v1/account/delete` is authenticated exactly like a proof-bound refresh, re-checked inside the erasure transaction. It erases every session, challenge, device attestation and delegation of the subscriber in one transaction (rollback tested on PostgreSQL), and keeps a LEGAL_HOLD erasure record and the audit log, as the domain and ADR-0012 require. Access tokens already issued are **not** revoked: edge-api admits them until expiry, ≤ 15 min (asserted end to end). **#191 fixed:** OkHttp resending single-use proofs; duplicate erasure records under concurrency; app messages claiming "nothing was deleted". **Remaining:** the web deletion page (an implementation-ready plan in §3 there; needs SMS/DLT and hosting), and decisions D1–D8 (§5 there; O9–O16). |
| Real-device Play Integrity, sign-in and calling | **Device and release-environment validation** | Never run on a device. It needs a Play-installed build and live hosts. |
| ADR-0029, ADR-0030 D30.3 (with amendments), ADR-0031; session permissions; SIM-swap policy and device-history retention; OTP backoff and session lifetime; the frontend stack | **Owner decision** | §3, §4 |
| Cryptographic review; privacy (DPDP) review | **Independent review** | §3 |

**Resolved since this table was first written:**

| Former blocker | Resolution | Evidence |
|---|---|---|
| The release app lacked `android.permission.INTERNET`, so every server call failed on a device (`socket failed: EPERM`) | **Resolved** by #187 | `core:network` declares it. `NetworkPermissionTest` failed on the Android 15 emulator without it and passes with it. CI now pins the release permission set to `android/app/release-permissions.txt`. |
| No test accepted an exactly-64-character `CS_VERSION_NAME` | **Resolved** by #150 (squash `ee08c0a`, head `feb45d9`) | `nameLengthBoundsAreInclusive()` accepts names of exactly 1 and 64 characters, and the 0- and 65-character rejections are unchanged. The `pr-android` job 120907314040 log lists `ReleaseVersionTest > nameLengthBoundsAreInclusive() PASSED`. Post-merge `main` `ee08c0a`: 9/9 green. |
| Stale `android.suppressUnsupportedCompileSdk=35` in `android/gradle.properties` | **Resolved** by #151 (squash `0208a88`, head `e4f2c49`) | The block is removed, and nothing else references the property. The `pr-android` job 120915500024 log has no compileSdk warning, and `:app:minifyStagingWithR8` ran. |

## 8. Google Play launch checklist (as of 2026-10-26, `main` `ff96fea`)

**Not claimed: Play readiness.** Each line is open unless it says done, and each cites its evidence. Nothing has been submitted to Play Console and nothing has been published.

### 8.1 Code and automated tests

| Item | State | Evidence |
|---|---|---|
| Target API 36; R8 release and staging builds; upload-key signing; version validation | **Done** | #145–#148, #154 (§1, §6) |
| `INTERNET` declared; release permission set pinned in CI | **Done** | #187; `android/app/release-permissions.txt` |
| In-app account deletion (server and app) | **Done**; security-reviewed, with three defects fixed (#191) | `ACCOUNT-DELETION.md` §2, §2a, §4 |
| Web deletion server flow | **Done, dormant (#193):** endpoints, migration `0009`, and tests including end to end on PostgreSQL. Answers 503 until a deletion-code sender and a budget are configured; production has neither. | `ACCOUNT-DELETION.md` §3.0 |
| Web deletion page | **Blocked:** no web stack or static hosting exists (`web/` is reserved for the admin console; the frontend stack is an owner decision), plus E10 | `ACCOUNT-DELETION.md` §3.0 |
| Device key destroyed after a confirmed deletion (R3) | **Done (#192)**, verified in real Keystore on the Android 15 emulator | `ACCOUNT-DELETION.md` §2a F5 |
| A sign-in in flight cannot outlive a deletion (R5) | **Done (#192)**, with PostgreSQL concurrency tests | `ACCOUNT-DELETION.md` §2a F4 |
| Server-side access-token revocation after deletion | **Not implemented; owner decision D9** (ADR-0010 amendment). The token is unusable by the app once its key is destroyed; it is not revoked by the server (≤ 15 min). | `ACCOUNT-DELETION.md` §5 D9 |
| Attestation housekeeping (D6) | **Open**: after O14, before E1 | §2a R4 |
| Call screening: list sync and forwarding provisioning | **Open** | §2a of this document |
| A reviewer path through sign-in (Play's "App access" needs a working login for review) | **Open: owner decision.** Sign-in is SMS-only. There is no test-number bypass, and adding one is a security decision (it must not work in production for anyone else). | `PhoneVerificationViewModel`; no bypass exists |

### 8.2 Owner decisions and privacy/legal review

| Item | Evidence |
|---|---|
| Account deletion D1–D9 (O9–O17): re-registration, erasure-record and audit retention (**privacy/legal**), step-up, web approach, attestation retention, grace period, the A53d confirmation | `ACCOUNT-DELETION.md` §5 |
| DPDP privacy review (E9), including the data inventory | `ANDROID-DATA-INVENTORY.md` §6 Q8 |
| The privacy policy text | `ANDROID-DATA-INVENTORY.md` §6 Q6 |
| ADR-0029, ADR-0030 D30.3, ADR-0031; session permissions; SIM-swap policy; OTP backoff and session lifetime | §7, `EXTERNAL-DEPENDENCIES.md` O1–O8 |
| Cryptographic review or penetration test (E8) | §3 |

### 8.3 External services, credentials and hosting

| Item | Evidence |
|---|---|
| SMS/OTP provider and India DLT, **including a deletion template** (E2). Nobody can enrol without it. | `otpdelivery.go`; `ACCOUNT-DELETION.md` §3.5 |
| Identity and edge-api hosts, deployed with migrations through `0008`; certificate pins | §7 |
| A domain and hosting for the web deletion page, and the privacy policy URL (E10) | `ACCOUNT-DELETION.md` §3.3 |
| Play Integrity: the Cloud project number (client) and the verifier's credential (server) | §7 |
| FCM (only if notifications ship) | §7 |

### 8.4 Physical-device and carrier validation

| Item | Evidence |
|---|---|
| **No physical device has run the app.** The emulator covered screening (14 cases), API 35 (12 instrumented tests) and API 28 (8). | `ANDROID-SCREENING-DEVICE-VALIDATION.md` |
| Sign-in, refresh and account deletion on a device against live hosts | Not run: no hosts. CI proves the client against the real binaries (`android-identity-contract`). |
| Carrier behaviour (forwarding, Indian number presentation) | Not run. The emulator region was not India (validation doc). |
| Play Integrity verdicts from a Play-installed build | Not run |

### 8.5 Play Console declarations and release operations

| Item | Evidence or input |
|---|---|
| Developer account, app entry, Play App Signing, upload key into the `play-release` Environment | §6 |
| Data safety form | Inputs: `ANDROID-DATA-INVENTORY.md` §6 Q2–Q5 (owner confirms) |
| Delete-account URL | Needs E10 |
| Privacy policy URL | Needs the policy (8.2) and hosting |
| App access instructions for review | Needs the reviewer path (8.1) |
| Content rating, target audience, ads declaration (no ads SDK: inventory) | The owner completes them in Play Console |
| Whether `READ_CONTACTS` or the call-screening role needs a permissions declaration | **Not verified here**; check Play's policy pages at submission |
| First internal-testing release: a manual `release-android.yml` run, then the owner uploads | §6 |
