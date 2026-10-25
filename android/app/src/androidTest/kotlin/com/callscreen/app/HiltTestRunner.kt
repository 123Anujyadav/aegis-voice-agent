package com.callscreen.app

import android.app.Application
import android.content.Context
import androidx.test.runner.AndroidJUnitRunner
import dagger.hilt.android.testing.HiltTestApplication

/**
 * Runs instrumented tests under [HiltTestApplication], so a test can replace a
 * production module with a fixture (`@UninstallModules`, `@BindValue`). The
 * app's own Application and modules are unchanged.
 */
public class HiltTestRunner : AndroidJUnitRunner() {
    override fun newApplication(cl: ClassLoader?, className: String?, context: Context?): Application =
        super.newApplication(cl, HiltTestApplication::class.java.name, context)
}
