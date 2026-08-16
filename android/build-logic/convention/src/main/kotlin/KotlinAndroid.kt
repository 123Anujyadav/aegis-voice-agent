import com.android.build.api.dsl.CommonExtension
import org.gradle.api.JavaVersion
import org.gradle.api.Project
import org.gradle.api.artifacts.VersionCatalogsExtension
import org.gradle.api.plugins.JavaPluginExtension
import org.gradle.jvm.toolchain.JavaLanguageVersion
import org.gradle.kotlin.dsl.getByType
import org.jetbrains.kotlin.gradle.dsl.JvmTarget
import org.jetbrains.kotlin.gradle.dsl.KotlinAndroidProjectExtension

/**
 * Shared configuration applied by both the application and library convention
 * plugins.
 *
 * This function is the single place where SDK levels, Java/Kotlin language
 * settings and compiler flags are decided for the entire Android build. Every
 * module receives an identical setup, which is the whole point: divergence in
 * these settings produces defects that appear in one module and not another and
 * are correspondingly hard to attribute.
 *
 * @param commonExtension The Android extension of the module being configured,
 *   which may be either an application or a library extension.
 */
internal fun Project.configureKotlinAndroid(
    commonExtension: CommonExtension<*, *, *, *, *, *>,
) {
    val libs = extensions.getByType<VersionCatalogsExtension>().named("libs")

    commonExtension.apply {
        compileSdk = libs.findVersion("compileSdk").get().requiredVersion.toInt()

        defaultConfig {
            minSdk = libs.findVersion("minSdk").get().requiredVersion.toInt()
        }

        compileOptions {
            sourceCompatibility = JavaVersion.VERSION_17
            targetCompatibility = JavaVersion.VERSION_17

            // Core library desugaring makes modern java.time and java.util.stream
            // APIs available below API 26. We set minSdk to 26, so it is not
            // strictly required today; it is left off deliberately rather than
            // enabled "just in case", because it adds a build step and grows the
            // APK, and Phase 1 §17 gates APK size growth at 2%.
            isCoreLibraryDesugaringEnabled = false
        }

        // Packaging excludes. Without these, duplicate licence and metadata files
        // from transitive dependencies collide and fail the merge task with an
        // error that names the file rather than the dependency that supplied it —
        // one of the more time-consuming build failures to diagnose.
        packaging {
            resources {
                excludes += setOf(
                    "/META-INF/{AL2.0,LGPL2.1}",
                    "/META-INF/LICENSE*",
                    "/META-INF/NOTICE*",
                    "/META-INF/DEPENDENCIES",
                    "/META-INF/*.kotlin_module",
                    "META-INF/versions/9/previous-compilation-data.bin",
                )
            }
        }

        // Lint is configured identically for every module so that a defect
        // suppressed in one place is not silently permitted in another.
        lint {
            // Warnings become errors: warning debt is never repaid voluntarily
            // (Phase 1 §17).
            warningsAsErrors = true
            abortOnError = true
            checkDependencies = true
            checkReleaseBuilds = true
            // A baseline is deliberately NOT configured. A baseline file lets
            // existing violations persist indefinitely and, worse, hides them
            // from review. New violations must be fixed, not recorded.
            sarifReport = true
            htmlReport = true
            xmlReport = false
        }
    }

    // The JVM toolchain pins the exact JDK used to compile, independent of
    // whichever JDK happens to be on the engineer's PATH. Without it, one
    // machine compiling with JDK 21 and another with JDK 17 can produce
    // different bytecode from identical source.
    extensions.getByType<JavaPluginExtension>().toolchain {
        languageVersion.set(
            JavaLanguageVersion.of(libs.findVersion("javaToolchain").get().requiredVersion)
        )
    }

    extensions.getByType<KotlinAndroidProjectExtension>().compilerOptions {
        jvmTarget.set(JvmTarget.JVM_17)

        // Warnings as errors, matching the Android Lint policy above and the
        // repository-wide rule in Phase 1 §18.
        allWarningsAsErrors.set(true)

        freeCompilerArgs.addAll(
            // Opt-in requirements must be acknowledged explicitly at each use
            // site rather than blanket-suppressed, so that an experimental API
            // entering the codebase is visible in review.
            "-opt-in=kotlin.RequiresOptIn",

            // Emit metadata that lets Compose's compiler report which
            // composables are skippable and which are not. Recomposition of a
            // non-skippable composable is the most common cause of jank, and
            // this is what makes it measurable rather than guessed at.
            "-P",
            "plugin:androidx.compose.compiler.plugins.kotlin:metricsDestination=" +
                "${layout.buildDirectory.get().asFile.absolutePath}/compose-metrics",
            "-P",
            "plugin:androidx.compose.compiler.plugins.kotlin:reportsDestination=" +
                "${layout.buildDirectory.get().asFile.absolutePath}/compose-reports",
        )
    }
}
