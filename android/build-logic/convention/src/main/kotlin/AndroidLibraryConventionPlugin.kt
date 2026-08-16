import com.android.build.gradle.LibraryExtension
import org.gradle.api.Plugin
import org.gradle.api.Project
import org.gradle.api.artifacts.VersionCatalogsExtension
import org.gradle.kotlin.dsl.assign
import org.gradle.kotlin.dsl.configure
import org.gradle.kotlin.dsl.dependencies
import org.gradle.kotlin.dsl.getByType
import org.gradle.api.tasks.testing.Test
import org.gradle.kotlin.dsl.withType

/**
 * Configures an Android library module.
 *
 * This is the plugin the overwhelming majority of modules apply. It establishes
 * the shared Kotlin and SDK configuration and wires the standard unit-test
 * stack, so that a module's own build file declares only its dependencies and
 * whatever is genuinely specific to it.
 */
class AndroidLibraryConventionPlugin : Plugin<Project> {
    override fun apply(target: Project) {
        with(target) {
            pluginManager.apply("com.android.library")
            pluginManager.apply("org.jetbrains.kotlin.android")
            pluginManager.apply("callscreen.android.quality")

            val libs = extensions.getByType<VersionCatalogsExtension>().named("libs")

            extensions.configure<LibraryExtension> {
                configureKotlinAndroid(this)

                defaultConfig {
                    // targetSdk on a library only affects instrumentation tests;
                    // the application module's value is what ships. Setting it
                    // keeps test behaviour consistent with production.
                    testOptions.targetSdk =
                        libs.findVersion("targetSdk").get().requiredVersion.toInt()
                    testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
                    // Library modules never ship their own ProGuard consumer
                    // rules unless they expose reflection-dependent APIs. Adding
                    // a blanket keep rule "to be safe" defeats shrinking for
                    // every consumer and is a common cause of APK bloat.
                }

                testOptions {
                    unitTests {
                        // Android framework methods throw by default in unit
                        // tests. Returning defaults instead lets a test exercise
                        // code that incidentally touches the framework without
                        // requiring Robolectric for every such case.
                        isReturnDefaultValues = true
                        isIncludeAndroidResources = true
                    }
                }

                buildFeatures {
                    // Off by default; a module that needs BuildConfig opts in
                    // explicitly. Generating an unused class for twenty modules
                    // is pure build-time cost.
                    buildConfig = false
                }
            }

            tasks.withType<Test>().configureEach {
                // JUnit 5 is the primary engine. The Vintage engine is included
                // by the test dependencies so that JUnit 4 rules — which large
                // parts of the AndroidX test stack still require — continue to
                // run alongside Jupiter tests.
                useJUnitPlatform()

                // Fail rather than hang if a test deadlocks. A hung test in CI
                // consumes an entire job slot until the global timeout, which is
                // far more expensive than a failed assertion.
                timeout.set(java.time.Duration.ofMinutes(10))

                testLogging {
                    events("failed", "skipped")
                    showStackTraces = true
                    showCauses = true
                    // Full exception output. A truncated stack trace in CI logs
                    // forces a local reproduction, which is the slowest possible
                    // debugging loop.
                    exceptionFormat = org.gradle.api.tasks.testing.logging.TestExceptionFormat.FULL
                }
            }

            dependencies {
                add("implementation", libs.findLibrary("androidx.core.ktx").get())
                add("implementation", libs.findLibrary("kotlinx.coroutines.android").get())

                add("testImplementation", libs.findBundle("unit.test").get())
                add("testRuntimeOnly", libs.findLibrary("junit.jupiter.engine").get())
                add("testRuntimeOnly", libs.findLibrary("junit.vintage.engine").get())

                add("androidTestImplementation", libs.findBundle("android.test").get())
            }
        }
    }
}
