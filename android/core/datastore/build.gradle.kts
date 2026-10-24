// =============================================================================
// :core:datastore
//
// Proto DataStore and encrypted preferences.
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
    namespace = "com.callscreen.core.datastore"
}

dependencies {
    // The encrypted session store (com.callscreen.core.datastore.session):
    // a Keystore AES-GCM key seals the record, DataStore holds the ciphertext.
    // androidx.security-crypto is NOT used: Google deprecated all of its APIs
    // (1.1.0-alpha07, 2025-04-09) "in favour of existing platform APIs and
    // direct use of Android Keystore".
    implementation(libs.androidx.datastore.preferences)
    implementation(libs.kotlinx.serialization.json)
}