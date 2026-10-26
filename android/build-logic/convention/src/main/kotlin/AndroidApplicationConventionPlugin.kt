import com.android.build.api.dsl.ApplicationExtension
import org.gradle.api.Plugin
import org.gradle.api.Project
import org.gradle.api.artifacts.VersionCatalogsExtension
import org.gradle.api.tasks.testing.Test
import org.gradle.kotlin.dsl.configure
import org.gradle.kotlin.dsl.dependencies
import org.gradle.kotlin.dsl.getByType
import org.gradle.kotlin.dsl.withType
import java.io.File

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
                    // provides the real value. A value that is set but invalid
                    // fails the build rather than falling back to the default
                    // (ReleaseVersion).
                    versionCode = ReleaseVersion.code(providers.environmentVariable("CS_VERSION_CODE").orNull)
                    versionName = ReleaseVersion.name(providers.environmentVariable("CS_VERSION_NAME").orNull)

                    testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
                }

                androidResources {
                    // Restrict packaged resources to the languages the product
                    // actually ships. Every unshipped locale in a dependency
                    // otherwise adds resources to the APK, and Phase 1 §17 gates
                    // APK growth at 2% — a threshold that stray locales alone
                    // can breach. localeFilters replaces the deprecated
                    // defaultConfig.resourceConfigurations, deprecated in AGP 8.9.
                    localeFilters += setOf("en", "hi", "bn", "ta", "te", "mr")
                }

                buildFeatures {
                    // The application module is the one place BuildConfig earns
                    // its keep: variant-specific endpoints are read from it.
                    buildConfig = true
                }

                // The upload key exists only in the release workflow's
                // environment (ReleaseSigning). Everywhere else none of its
                // variables is set, no "upload" config is created, and release
                // builds stay unsigned.
                val uploadKey = ReleaseSigning.uploadKey(
                    { providers.environmentVariable(it).orNull },
                    { File(it).let { file -> file.isAbsolute && file.isFile } },
                )
                if (uploadKey != null) {
                    signingConfigs.create("upload") {
                        storeFile = File(uploadKey.storeFile)
                        storePassword = uploadKey.storePassword
                        keyAlias = uploadKey.keyAlias
                        keyPassword = uploadKey.keyPassword
                    }
                }

                // A release (the release script sets callscreen.requireReleaseEndpoints)
                // must carry its public endpoint configuration, or it would ship
                // an app that stops at sign-in and connects unpinned
                // (ReleaseEndpoints). Every other build is unaffected.
                if (providers.gradleProperty(ReleaseEndpoints.REQUIRED).orNull == "true") {
                    val problems = ReleaseEndpoints.problems { providers.gradleProperty(it).orNull }
                    require(problems.isEmpty()) {
                        "A release build needs its public endpoint configuration: " + problems.joinToString("; ")
                    }
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

                    release {
                        isDebuggable = false
                        isMinifyEnabled = true
                        isShrinkResources = true
                        proguardFiles(
                            getDefaultProguardFile("proguard-android-optimize.txt"),
                            "proguard-rules.pro",
                        )
                        // Signed with the upload key only when the release
                        // workflow supplies it through CS_UPLOAD_* (above). A
                        // keystore path or password in a build file is a
                        // Phase 1 §12 violation, and one that is easy to commit
                        // by accident.
                        signingConfigs.findByName("upload")?.let { signingConfig = it }
                    }

                    // release is configured BEFORE staging is created: initWith copies
                    // the configuration as it stands at the call. When staging was
                    // created first it copied release's defaults, so it was neither
                    // minified nor shrunk, and the R8 check it exists for never ran.
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

            // The same unit-test setup as callscreen.android.library. Without
            // useJUnitPlatform() Gradle runs JUnit 4 only, so the app's JUnit 5
            // tests were compiled but never executed, and the task still passed.
            tasks.withType<Test>().configureEach {
                useJUnitPlatform()
                timeout.set(java.time.Duration.ofMinutes(10))
                testLogging {
                    events("failed", "skipped")
                    showStackTraces = true
                    showCauses = true
                    exceptionFormat = org.gradle.api.tasks.testing.logging.TestExceptionFormat.FULL
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
