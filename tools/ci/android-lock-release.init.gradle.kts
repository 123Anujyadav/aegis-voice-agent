// Gradle init script for android-deps-audit.yml: turns on dependency locking
// for :app in that one CI run, so `--write-locks` records what the release
// build resolves (android/app/gradle.lockfile) for OSV-Scanner to audit.
//
// It does not change the repository's build: locking is enabled only by this
// script, and the lockfile is written to the CI checkout and never committed.
allprojects {
    if (path == ":app") {
        dependencyLocking {
            lockAllConfigurations()
        }
    }
}
