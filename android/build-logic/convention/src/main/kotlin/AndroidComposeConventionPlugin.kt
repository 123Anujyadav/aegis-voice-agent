import com.android.build.api.dsl.CommonExtension
import org.gradle.api.Plugin
import org.gradle.api.Project
import org.gradle.api.artifacts.VersionCatalogsExtension
import org.gradle.kotlin.dsl.dependencies
import org.gradle.kotlin.dsl.getByType

/**
 * Adds Jetpack Compose with Material 3 to a module.
 *
 * Applied only by modules that actually contain UI. Compose brings a
 * substantial dependency graph and an extra compiler plugin; applying it to a
 * module that has no composables costs build time for nothing, which is why it
 * is a separate opt-in plugin rather than part of the library convention.
 */
class AndroidComposeConventionPlugin : Plugin<Project> {
    override fun apply(target: Project) {
        with(target) {
            // From Kotlin 2.0 the Compose compiler ships with Kotlin itself and
            // is applied as a Gradle plugin. This replaces the older
            // composeOptions.kotlinCompilerExtensionVersion, which required
            // manually keeping a compiler version in step with the Kotlin
            // version — a pairing that broke on almost every Kotlin upgrade.
            pluginManager.apply("org.jetbrains.kotlin.plugin.compose")

            val libs = extensions.getByType<VersionCatalogsExtension>().named("libs")

            // The extension type differs between application and library
            // modules, so the common supertype is resolved dynamically. This is
            // the standard approach for a convention plugin that must serve both.
            val extension = extensions.findByName("android") as? CommonExtension<*, *, *, *, *, *>
                ?: error(
                    "callscreen.android.compose requires an Android module. Apply " +
                        "callscreen.android.library or callscreen.android.application first.",
                )

            extension.buildFeatures.compose = true

            dependencies {
                // The BOM aligns every Compose artefact to one tested
                // combination, so individual Compose libraries are declared
                // without versions. Mixing Compose versions produces link-time
                // failures with messages that name an internal symbol rather
                // than the mismatched artefact.
                val bom = libs.findLibrary("androidx.compose.bom").get()
                add("implementation", platform(bom))
                add("androidTestImplementation", platform(bom))

                add("implementation", libs.findBundle("compose").get())
                add("implementation", libs.findLibrary("androidx.lifecycle.runtime.compose").get())

                // Tooling and the test manifest are debug-only. Shipping the
                // Compose tooling in a release build adds several hundred
                // kilobytes and exposes inspection surfaces that have no place
                // in production.
                add("debugImplementation", libs.findBundle("compose.debug").get())

                add("androidTestImplementation", libs.findLibrary("androidx.compose.ui.test.junit4").get())

                // Screenshot testing runs on the JVM via Robolectric rather than
                // on a device, so visual regressions are caught in the fast PR
                // stage instead of the slow instrumentation stage (Phase 1 §16).
                add("testImplementation", libs.findLibrary("roborazzi").get())
                add("testImplementation", libs.findLibrary("roborazzi.compose").get())
                add("testImplementation", libs.findLibrary("roborazzi.rule").get())
                add("testImplementation", libs.findLibrary("robolectric").get())
            }
        }
    }
}
