// =============================================================================
// :core:designsystem
//
// Design tokens, theme and the component library. The single source of visual
// truth for every screen in the product.
//
// This module has NO dependency on any other :core module and never will. It
// sits at the bottom of the dependency graph precisely so that everything else
// can depend on it without creating a cycle — a design system that knows about
// domain models is not a design system.
//
// Values here are generated from design/tokens/*.json. See
// docs/design/10-tokens-and-naming.md for the pipeline.
// =============================================================================

plugins {
    id("callscreen.android.library")
    id("callscreen.android.compose")
}

android {
    namespace = "com.callscreen.core.designsystem"
}

dependencies {
    // Easing curves and spring specs for the motion tokens. Declared explicitly
    // rather than leaned on transitively — see the note in libs.versions.toml.
    implementation(libs.androidx.compose.animation)
    implementation(libs.androidx.compose.material.icons.extended)
}

