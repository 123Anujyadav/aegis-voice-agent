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
| Android 16 behavior changes | **VERIFIED** (no change needed) | Checked against [behavior-changes-16](https://developer.android.com/about/versions/16/behavior-changes-16): `MainActivity` already calls `enableEdgeToEdge()`; there is no `onBackPressed` or `KEYCODE_BACK`; there are no orientation or resizability locks; the manifest declares no permissions, services or receivers. |
| **Release workflow** | **EXTERNAL** (code done) | `.github/workflows/release-android.yml` builds and verifies a signed `.aab` (§6). It is tested through the same script on every Android PR, with a throwaway key. It needs the owner's upload key, the protected `play-release` Environment and its secrets. |
| Release signing and upload key | **EXTERNAL** | No release signing config exists anywhere, which is correct: there must be no key in Git. It needs an upload key held in CI secrets, with Play App Signing managed by Google. Secret names will be defined by the release workflow; none exist yet. |
| Version code and name | **VERIFIED** | #148 (squash `2ab335a`, head `2217a45`): `CS_VERSION_CODE` and `CS_VERSION_NAME` (non-secret environment variables) are validated by `ReleaseVersion` (`android/build-logic/convention/src/main/kotlin/ReleaseVersion.kt`). Unset gives the developer defaults. Set but invalid fails the build; before #148, an invalid code silently became versionCode 1. The code must be 1 to 2,100,000,000, Play's maximum ([versioning](https://developer.android.com/studio/publish/versioning)). The name must be 1–64 characters of `[0-9A-Za-z.+_-]`, starting with a letter or digit. CI job 120881371351: all 5 `ReleaseVersionTest` cases are listed PASSED; the JUnit report is required to contain at least one test; and `CS_VERSION_CODE=not-a-number` fails `:app:help` with the validation message. **Gap:** no test accepts an exactly-64-character name. No release has set either value yet. |
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
| App wiring (Play Integrity plus edge in the app graph) | **MERGED, NOT INTEGRATED** | #146 (squash `868f887`, head `819b97f`): one device key and one authenticator; strict https-origin and project-number parsing (`EndpointConfig`); no Play Integrity work and no token without a valid project. All values default to empty, so nothing is reachable until they are configured. |
| Identity and edge hosts | **EXTERNAL** | `callscreen.identityBaseUrl` and `callscreen.edgeApiBaseUrl` (non-secret) are unset, so sign-in and edge calls are unavailable. **No host is chosen.** |
| Play Integrity, end to end | **EXTERNAL** | Needs: the Cloud project linked in Play Console; `callscreen.playIntegrityCloudProjectNumber` (an identifier); a credential for identity's server verifier (`internal/playintegrity`, unwired); an app installed from Play. Default quota 10,000/day ([setup](https://developer.android.com/google/play/integrity/setup)); **the price is not stated there, so it is unverified.** |
| OTP delivery | **EXTERNAL** | No provider. India DLT registration is needed. Billed per message (cost unverified). |
| FCM | **EXTERNAL** | Interface only. A Firebase project is needed (cost unverified). |

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
| Identity and edge-api hosts | **External setup (owner)** | `callscreen.identityBaseUrl` and `callscreen.edgeApiBaseUrl` default to empty (`android/app/build.gradle.kts`). These are public configuration, not secrets. |
| Server-side Play Integrity verifier: integration into identity | **Owner decision**, then **external setup**, then **code** | The adapter `services/go/identity/internal/playintegrity/playintegrity.go` exists; its tests run only against a local fake of `decodeIntegrityToken` (`playintegrity_test.go`), and it has **never called Google**. It is **not wired** into identity's authentication flow: nothing outside the package imports it, and `SetIntegrityVerifier` (`internal/app/integrity.go`) is never called, so every session is recorded `unverified`. Wiring a real verifier would switch on ADR-0031 D31.3, which refuses a token Google rejects with 400 `ERR_INVALID_INTEGRITY_TOKEN`. ADR-0031 is **Proposed, owner ratification pending**, and the verdict thresholds (`IntegrityPolicy`) are security policy, so both are the owner's. Then external setup: the Google Cloud project linked in Play Console, and a credential for the verifier. The mechanism (workload identity federation or a service-account key) is undecided, and the credential would be a secret. |
| Play Integrity on the client: Cloud project number | **External setup (owner)** | `callscreen.playIntegrityCloudProjectNumber` (public) is unset, so the app requests no integrity token (#146). |
| OTP provider and India DLT | **External setup (owner)**, then **code** | `services/go/identity/internal/otpdelivery/otpdelivery.go`: "No SMS provider is implemented in this repository." |
| FCM | **External setup**, then **code** | No Firebase SDK in any `go.mod` or Android build file. `notification-fanout` is a Phase 2 shell (`cmd/server/main.go`). |
| Play Console account, upload key, privacy policy, Data safety form | **External setup (owner)** | Not represented in the repository. |
| Real-device Play Integrity, sign-in and calling | **Device and release-environment validation** | Never run on a device. It needs a Play-installed build and live hosts. |
| ADR-0029, ADR-0030 D30.3 (with amendments), ADR-0031; session permissions; SIM-swap policy and device-history retention; OTP backoff and session lifetime; the frontend stack | **Owner decision** | §3, §4 |
| Cryptographic review; privacy (DPDP) review | **Independent review** | §3 |

**Resolved since this table was first written:**

| Former blocker | Resolution | Evidence |
|---|---|---|
| No test accepted an exactly-64-character `CS_VERSION_NAME` | **Resolved** by #150 (squash `ee08c0a`, head `feb45d9`) | `nameLengthBoundsAreInclusive()` accepts names of exactly 1 and 64 characters, and the 0- and 65-character rejections are unchanged. The `pr-android` job 120907314040 log lists `ReleaseVersionTest > nameLengthBoundsAreInclusive() PASSED`. Post-merge `main` `ee08c0a`: 9/9 green. |
| Stale `android.suppressUnsupportedCompileSdk=35` in `android/gradle.properties` | **Resolved** by #151 (squash `0208a88`, head `e4f2c49`) | The block is removed, and nothing else references the property. The `pr-android` job 120915500024 log has no compileSdk warning, and `:app:minifyStagingWithR8` ran. |
