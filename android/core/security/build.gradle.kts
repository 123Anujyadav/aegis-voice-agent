// =============================================================================
// :core:security
//
// Play Integrity attestation, keystore access and certificate pinning.
//
// Configuration comes from the convention plugin applied below; this file
// declares only what is specific to this module (Phase 1 monorepo guideline:
// a module's build file should state its dependencies, not re-derive the
// build's conventions).
// =============================================================================

plugins {
    id("callscreen.android.library")
    id("callscreen.android.hilt")
}

android {
    namespace = "com.callscreen.core.security"
}

dependencies {
    // Standard Play Integrity requests (ADR-0031). Resolved from Google's Maven
    // repository, which the settings content filter admits for com.google.*.
    implementation(libs.play.integrity)
}