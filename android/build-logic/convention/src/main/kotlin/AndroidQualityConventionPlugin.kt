import io.gitlab.arturbosch.detekt.Detekt
import io.gitlab.arturbosch.detekt.extensions.DetektExtension
import org.gradle.api.Plugin
import org.gradle.api.Project
import org.gradle.kotlin.dsl.configure
import org.gradle.kotlin.dsl.withType

/**
 * Applies the static-analysis policy to a module.
 *
 * Applied automatically by the application and library convention plugins, so
 * that no module can opt out of analysis by omitting a line from its build
 * file. Quality gates that are opt-in are, over time, opted out of.
 *
 * Tool responsibilities are deliberately separated (Phase 1 §18):
 *   - ktlint owns formatting. Style is never a review comment.
 *   - detekt owns complexity and correctness smells.
 *   - Android Lint owns platform and API misuse, configured in KotlinAndroid.kt.
 */
class AndroidQualityConventionPlugin : Plugin<Project> {
    override fun apply(target: Project) {
        with(target) {
            pluginManager.apply("io.gitlab.arturbosch.detekt")
            pluginManager.apply("org.jlleitschuh.gradle.ktlint")

            extensions.configure<DetektExtension> {
                // One configuration for the whole build, resolved from the
                // Android root rather than per module. A per-module config file
                // is how one module quietly acquires a laxer standard.
                config.setFrom(rootProject.files("config/detekt/detekt.yml"))
                buildUponDefaultConfig = true
                parallel = true
                // A baseline is intentionally not configured. Baselines preserve
                // existing violations indefinitely and hide them from review.
                autoCorrect = false
            }

            tasks.withType<Detekt>().configureEach {
                // Detekt must analyse against the compiled classpath to perform
                // type resolution. Without it, roughly a third of the rules —
                // including most of the genuinely valuable ones — silently do
                // nothing.
                jvmTarget = "17"
                reports {
                    sarif.required.set(true)
                    html.required.set(true)
                    xml.required.set(false)
                    txt.required.set(false)
                    md.required.set(false)
                }
            }
        }
    }
}
