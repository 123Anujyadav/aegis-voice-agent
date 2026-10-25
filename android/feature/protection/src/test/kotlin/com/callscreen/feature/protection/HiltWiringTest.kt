package com.callscreen.feature.protection

import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test

@DisplayName("Hilt wiring")
class HiltWiringTest {

    @Test
    @DisplayName("CallerListViewModel's Hilt bindings are generated, so hiltViewModel() can create it")
    fun viewModelIsGenerated() {
        // Hilt generates this for each @HiltViewModel only when the module applies
        // its plugin; without it the app compiles but the screen crashes on open.
        val generated = runCatching {
            Class.forName("com.callscreen.feature.protection.CallerListViewModel_HiltModules")
        }
        assertTrue(generated.isSuccess, "CallerListViewModel_HiltModules is missing: apply callscreen.android.hilt")
    }
}
