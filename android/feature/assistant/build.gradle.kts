plugins {
    id("callscreen.android.library")
    id("callscreen.android.compose")
}

android {
    namespace = "com.callscreen.feature.assistant"
}

dependencies {
    implementation(projects.core.designsystem)
    implementation(projects.core.ui)
    implementation(projects.core.shared)
    implementation(libs.androidx.compose.material.icons.extended)
    // Screens rendered on the JVM with Robolectric (provided by callscreen.android.compose).
    testImplementation(libs.androidx.compose.ui.test.junit4)
    testImplementation(libs.junit4)
}
