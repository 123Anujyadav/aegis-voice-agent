// =============================================================================
// Settings for the build-logic included build.
//
// build-logic compiles the convention plugins that configure every other
// module. It is a separate Gradle build with its own settings file because it
// must be fully built BEFORE the main build's configuration phase begins.
// =============================================================================

// build-logic compiles with the same pinned toolchain as the main build, so it
// needs the same resolver. Without it this build fails first, before the main
// build has a chance to report anything useful.
plugins {
    id("org.gradle.toolchains.foojay-resolver-convention") version "0.9.0"
}

dependencyResolutionManagement {
    repositories {
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

    // The version catalog is shared with the main build rather than duplicated.
    // Duplication here is a classic monorepo trap: the convention plugin ends up
    // compiled against one AGP version while modules resolve another, and the
    // resulting failure surfaces as an opaque NoSuchMethodError at configuration
    // time.
    versionCatalogs {
        create("libs") {
            from(files("../gradle/libs.versions.toml"))
        }
    }
}

rootProject.name = "build-logic"

include(":convention")
