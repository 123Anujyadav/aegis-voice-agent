plugins {
    id("callscreen.android.library")
    id("callscreen.android.compose")
    id("callscreen.android.hilt")
}

android {
    namespace = "com.callscreen.feature.onboarding"
}

dependencies {
    implementation(projects.core.designsystem)
    implementation(projects.core.ui)
    implementation(projects.core.shared)
    implementation(libs.androidx.compose.material.icons.extended)
    // Phone verification: a ViewModel over core:auth's identity flow.
    implementation(projects.core.auth)
    implementation(libs.androidx.lifecycle.viewmodel.compose)
    implementation(libs.androidx.lifecycle.runtime.compose)
    implementation(libs.hilt.navigation.compose)
    // A09b: the call screening role request (ScreeningRole) and its system result.
    implementation(projects.core.telephony)
    implementation(libs.androidx.activity.compose)
    // Screens rendered on the JVM with Robolectric (provided by callscreen.android.compose).
    testImplementation(libs.androidx.compose.ui.test.junit4)
    testImplementation(libs.junit4)
}
