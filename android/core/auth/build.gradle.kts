// =============================================================================
// :core:auth
//
// Identity orchestration: requesting a code, enrolling this device, keeping a
// valid access token (single-flight refresh), and signing out locally.
//
// Dependency boundaries:
//   core:network   the identity HTTP client and its contract
//   core:datastore the encrypted session store
//   (core:security's device-key proofs reach the client through DeviceProofs)
// It depends on no feature module. Features depend on it.
// =============================================================================

plugins {
    id("callscreen.android.library")
    id("callscreen.android.hilt")
}

android {
    namespace = "com.callscreen.core.auth"
}

dependencies {
    // api: IdentityClient, IdentityError, OtpChallenge and EncryptedSessionStore
    // appear in this module's public API. The device-key proofs (core:security)
    // reach IdentityClient as a DeviceProofs, wired by the app.
    api(projects.core.network)
    api(projects.core.datastore)
    implementation(libs.kotlinx.coroutines.core)
    testImplementation(libs.okhttp.mockwebserver)
}
