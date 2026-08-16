// =============================================================================
// :core:ui
//
// Shared composables that are aware of domain models, built on :core:designsystem.
//
// Configuration comes from the convention plugin applied below; this file
// declares only what is specific to this module (Phase 1 monorepo guideline:
// a module's build file should state its dependencies, not re-derive the
// build's conventions).
// =============================================================================

plugins {
    id("callscreen.android.library")
    id("callscreen.android.compose")
}

android {
    namespace = "com.callscreen.core.ui"
}

dependencies {
    implementation(projects.core.designsystem)
    implementation(libs.androidx.compose.material.icons.extended)
}