/*
 * Functional tests of the R8-minified app, as it is built for release.
 *
 * :app's instrumented tests run the debug build, inside the app's process.
 * They cannot run against the minified build: R8 renames and inlines the
 * app's classes (Hilt modules among them), so a test APK compiled against the
 * originals would find classes and methods that no longer exist. This module
 * is a separate, self-instrumenting test APK that drives the installed
 * minified app from outside, through UI Automator, the way the Macrobenchmark
 * and baseline-profile modules test release builds. It sees only what a user
 * sees: the text on screen, the buttons, Back, Home and rotation.
 *
 * The app under test is :app's staging build type: initWith(release), so the
 * same R8 minification and resource shrinking as production, debug-signed so
 * it needs no release secrets (application id com.callscreen.staging).
 *
 * Run: ./gradlew :test-minified:connectedStagingAndroidTest
 * (tools/scripts/android-device-tests.sh runs it on the CI emulator).
 */
plugins {
    alias(libs.plugins.android.test)
    alias(libs.plugins.kotlin.android)
}

android {
    namespace = "com.callscreen.minifiedtest"
    compileSdk = libs.versions.compileSdk.get().toInt()

    defaultConfig {
        minSdk = libs.versions.minSdk.get().toInt()
        targetSdk = libs.versions.targetSdk.get().toInt()
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
    }

    buildTypes {
        // Matches :app's staging build type, which is the variant under test.
        create("staging") {
            isDebuggable = true
            signingConfig = signingConfigs.getByName("debug")
            matchingFallbacks += listOf("release")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    targetProjectPath = ":app"
    // The test APK instruments itself and drives the app as a separate
    // process: nothing of the app's code is linked into the test.
    experimentalProperties["android.experimental.self-instrumenting"] = true
}

kotlin {
    jvmToolchain(libs.versions.javaToolchain.get().toInt())
}

// Only the minified build is tested here; the debug build has :app's own
// instrumented tests.
androidComponents {
    beforeVariants(selector().withBuildType("debug")) { it.enable = false }
}

dependencies {
    implementation(libs.androidx.test.runner)
    implementation(libs.androidx.test.ext.junit)
    implementation(libs.androidx.test.uiautomator)
    implementation(libs.junit4)
}
