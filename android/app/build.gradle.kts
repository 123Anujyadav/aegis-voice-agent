// =============================================================================
// :app — the application shell.
//
// Thin by design. It owns the dependency-injection graph root, the navigation
// host and the merged manifest, and nothing else. Any business logic appearing
// here is a boundary violation (Phase 1 §3): it belongs in a feature module,
// which can be tested without building the whole application.
//
// Build variants, signing and packaging come from the convention plugin; this
// file declares only this module's dependencies.
// =============================================================================

plugins {
    id("callscreen.android.application")
    id("callscreen.android.compose")
    id("callscreen.android.hilt")
}

android {
    namespace = "com.callscreen"
}

dependencies {
    // Core modules the shell genuinely needs to start the application. It
    // deliberately does NOT depend on every core module: the shell should not
    // be a convenience aggregator, because that makes every module a
    // transitive dependency of every other and defeats incremental builds.
    implementation(projects.core.common)
    implementation(projects.core.designsystem)
    implementation(projects.core.ui)
    implementation(projects.core.shared)
    implementation(projects.core.logging)

    implementation(projects.feature.onboarding)
    implementation(projects.feature.calls)
    implementation(projects.feature.screening)
    implementation(projects.feature.summary)
    implementation(projects.feature.protection)
    implementation(projects.feature.assistant)
    implementation(projects.feature.family)
    implementation(projects.feature.settings)
    implementation(projects.feature.business)


    implementation(libs.androidx.activity.compose)
    implementation(libs.androidx.navigation.compose)
    implementation(libs.androidx.lifecycle.runtime.ktx)
    implementation(libs.hilt.navigation.compose)
    implementation(libs.timber)
}

