# Android / Google Play release-readiness assessment

- **As of:** 2026-10-24.
- **Evidence:** `main` at `84d6741`, plus the PRs named below. Every claim
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
| `app/proguard-rules.pro` | **VERIFIED** | It was missing, although `release` names it. Added in #145 (empty, with an explanation): no keep rule was needed for the R8 build to pass. |
| **Target API 36** | **VERIFIED** (build) | Google Play: "New apps and app updates must target Android 16 (API level 36) or higher", effective 2026-08-31, with extensions to 2026-11-01 ([target-sdk requirements](https://developer.android.com/google/play/requirements/target-sdk), updated 2026-10-16). #147 (squash `84d6741`, head `0c6efa4`): AGP 8.7.3 → 8.9.1 (the minimum for API 36, per [about-agp](https://developer.android.com/build/releases/about-agp)); `compileSdk` and `targetSdk` 36; Gradle 8.12 and JDK 17 kept. AGP 8.9 deprecated `resourceConfigurations`, so it was migrated to `androidResources.localeFilters`, not suppressed. CI job 120873962833: all modules compiled, 24 unit-test tasks ran, `:app:minifyStagingWithR8` ran, and the mapping assertion passed. **Not run on a device.** |
| Android 16 behavior changes | **VERIFIED** (no change needed) | Checked against [behavior-changes-16](https://developer.android.com/about/versions/16/behavior-changes-16): `MainActivity` already calls `enableEdgeToEdge()`; there is no `onBackPressed` or `KEYCODE_BACK`; there are no orientation or resizability locks; the manifest declares no permissions, services or receivers. |
| **Release workflow** | **EXTERNAL / DEFECT** | **None exists** (`.github/workflows/`), although the convention plugin says release signing "is injected by the release workflow". |
| Release signing and upload key | **EXTERNAL** | No release signing config exists anywhere, which is correct: there must be no key in Git. It needs an upload key held in CI secrets, with Play App Signing managed by Google. Secret names will be defined by the release workflow; none exist yet. |
| Version code and name | **DEFECT** (fix OPEN in #148) | `CS_VERSION_CODE` and `CS_VERSION_NAME` (non-secret environment variables) are read by `AndroidApplicationConventionPlugin.kt`. On `main`, a set-but-invalid `CS_VERSION_CODE` **silently becomes versionCode 1**. The fix, #148 (**OPEN**), fails the build instead and bounds the code to 1–2,100,000,000, Play's maximum ([versioning](https://developer.android.com/studio/publish/versioning)). No release has set either value yet. |
| Backup | **VERIFIED** | `android:allowBackup="false"` in `app/src/main/AndroidManifest.xml`. |
| Cleartext traffic | **VERIFIED** (platform default) | There is no network-security config. Cleartext is blocked by default at this `targetSdk`, and the clients additionally refuse non-https origins (`EndpointConfig`, `IdentityClient`, `EdgeApiClient`). |

## 2. Identity on Android

| Item | Status | Evidence |
|---|---|---|
| Device-key proofs (DPoP) in the server's exact profile | **MERGED, NOT INTEGRATED** | #137 (`3dc06e5`). Its vectors are shared with the Go tests. Never run against a live identity. |
| Identity HTTP client | **MERGED, NOT INTEGRATED** | #139 (`ed71365`), tested with MockWebServer. |
| Encrypted session store (Keystore AES-GCM plus DataStore) | **MERGED, NOT INTEGRATED** | #140 (`4f0c67f`). `androidx.security-crypto` is avoided: it is deprecated per [the AndroidX releases page](https://developer.android.com/jetpack/androidx/releases/security). |
| Orchestration (single-flight refresh) | **MERGED, NOT INTEGRATED** | #141 (`da8edb8`): 8 concurrent callers make 1 refresh (test). |
| Phone-verification UI | **MERGED, NOT INTEGRATED** | #142 (`4a6bbe6`). The Hilt graph was validated in CI. |
| Edge-api client (`GET /v1/me`) | **MERGED, NOT INTEGRATED** | #144 (`2dfcdb6`). |
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

1. #145 (R8), #146 (app wiring) and #147 (API 36) are merged. #148 (version validation) is next.
2. **Owner/SRE:** choose the identity and edge hosts, and deploy staging.
3. **Owner:** set up the Play Console account and app, the upload key (held
   as a CI secret) and Play App Signing.
4. **Engineering:** a release workflow: `bundleRelease`, signed with the
   upload key from CI secrets, with `CS_VERSION_CODE`/`CS_VERSION_NAME`
   supplied.
5. **Owner:** link the Play Integrity Cloud project, and choose the OTP
   provider and complete DLT. Without an OTP provider, nobody can enrol.

## 6. Release workflow and signing: proposed design (not implemented)

**Why it is not implemented yet:**
- The signing path cannot be exercised without an upload key, and none exists.
- A workflow nobody can run would be unverified code that only looks finished.
- The smallest safe step taken so far is version validation: an invalid version now fails the build (section 1).

**How the keys relate** ([Play App Signing](https://support.google.com/googleplay/android-developer/answer/9842756)):
- The **app signing key** is held by Google. It signs the APKs delivered to users, and new apps are enrolled by default.
- The **upload key** is held by us. It signs the bundle we upload, and Google uses it to verify the uploader.
- Google recommends the two keys be different.
- A lost or compromised upload key can be reset from Play Console with a new certificate. Losing it does not lock the app.

**Proposed workflow** (`release-android.yml`):
1. **Trigger:** manual `workflow_dispatch` only, gated by a protected GitHub Environment with required reviewers. The owner configures the environment.
2. **Inputs:**
   - `CS_VERSION_CODE`: 1–2,100,000,000, strictly greater than the last uploaded code;
   - `CS_VERSION_NAME`: for example `1.0.0`.
   - Both are non-secret. The build rejects invalid values.
3. **Build:**
   - decode the upload keystore from an Environment secret into `$RUNNER_TEMP`;
   - run `bundleRelease` (R8, as staging already proves) with a signing config that the convention plugin creates **only** when the signing variables are present;
   - delete the keystore in an `always()` step.
4. **Output:** a signed `.aab` workflow artifact, uploaded to Play's internal-testing track **by the owner**. Automated upload through the Play Developer API would need a service-account credential, and it is deferred until the owner authorises it.

**Proposed secret names.** They are not created. They become final only when the workflow is written and run, and they would be GitHub **Environment** secrets, never in Git:

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
