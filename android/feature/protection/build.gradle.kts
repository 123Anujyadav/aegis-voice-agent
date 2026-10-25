plugins {
    id("callscreen.android.library")
    id("callscreen.android.compose")
    // CallerListViewModel is a @HiltViewModel: without Hilt's code generation
    // here, hiltViewModel() cannot create it and the screen crashes on open.
    id("callscreen.android.hilt")
}

android {
    namespace = "com.callscreen.feature.protection"
}

dependencies {
    implementation(projects.core.designsystem)
    implementation(projects.core.ui)
    implementation(projects.core.shared)
    implementation(libs.androidx.compose.material.icons.extended)
    // A32/A33/A35: the caller list the on-device pre-filter reads, edited here.
    implementation(projects.core.telephony)
    implementation(libs.androidx.lifecycle.viewmodel.compose)
    implementation(libs.androidx.lifecycle.runtime.compose)
    implementation(libs.hilt.navigation.compose)
}

