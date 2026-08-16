package com.callscreen.app

import android.app.Application
// BuildConfig is generated into the module's `namespace` (com.callscreen), not
// into this file's package (com.callscreen.app), so it requires an explicit
// import. Without it the reference resolves only by accident in modules whose
// package happens to equal their namespace.
import com.callscreen.BuildConfig
import dagger.hilt.android.HiltAndroidApp
import timber.log.Timber

/**
 * Application entry point and dependency-injection graph root.
 *
 * PHASE 2 SCOPE: this establishes process-wide initialisation only — the Hilt
 * graph and logging. No feature wiring exists yet.
 *
 * The class is deliberately minimal and is expected to stay that way. An
 * `Application` subclass runs on the main thread before any UI appears, so every
 * line added here is paid for directly in cold-start time, which is among the
 * metrics gated at release (Phase 1 §17). Anything that can be deferred should
 * be, via `androidx.startup` initialisers or lazy injection.
 */
@HiltAndroidApp
class CallScreenApplication : Application() {

    /**
     * Performs process-wide initialisation.
     *
     * Ordering matters: logging is installed first so that any failure in
     * subsequent initialisation is itself observable rather than silent.
     */
    override fun onCreate() {
        super.onCreate()
        installLogging()
    }

    /**
     * Installs the logging tree appropriate to this build.
     *
     * A debug tree is planted only in debuggable builds. Release builds
     * deliberately plant nothing here yet: the crash-reporting tree that
     * forwards to the backend arrives with `:core:logging`, and planting
     * Timber's `DebugTree` in release would write call metadata to logcat,
     * where any app holding READ_LOGS on a rooted device could read it.
     */
    private fun installLogging() {
        if (BuildConfig.DEBUG) {
            Timber.plant(Timber.DebugTree())
        }
    }
}
