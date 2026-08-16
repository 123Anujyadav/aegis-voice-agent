// =============================================================================
// :core:permissions
//
// Runtime permission state machine.
//
// Configuration comes from the convention plugin applied below; this file
// declares only what is specific to this module (Phase 1 monorepo guideline:
// a module's build file should state its dependencies, not re-derive the
// build's conventions).
// =============================================================================

plugins {
    id("callscreen.android.library")
}

android {
    namespace = "com.callscreen.core.permissions"
}

dependencies {
}