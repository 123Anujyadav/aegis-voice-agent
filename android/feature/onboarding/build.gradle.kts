plugins {
    id("callscreen.android.library")
    id("callscreen.android.compose")
}

android {
    namespace = "com.callscreen.feature.onboarding"
}

dependencies {
    implementation(projects.core.designsystem)
    implementation(projects.core.ui)
    implementation(projects.core.shared)
    implementation(libs.androidx.compose.material.icons.extended)
}

