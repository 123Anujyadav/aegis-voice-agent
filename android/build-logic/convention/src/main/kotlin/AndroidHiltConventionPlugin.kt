import org.gradle.api.Plugin
import org.gradle.api.Project
import org.gradle.api.artifacts.VersionCatalogsExtension
import org.gradle.kotlin.dsl.dependencies
import org.gradle.kotlin.dsl.getByType

/**
 * Adds Hilt dependency injection with KSP code generation.
 *
 * Opt-in per module for the same reason as Compose: annotation processing is
 * one of the slowest phases of an Android build, and a module with no injected
 * types should not pay for it. Only modules that declare `@Module`, `@Inject`
 * or an entry point apply this.
 *
 * KSP is used rather than KAPT. KAPT generates Java stubs for every Kotlin
 * source before processing, which roughly doubles annotation-processing time;
 * KSP reads Kotlin directly and is typically two to three times faster on a
 * module of this size.
 */
class AndroidHiltConventionPlugin : Plugin<Project> {
    override fun apply(target: Project) {
        with(target) {
            pluginManager.apply("com.google.devtools.ksp")
            pluginManager.apply("dagger.hilt.android.plugin")

            val libs = extensions.getByType<VersionCatalogsExtension>().named("libs")

            dependencies {
                add("implementation", libs.findLibrary("hilt.android").get())
                add("ksp", libs.findLibrary("hilt.compiler").get())

                // The Hilt test runtime and its generated components are needed
                // by instrumentation tests that replace bindings.
                add("androidTestImplementation", libs.findLibrary("hilt.android.testing").get())
                add("kspAndroidTest", libs.findLibrary("hilt.compiler").get())
            }
        }
    }
}
