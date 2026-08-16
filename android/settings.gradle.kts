// =============================================================================
// Gradle settings for the Android build.
//
// The Android build is a self-contained Gradle build rooted at android/ rather
// than at the repository root. Keeping it separate means a Go or Python change
// never triggers Gradle configuration, and an engineer working only on the
// backend never needs a JDK or the Android SDK installed.
// =============================================================================

// build-logic is an INCLUDED BUILD, not a subproject.
//
// This is what allows convention plugins to be applied by id (for example
// `alias(libs.plugins.callscreen.android.library)`) in every module's build
// script. As a subproject it would create a circular dependency: the modules
// need the plugins to configure themselves, but a subproject is only built
// after configuration has begun.
pluginManagement {
    includeBuild("build-logic")
    repositories {
        // google() is restricted by content filter so that a typosquatted
        // artefact published to Maven Central under an androidx-like
        // coordinate cannot be resolved from the wrong repository. Ordering
        // alone is not a defence; explicit filtering is.
        google {
            content {
                includeGroupByRegex("com\\.android.*")
                includeGroupByRegex("com\\.google.*")
                includeGroupByRegex("androidx.*")
            }
        }
        mavenCentral()
        gradlePluginPortal()
    }
}

// Toolchain auto-provisioning.
//
// The build pins a Java 17 toolchain (see libs.versions.toml). Without a
// resolver, Gradle can only use a JDK 17 that already happens to be installed,
// and fails with "No locally installed toolchains match" on any machine that
// has a different JDK — which is most machines, and every fresh CI runner.
//
// This plugin lets Gradle download the exact toolchain it needs, so the build
// depends on the pinned version rather than on what the engineer happened to
// install. That is what makes `git clone && ./gradlew build` actually work,
// which Phase 1 monorepo guideline #9 requires.
plugins {
    id("org.gradle.toolchains.foojay-resolver-convention") version "0.9.0"
}

dependencyResolutionManagement {
    // FAIL_ON_PROJECT_REPOS forbids a module declaring its own repositories.
    // Without it, one module can silently introduce an unvetted repository and
    // change where every shared dependency resolves from — a supply-chain risk
    // that is invisible in review because it lives in a file nobody re-reads.
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        google {
            content {
                includeGroupByRegex("com\\.android.*")
                includeGroupByRegex("com\\.google.*")
                includeGroupByRegex("androidx.*")
            }
        }
        mavenCentral()
    }
}

// Type-safe project accessors (projects.core.telephony) instead of string paths
// (project(":core:telephony")). A renamed module then fails at compile time
// rather than at configuration time with a less useful message.
enableFeaturePreview("TYPESAFE_PROJECT_ACCESSORS")

rootProject.name = "callscreen"

// --- Application shell -------------------------------------------------------
// Thin by design: DI graph root, navigation host, manifest merge. No business
// logic. Phase 1 §3 treats any logic appearing here as a boundary violation.
include(":app")

// --- Core modules ------------------------------------------------------------
// Horizontal capabilities with no knowledge of any feature. A core module may
// depend on another core module; it may never depend on a feature module.
include(":core:common")
include(":core:model")
include(":core:designsystem")
include(":core:ui")
include(":core:network")
include(":core:database")
include(":core:datastore")
include(":core:telephony")
include(":core:security")
include(":core:analytics")
include(":core:logging")
include(":core:notifications")
include(":core:permissions")
include(":core:testing")

// --- Generated contract bindings ---------------------------------------------
// Included from outside the android/ tree so that Android consumes exactly the
// same generated protobuf bindings as the Go and Python tiers, from one source
// of truth. Duplicating them under android/ would let the client drift from the
// server contract silently.
include(":contracts")
project(":contracts").projectDir = file("../packages/kotlin/contracts-kt")

// --- Benchmark ---------------------------------------------------------------
// Macrobenchmark and baseline-profile generation are NOT included at Phase 2.
//
// A macrobenchmark module measures a running application by driving its UI.
// With no screens yet there is nothing to measure, and a benchmark module that
// exercises an empty app would report meaningless numbers while adding an
// `com.android.test` variant to every build. It is added alongside the first
// real screen, which is the point at which cold-start and frame metrics become
// gateable (Phase 1 §17).

// --- Feature modules ---------------------------------------------------------
include(":core:shared")
include(":feature:onboarding")
include(":feature:calls")
include(":feature:screening")
include(":feature:summary")
include(":feature:protection")
include(":feature:assistant")
include(":feature:family")
include(":feature:settings")
include(":feature:business")


