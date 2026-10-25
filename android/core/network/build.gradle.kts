// =============================================================================
// :core:network
//
// Connect-RPC client, interceptors, retry and backoff policy, and the offline request queue.
//
// Configuration comes from the convention plugin applied below; this file
// declares only what is specific to this module (Phase 1 monorepo guideline:
// a module's build file should state its dependencies, not re-derive the
// build's conventions).
// =============================================================================

plugins {
    id("callscreen.android.library")
    id("callscreen.android.hilt")
    alias(libs.plugins.kotlin.serialization)
}

android {
    namespace = "com.callscreen.core.network"
}

dependencies {
    // The identity HTTP client (com.callscreen.core.network.identity): plain
    // JSON over HTTPS, as identity serves it (services/go/identity/internal/httpapi).
    // api: OkHttpClient is part of IdentityClient's public API (its constructor
    // and defaultHttpClient()), so consumers must see it.
    api(libs.okhttp)
    implementation(libs.kotlinx.serialization.json)
    testImplementation(libs.okhttp.mockwebserver)
    // Test only: a real TLS server and certificates for the pinning tests.
    testImplementation(libs.okhttp.tls)
    // Test only: IdentityLiveContractTest signs real proofs with the app's
    // own DpopProofFactory. core:security does not depend on this module.
    testImplementation(projects.core.security)
}