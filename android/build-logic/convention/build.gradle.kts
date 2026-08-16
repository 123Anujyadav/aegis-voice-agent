// =============================================================================
// The convention plugin module.
//
// WHY CONVENTION PLUGINS RATHER THAN A SHARED build.gradle SNIPPET:
//
// With twenty Android modules, the alternatives are all worse:
//   - Copying configuration into each build file guarantees drift. Within a
//     quarter, three modules will target a different Java version than the rest
//     and nobody will know until an obscure desugaring bug appears.
//   - `subprojects { }` or `allprojects { }` in the root build file is the old
//     approach. It breaks configuration-on-demand and the configuration cache,
//     which are two of the largest build-speed levers we have (see
//     gradle.properties), and it makes a module's configuration invisible from
//     its own build file.
//
// Convention plugins are compiled, testable Kotlin. A module opts in by
// applying one plugin, and its build file then states only what is genuinely
// specific to that module.
// =============================================================================

plugins {
    `kotlin-dsl`
}

group = "com.callscreen.buildlogic"

// The JVM toolchain here must match the one used by the main build. A mismatch
// produces "class file has wrong version" at configuration time, with an error
// that names neither this file nor the real cause.
java {
    toolchain {
        languageVersion.set(JavaLanguageVersion.of(libs.versions.javaToolchain.get()))
    }
}

kotlin {
    compilerOptions {
        // Warnings in build logic are as significant as warnings in application
        // code — more so, since a deprecation here breaks every module at once.
        allWarningsAsErrors.set(true)
    }
}

dependencies {
    // These are compileOnly because the plugins are applied by the CONSUMING
    // build at its own runtime, not by this module. Declaring them as api or
    // implementation would place two copies of AGP on the classpath and produce
    // a duplicate-class failure.
    compileOnly(libs.android.gradlePlugin)
    compileOnly(libs.kotlin.gradlePlugin)
    compileOnly(libs.ksp.gradlePlugin)
    compileOnly(libs.hilt.gradlePlugin)
    compileOnly(libs.detekt.gradlePlugin)
    compileOnly(libs.compose.compiler.gradlePlugin)
}

// Registering plugins by id lets modules apply them with
// `alias(libs.plugins.callscreen.android.library)` or by literal id, rather
// than with the `apply(plugin = "...")` string form that has no compile-time
// checking.
gradlePlugin {
    plugins {
        register("androidApplication") {
            id = "callscreen.android.application"
            implementationClass = "AndroidApplicationConventionPlugin"
            description = "Configures the application module: SDK levels, build variants, signing, packaging."
        }
        register("androidLibrary") {
            id = "callscreen.android.library"
            implementationClass = "AndroidLibraryConventionPlugin"
            description = "Configures an Android library module with the shared Kotlin, SDK and test setup."
        }
        register("androidCompose") {
            id = "callscreen.android.compose"
            implementationClass = "AndroidComposeConventionPlugin"
            description = "Adds Jetpack Compose with Material 3, strong skipping and metrics reporting."
        }
        register("androidHilt") {
            id = "callscreen.android.hilt"
            implementationClass = "AndroidHiltConventionPlugin"
            description = "Adds Hilt dependency injection with KSP code generation."
        }
        register("jvmLibrary") {
            id = "callscreen.jvm.library"
            implementationClass = "JvmLibraryConventionPlugin"
            description = "Configures a pure-Kotlin JVM module with no Android dependency."
        }
        register("androidQuality") {
            id = "callscreen.android.quality"
            implementationClass = "AndroidQualityConventionPlugin"
            description = "Applies detekt, Android Lint configuration and the warnings-as-errors policy."
        }
    }
}
