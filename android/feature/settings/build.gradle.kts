plugins {
    id("callscreen.android.library")
    id("callscreen.android.compose")
    id("callscreen.android.hilt")
}

android {
    namespace = "com.callscreen.feature.settings"
}

dependencies {
    implementation(projects.core.designsystem)
    implementation(projects.core.ui)
    implementation(projects.core.shared)
    implementation(libs.androidx.compose.material.icons.extended)
    // The setup checklist reads the screening role and contacts access.
    implementation(projects.core.telephony)
    // Account deletion: a ViewModel over core:auth's identity flow.
    implementation(projects.core.auth)
    implementation(libs.androidx.lifecycle.viewmodel.compose)
    implementation(libs.androidx.lifecycle.runtime.compose)
    implementation(libs.hilt.navigation.compose)
    // Screens rendered on the JVM with Robolectric (provided by callscreen.android.compose).
    testImplementation(libs.androidx.compose.ui.test.junit4)
    testImplementation(libs.junit4)
}

