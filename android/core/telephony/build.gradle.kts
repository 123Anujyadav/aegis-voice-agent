// =============================================================================
// :core:telephony
//
// CallScreeningService, RoleManager handling and call-forwarding orchestration.
//
// Configuration comes from the convention plugin applied below; this file
// declares only what is specific to this module (Phase 1 monorepo guideline:
// a module's build file should state its dependencies, not re-derive the
// build's conventions).
// =============================================================================

plugins {
    id("callscreen.android.library")
    id("callscreen.android.hilt")
}

android {
    namespace = "com.callscreen.core.telephony"
}

dependencies {
}