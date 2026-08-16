// =============================================================================
// Root build script for the Android build.
//
// It deliberately contains almost nothing.
//
// The historical pattern of configuring every module from the root with
// `subprojects { }` or `allprojects { }` is rejected here. It defeats
// configuration-on-demand and the configuration cache — two of the largest
// build-speed levers available to us (see gradle.properties) — and it makes a
// module's configuration invisible from that module's own build file, so an
// engineer reading :core:telephony cannot tell what it is actually configured
// with. Convention plugins in build-logic do that work instead.
//
// Plugins are declared with `apply false` so that their versions are resolved
// and their classpaths are available to subprojects, without applying them to
// the root project, which is not itself an Android or Kotlin module.
// =============================================================================

plugins {
    alias(libs.plugins.android.application) apply false
    alias(libs.plugins.android.library) apply false
    alias(libs.plugins.android.test) apply false
    alias(libs.plugins.kotlin.android) apply false
    alias(libs.plugins.kotlin.jvm) apply false
    alias(libs.plugins.kotlin.compose) apply false
    alias(libs.plugins.kotlin.serialization) apply false
    alias(libs.plugins.ksp) apply false
    alias(libs.plugins.hilt) apply false
    alias(libs.plugins.detekt) apply false
    alias(libs.plugins.ktlint) apply false
}

// Registering `clean` at the root is the one convenience worth keeping: it lets
// an engineer clear build output for the whole build with a single invocation
// rather than remembering which module holds stale artefacts.
tasks.register<Delete>("clean") {
    delete(rootProject.layout.buildDirectory)
}
