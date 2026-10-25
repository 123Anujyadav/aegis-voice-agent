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

        // edge-api's public origin (configuration, NOT a secret). It must equal
        // edge-api's CS_EDGE_API_PUBLIC_ORIGIN, which every proof's htu is
        // checked against. Unset (the default) means protected requests are
        // unavailable; no host is assumed. -Pcallscreen.edgeApiBaseUrl=https://...
        val edgeApiBaseUrl = providers.gradleProperty("callscreen.edgeApiBaseUrl").getOrElse("")
        require(Regex("^[A-Za-z0-9:/._-]*$").matches(edgeApiBaseUrl)) { "callscreen.edgeApiBaseUrl must be a plain URL" }
        buildConfigField("String", "EDGE_API_BASE_URL", "\"$edgeApiBaseUrl\"")

        // The Google Cloud project number linked to this app in Play Console
        // (an identifier, NOT a secret), for Play Integrity standard requests
        // (ADR-0031). Unset (the default) means no integrity tokens are sent
        // and identity records sessions as unverified.
        // -Pcallscreen.playIntegrityCloudProjectNumber=<digits>
        val playIntegrityProject = providers.gradleProperty("callscreen.playIntegrityCloudProjectNumber").getOrElse("")
        require(Regex("^[0-9]*$").matches(playIntegrityProject)) { "callscreen.playIntegrityCloudProjectNumber must be digits" }
        buildConfigField("String", "PLAY_INTEGRITY_CLOUD_PROJECT_NUMBER", "\"$playIntegrityProject\"")

        // Certificate pins for each origin (ADR-0010: "our own leaf and a
        // backup pin"). These are configuration, NOT secrets: hashes of public
        // keys, as sha256/<base64>, at least two each, comma-separated. Unset
        // (the default) means no pinning, which is today's behaviour. A value
        // that is set but unusable turns that endpoint off rather than leaving
        // it unpinned. They must be set before a release (readiness §7).
        // -Pcallscreen.identityCertPins=sha256/...,sha256/...
        val identityCertPins = providers.gradleProperty("callscreen.identityCertPins").getOrElse("")
        require(Regex("^[A-Za-z0-9+/=,]*$").matches(identityCertPins)) { "callscreen.identityCertPins must be sha256/<base64> pins, comma-separated" }
        buildConfigField("String", "IDENTITY_CERT_PINS", "\"$identityCertPins\"")
        val edgeApiCertPins = providers.gradleProperty("callscreen.edgeApiCertPins").getOrElse("")
        require(Regex("^[A-Za-z0-9+/=,]*$").matches(edgeApiCertPins)) { "callscreen.edgeApiCertPins must be sha256/<base64> pins, comma-separated" }
        buildConfigField("String", "EDGE_API_CERT_PINS", "\"$edgeApiCertPins\"")
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

