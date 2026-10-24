# Cross-platform frontend: assessment and recommendation

- **Status:** **a recommendation, not a decision.**
  - The owner's requirement is one shared Android/iOS frontend codebase, with
    Android and Google Play launching first.
  - Nothing here migrates code, adds iOS work, or delays the Android release.
- **Evidence:** the repository at `main` `2ab335a` (2026-10-24). Module counts
  are `find`/`grep` over `android/**/src/main`.

## 1. What exists today

The app is **Kotlin with Jetpack Compose**, Hilt and Gradle, and has 67 Kotlin
source files across 17 modules, excluding the Gradle convention plugins. **It is not a shared iOS UI.** The Go
backend's HTTP contracts (identity, edge-api) are the only interface the
frontend depends on.

| Layer | Modules (main source files) | Platform coupling today |
|---|---|---|
| UI | `core:ui` (12), `core:designsystem` (8), nine `feature:*` modules (31) | Jetpack Compose and Material 3. Compose Multiplatform keeps the **same `androidx.compose.*` packages**, so most of this code can be shared. Android-only parts: `@Preview`, `androidx.activity`, Hilt ViewModels. |
| Identity client contract | `core:network` (2) | `IdentityContract.kt` is plain Kotlin, so it can be shared. `IdentityClient` uses **OkHttp**, which is JVM-only. |
| Orchestration | `core:auth` (2) | `IdentityAuthenticator` logic is plain Kotlin with coroutines, so it can be shared. `EdgeApiClient` uses OkHttp. |
| Device proofs | `core:security` (4) | The proof format, DER-to-raw conversion and thumbprint logic use `java.security` and `java.util.Base64`, which are JVM-only. `KeystoreProofKey` and Play Integrity are **Android-only by nature**. |
| Session storage | `core:datastore` (2) | The sealing format and recovery rules use `javax.crypto`, which is JVM-only. The Keystore key and DataStore are Android-only. |
| DI and app shell | `app` (5) | **Hilt is Android-only.** |
| Calling | ADR-0002: carrier-side screening via conditional call forwarding, plus an on-device pre-filter | Carrier-side forwarding is platform-neutral. The **on-device pre-filter is Android-only** (Android's call-screening role). No ADR mentions iOS. |

The recent identity work already separated the shared logic from the
platform seams on purpose:
- `DeviceProofs`, `AccessTokenProofs` and `IntegrityTokens` (network, auth);
- `ProofKey` and `IntegritySource` (security);
- `SessionKey` and `BlobStore` (datastore).

That is the shape a multiplatform split needs.

## 2. The two candidates

| | **Kotlin Multiplatform + Compose Multiplatform** (KMP/CMP) | **React Native + Expo + TypeScript** |
|---|---|---|
| Reuse of today's frontend | **High.** Compose UI moves largely as-is. Contracts, orchestration, validation and error models move to `commonMain`. | **None.** Every screen, the state logic and the identity client are rewritten in TypeScript. |
| Work needed to share the identity client | Swap OkHttp for **Ktor** in `commonMain`. Move `java.security`/`javax.crypto` uses behind `expect`/`actual`: Android Keystore on Android, CryptoKit and the Secure Enclave on iOS. | Rewrite in TypeScript. Device keys and attestation need native modules on both platforms anyway. |
| Platform security (device key, attestation) | `actual` implementations: Android Keystore plus Play Integrity; iOS Secure Enclave plus **App Attest**. | Native modules for the same platform APIs. JavaScript cannot touch the Secure Enclave or Keystore directly. |
| DI | Replace Hilt (Android-only) with Koin or manual wiring. That touches the app shell and ViewModels. | Not applicable (a new stack). |
| Backend impact | **None.** The same HTTP contracts. | **None.** The same HTTP contracts. |
| Main risks | Compose Multiplatform for iOS must be confirmed stable at the version adopted; the Kotlin and AGP toolchain must be aligned; Hilt must be replaced. | A full rewrite of a security-sensitive client; two languages in one product; the proof and session behaviour must be re-proven from scratch. |

**iOS server contract gap, whichever is chosen:**
- identity verifies **Play Integrity** only (ADR-0031). iOS would need **App
  Attest** verification server-side, which is a new ADR;
- and the restricted or unverified tier semantics (an owner decision) would
  have to cover iOS.

## 3. Recommendation

**Kotlin Multiplatform with Compose Multiplatform**, adopted incrementally,
**after** the Android Play launch work. It reuses the most existing work
(the Compose UI and the plain-Kotlin identity logic), and the seams for
platform security already exist.

**Shared UI does not remove platform-specific work.** Whichever stack is chosen, each platform still needs its own:
- device-key security (Android Keystore; iOS Secure Enclave);
- attestation (Play Integrity; App Attest, plus server verification);
- calling integration.

The calling pre-filter stays Android-only. The carrier-side screening path
(ADR-0002) serves both platforms.

## 4. Proof of concept (proposed, not started)

**Scope:** one module, `shared:identity`, as a KMP library with Android and
iOS targets. It contains:
- `IdentityContract` (moved, unchanged behaviour);
- the input rules;
- `IdentityError` mapping;
- the `IdentityAuthenticator` orchestration;
- an HTTP client on **Ktor**.

Crypto and storage stay behind the existing interfaces, with Android
`actual`s delegating to today's code.

**Acceptance criteria (measurable):**
1. **Android builds and passes tests with the moved module:** the existing
   `IdentityClientTest` and `IdentityAuthenticatorTest` cases pass unchanged in
   behaviour, including 8 concurrent callers making exactly one refresh.
2. **The iOS target compiles in CI:** `linkDebugFrameworkIosSimulatorArm64`,
   on a macOS runner, which is a CI cost for the owner to approve.
3. **No behaviour change on Android:** the same requests (MockWebServer
   assertions) and the same error mapping.
4. **The iOS proof and storage `actual`s are stubs that fail closed.** No iOS
   security claim is made until Secure Enclave plus App Attest are implemented
   and identity verifies App Attest.

**Not in the proof of concept:** UI migration, Hilt replacement, App Attest
server work, and the call pre-filter.

## 5. Decisions for the owner

- **Choose the frontend stack** (recommended: KMP/CMP).
- **Approve the macOS CI runner cost** needed to build iOS targets. Pricing
  is unverified.
- **Accept that iOS needs App Attest server-side verification,** through a
  new ADR, and decide the restricted-tier policy for iOS.
