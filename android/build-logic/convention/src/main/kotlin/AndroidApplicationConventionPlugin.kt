import com.android.build.api.dsl.ApplicationExtension
import org.gradle.api.Plugin
import org.gradle.api.Project
import org.gradle.api.artifacts.VersionCatalogsExtension
import org.gradle.kotlin.dsl.configure
import org.gradle.kotlin.dsl.dependencies
import org.gradle.kotlin.dsl.getByType

/**
 * Configures the application module: SDK levels, build variants, signing and
 * packaging.
 *
 * Applied by exactly one module, `:app`. It exists as a plugin rather than as
 * inline configuration so that the build-variant policy — which determines what
 * ships to users — is version-controlled in one reviewable place rather than
 * spread through a build file that accumulates unrelated edits.
 */
class AndroidApplicationConventionPlugin : Plugin<Project> {
    override fun apply(target: Project) {
        with(target) {
            pluginManager.apply("com.android.application")
            pluginManager.apply("org.jetbrains.kotlin.android")
            pluginManager.apply("callscreen.android.quality")

            val libs = extensions.getByType<VersionCatalogsExtension>().named("libs")

            extensions.configure<ApplicationExtension> {
                configureKotlinAndroid(this)

                defaultConfig {
                    applicationId = "com.callscreen"
                    targetSdk = libs.findVersion("targetSdk").get().requiredVersion.toInt()

                    // versionCode is supplied by CI as a monotonic build number
                    // (Phase 1 §14). The local default of 1 exists so that a
                    // developer build works without CI environment variables; it
                    // is never what ships, because the release workflow always
                    // provides the real value.
                    versionCode = providers.environmentVariable("CS_VERSION_CODE")
                        .orNull?.toIntOrNull() ?: 1
                    versionName = providers.environmentVariable("CS_VERSION_NAME")
                        .orNull ?: "0.1.0-dev"

                    testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"

                    // Restrict packaged resources to the languages the product
                    // actually ships. Every unshipped locale in a dependency
                    // otherwise adds resources to the APK, and Phase 1 §17 gates
                    // APK growth at 2% — a threshold that stray locales alone
                    // can breach.
                    resourceConfigurations += setOf("en", "hi", "bn", "ta", "te", "mr")
                }

                buildFeatures {
                    // The application module is the one place BuildConfig earns
                    // its keep: variant-specific endpoints are read from it.
                    buildConfig = true
                }

                // -------------------------------------------------------------
                // BUILD VARIANTS
                //
                // Three variants matching the three deployment tiers. They are
                // distinct applicationIdSuffixes so that debug, staging and
                // production can be installed side by side on one device — an
                // engineer testing a staging build must not have to uninstall
                // the production app they use daily, and QA must be able to
                // compare two builds on the same handset.
                // -------------------------------------------------------------
                buildTypes {
                    debug {
                        applicationIdSuffix = ".debug"
                        versionNameSuffix = "-debug"
                        isDebuggable = true
                        isMinifyEnabled = false
                        isShrinkResources = false
                    }

                    create("staging") {
                        // initWith(release) inherits the release configuration,
                        // so staging exercises the SAME minification and
                        // shrinking as production. A staging build that skips
                        // R8 does not test what ships, and R8-only defects then
                        // reach production undetected — a recurring and
                        // expensive class of Android incident.
                        initWith(getByName("release"))
                        applicationIdSuffix = ".staging"
                        versionNameSuffix = "-staging"
                        isDebuggable = false
                        matchingFallbacks += listOf("release")
                        // Staging is signed with the debug key so it can be
                        // installed without release credentials. It never
                        // reaches the Play Store.
                        signingConfig = signingConfigs.getByName("debug")
                    }

                    release {
                        isDebuggable = false
                        isMinifyEnabled = true
                        isShrinkResources = true
                        proguardFiles(
                            getDefaultProguardFile("proguard-android-optimize.txt"),
                            "proguard-rules.pro",
                        )
                        // Signing config is injected by the release workflow from
                        // the secret manager. It is deliberately absent here:
                        // a keystore path or password in a build file is a
                        // Phase 1 §12 violation, and one that is easy to commit
                        // by accident.
                    }
                }

                // NOTE ON APK FILE NAMING:
                //
                // Renaming the output artefact deliberately is NOT done here.
                // The legacy `applicationVariants` DSL does not exist on
                // ApplicationExtension in AGP 8, and the usual workaround casts
                // to com.android.build.gradle.internal.api.BaseVariantOutputImpl
                // — an INTERNAL AGP class that carries no compatibility
                // guarantee and has broken across several AGP releases.
                //
                // AGP's default names (app-debug.apk, app-release.apk) are
                // unambiguous within a single-application build, and the release
                // workflow renames the artefact on upload where the version is
                // already known. Reaching into AGP internals to save a rename
                // step in CI is a bad trade: it makes every AGP upgrade a
                // potential build break.

                testOptions {
                    unitTests {
                        isReturnDefaultValues = true
                        isIncludeAndroidResources = true
                    }
                }
            }

            dependencies {
                add("implementation", libs.findLibrary("androidx.core.ktx").get())
                add("implementation", libs.findLibrary("kotlinx.coroutines.android").get())
                // Installs the baseline profile at first run, which measurably
                // improves cold start. Without this dependency a generated
                // profile is packaged but never applied.
                add("implementation", libs.findLibrary("androidx.profileinstaller").get())

                add("testImplementation", libs.findBundle("unit.test").get())
                add("testRuntimeOnly", libs.findLibrary("junit.jupiter.engine").get())
                add("testRuntimeOnly", libs.findLibrary("junit.vintage.engine").get())
                add("androidTestImplementation", libs.findBundle("android.test").get())
            }
        }
    }
}
