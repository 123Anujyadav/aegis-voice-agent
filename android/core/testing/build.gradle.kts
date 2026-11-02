// =============================================================================
// :core:testing
//
// Shared fakes, JUnit rules and fixtures. Consumed only by test source sets.
//
// Configuration comes from the convention plugin applied below; this file
// declares only what is specific to this module (Phase 1 monorepo guideline:
// a module's build file should state its dependencies, not re-derive the
// build's conventions).
// =============================================================================

plugins {
    id("callscreen.android.library")
    // PseudoTranslation provides a Compose context for layout tests.
    id("callscreen.android.compose")
}

android {
    namespace = "com.callscreen.core.testing"
}

dependencies {
    // The release languages (AppLanguage) and the theme screens are shown in.
    implementation(projects.core.ui)
    implementation(projects.core.designsystem)
    // TranslationLayout drives a Compose test rule. Test-only consumers.
    implementation(libs.androidx.compose.ui.test.junit4)
    implementation(libs.junit4)
}