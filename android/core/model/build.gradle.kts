// =============================================================================
// :core:model
//
// Pure domain models. Deliberately Android-free so domain logic cannot couple to the platform.
//
// Configuration comes from the convention plugin applied below; this file
// declares only what is specific to this module (Phase 1 monorepo guideline:
// a module's build file should state its dependencies, not re-derive the
// build's conventions).
// =============================================================================

plugins {
    id("callscreen.jvm.library")
}

dependencies {
}