plugins {
    alias(libs.plugins.android.library)
    alias(libs.plugins.kotlin.android)
}

android {
    namespace = "io.chepherd.rc.transport"
    compileSdk = 34
    defaultConfig { minSdk = 26 }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
}

dependencies {
    implementation(project(":core:protocol"))
    implementation(libs.okhttp)
    implementation(libs.kotlinx.coroutines.android)
    // WebRTC binding is reserved for Wave 6 (WebRTCTransport.kt).
    // Removed from compile classpath until that wave lands so the
    // currently-shipped WSTransport-only build stays green in CI.
    // implementation(libs.webrtc)
}
