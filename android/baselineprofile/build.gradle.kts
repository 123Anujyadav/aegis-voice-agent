/*
 * Generates the app's baseline profile: the classes and methods a cold start
 * and the first screens run, which Android compiles ahead of time at install
 * (through androidx.profileinstaller) instead of interpreting them on first
 * use (production-readiness F10).
 *
 * Like :test-minified, a self-instrumenting test APK that drives :app from
 * outside, here its profile build: release's configuration without R8, so
 * the recorded profile names classes as the source does. The output is
 * committed as app/src/main/baseline-prof.txt, which the Android Gradle
 * Plugin packages into release builds, rewritten for R8's renaming.
 * Generating it needs a device or emulator running Android 13 or later, or
 * a rooted one (any Google APIs emulator image):
 *
 *   ./gradlew :baselineprofile:connectedProfileAndroidTest \
 *     -Pandroid.testInstrumentationRunnerArguments.androidx.benchmark.enabledRules=BaselineProfile
 *
 * then copy the generated *-baseline-prof.txt from
 * build/outputs/connected_android_test_additional_output to
 * app/src/main/baseline-prof.txt (docs/phase19/BASELINE-PROFILE.md). It is
 * not run in CI: a profile is regenerated when startup or the first screens
 * change, and reviewed like code.
 */
plugins {
    alias(libs.plugins.android.test)
    alias(libs.plugins.kotlin.android)
}

android {
    namespace = "com.callscreen.baselineprofile"
    compileSdk = libs.versions.compileSdk.get().toInt()

    defaultConfig {
        // BaselineProfileRule needs Android 9 or later, rooted, or Android 13 or later.
        minSdk = 28
        targetSdk = libs.versions.targetSdk.get().toInt()
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
    }

    buildTypes {
        // Matches :app's profile build type: release without R8, debug-signed.
        create("profile") {
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
    experimentalProperties["android.experimental.self-instrumenting"] = true
}

kotlin {
    jvmToolchain(libs.versions.javaToolchain.get().toInt())
}

// A profile is generated from the build that ships, never the debug build.
androidComponents {
    beforeVariants(selector().withBuildType("debug")) { it.enable = false }
}

dependencies {
    implementation(libs.androidx.benchmark.macro.junit4)
    implementation(libs.androidx.test.runner)
    implementation(libs.androidx.test.ext.junit)
    implementation(libs.androidx.test.uiautomator)
    implementation(libs.junit4)
}
