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

    defaultConfig {
        // identity's public origin (configuration, NOT a secret). Device proofs
        // sign <origin>/v1/otp/validate and <origin>/v1/token/refresh, which
        // must equal identity's CS_IDENTITY_ENROLLMENT_CANONICAL_URL and
        // CS_IDENTITY_REFRESH_CANONICAL_URL exactly. Unset (the default), the
        // build has no identity server and sign-in reports itself unavailable;
        // no host is assumed. Set it with -Pcallscreen.identityBaseUrl=https://...
        val identityBaseUrl = providers.gradleProperty("callscreen.identityBaseUrl").getOrElse("")
        require(Regex("^[A-Za-z0-9:/._-]*$").matches(identityBaseUrl)) { "callscreen.identityBaseUrl must be a plain URL" }
        buildConfigField("String", "IDENTITY_BASE_URL", "\"$identityBaseUrl\"")
    }
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
    // The identity flow: orchestration, its HTTP client and session store
    // (api from core:auth), and the device-key proofs.
    implementation(projects.core.auth)
    implementation(projects.core.security)

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

