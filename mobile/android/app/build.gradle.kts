plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.android)
    alias(libs.plugins.compose.compiler)
}

android {
    namespace = "io.chepherd.rc"
    compileSdk = 34
    defaultConfig {
        applicationId = "io.chepherd.rc"
        minSdk = 26
        targetSdk = 34
        versionCode = 1
        versionName = "0.0.1"
        // AppAuth requires this placeholder so its own manifest entries
        // (the redirect-handler activity) get the same scheme our
        // io.chepherd.rc://callback OAuth2 redirect uses.
        manifestPlaceholders["appAuthRedirectScheme"] = "io.chepherd.rc"
    }
    kotlinOptions { jvmTarget = "17" }
    buildFeatures { compose = true }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}

dependencies {
    implementation(project(":core:protocol"))
    implementation(project(":core:transport"))
    implementation(project(":core:auth"))
    implementation(project(":core:style"))

    implementation(libs.core.ktx)
    implementation(libs.lifecycle.runtime.ktx)
    implementation(libs.lifecycle.viewmodel.ktx)
    implementation(libs.activity.compose)
    implementation(libs.kotlinx.coroutines.android)
    implementation(platform(libs.compose.bom))
    implementation(libs.compose.ui)
    implementation(libs.compose.material3)
    debugImplementation(libs.compose.tooling)

    // AppAuth — :app's AuthCoordinator.kt drives the OAuth2 flow
    // directly via net.openid.appauth.AuthorizationService. core:auth
    // exposes the protocol helpers but not the Service/Request classes.
    implementation(libs.appauth)
    // OkHttp — :app's RootView + AuthCoordinator instantiate OkHttpClient
    // directly (the shared transport HTTP client). core:transport uses
    // it internally but doesn't expose it on the api classpath.
    implementation(libs.okhttp)
    // kotlinx.serialization runtime — SessionStore.kt decodes envelopes
    // from kotlinx-serialization-generated @Serializable shapes in
    // :core:protocol. Runtime only — :app doesn't apply the codegen
    // plugin (only :core:protocol does).
    implementation(libs.kotlinx.serialization.json)
}
