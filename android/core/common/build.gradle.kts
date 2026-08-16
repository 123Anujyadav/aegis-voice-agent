// =============================================================================
// :core:common
//
// Result types, coroutine dispatchers and a Clock abstraction. Zero Android imports so it compiles and tests on a plain JVM.
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