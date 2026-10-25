plugins {
    id("callscreen.android.library")
    id("callscreen.android.compose")
}

android {
    namespace = "com.callscreen.feature.calls"
}

dependencies {
    implementation(projects.core.designsystem)
    implementation(projects.core.ui)
    implementation(projects.core.shared)
    implementation(libs.androidx.compose.material.icons.extended)
    // F14: noticing that another app took the call screening role, and asking for it back.
    implementation(projects.core.telephony)
    implementation(libs.androidx.activity.compose)
    implementation(libs.androidx.lifecycle.runtime.compose)
    // Screens rendered on the JVM with Robolectric (provided by callscreen.android.compose).
    testImplementation(libs.androidx.compose.ui.test.junit4)
    testImplementation(libs.junit4)
}
