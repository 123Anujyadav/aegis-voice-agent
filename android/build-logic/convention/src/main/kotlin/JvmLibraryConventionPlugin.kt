import org.gradle.api.Plugin
import org.gradle.api.Project
import org.gradle.api.artifacts.VersionCatalogsExtension
import org.gradle.api.plugins.JavaPluginExtension
import org.gradle.api.tasks.testing.Test
import org.gradle.jvm.toolchain.JavaLanguageVersion
import org.gradle.kotlin.dsl.configure
import org.gradle.kotlin.dsl.dependencies
import org.gradle.kotlin.dsl.getByType
import org.gradle.kotlin.dsl.withType
import org.jetbrains.kotlin.gradle.dsl.JvmTarget
import org.jetbrains.kotlin.gradle.dsl.KotlinJvmProjectExtension

/**
 * Configures a pure-Kotlin JVM module with no dependency on the Android
 * framework.
 *
 * Modules holding domain models and business rules apply this rather than the
 * Android library plugin. The benefit is structural, not cosmetic: a module
 * that cannot import `android.*` cannot accidentally couple domain logic to the
 * platform, and its tests run on a plain JVM in milliseconds rather than under
 * Robolectric. The compiler enforces the layering that a convention alone would
 * only ask for.
 */
class JvmLibraryConventionPlugin : Plugin<Project> {
    override fun apply(target: Project) {
        with(target) {
            pluginManager.apply("org.jetbrains.kotlin.jvm")
            pluginManager.apply("io.gitlab.arturbosch.detekt")
            pluginManager.apply("org.jlleitschuh.gradle.ktlint")

            val libs = extensions.getByType<VersionCatalogsExtension>().named("libs")

            extensions.configure<JavaPluginExtension> {
                toolchain {
                    languageVersion.set(
                        JavaLanguageVersion.of(
                            libs.findVersion("javaToolchain").get().requiredVersion,
                        ),
                    )
                }
            }

            extensions.getByType<KotlinJvmProjectExtension>().compilerOptions {
                jvmTarget.set(JvmTarget.JVM_17)
                allWarningsAsErrors.set(true)
                freeCompilerArgs.add("-opt-in=kotlin.RequiresOptIn")
            }

            tasks.withType<Test>().configureEach {
                useJUnitPlatform()
                timeout.set(java.time.Duration.ofMinutes(5))
                testLogging {
                    events("failed", "skipped")
                    showStackTraces = true
                    exceptionFormat =
                        org.gradle.api.tasks.testing.logging.TestExceptionFormat.FULL
                }
            }

            dependencies {
                add("implementation", libs.findLibrary("kotlinx.coroutines.core").get())
                add("testImplementation", libs.findBundle("unit.test").get())
                add("testRuntimeOnly", libs.findLibrary("junit.jupiter.engine").get())
            }
        }
    }
}
