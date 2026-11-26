plugins {
    id("com.android.application")
}

// The repo's VERSION, shared by every app: "major.minor.patch".
val appVersion = rootProject.file("../VERSION").readText().trim()
val (vMajor, vMinor, vPatch) = appVersion.split('.').map(String::toInt)

android {
    namespace = "com.canalpeer.tv"
    compileSdk = 36

    defaultConfig {
        applicationId = "com.canalpeer.tv"
        minSdk = 24
        targetSdk = 34
        versionCode = vMajor * 10_000 + vMinor * 100 + vPatch
        versionName = appVersion
        ndk { abiFilters += listOf("armeabi-v7a", "arm64-v8a", "x86_64") }
    }

    buildFeatures { buildConfig = true }

    // The key lives outside the repo; scripts/publish-android.sh
    // exports these. Losing it means installed copies can no longer be updated.
    signingConfigs {
        create("release") {
            storeFile = System.getenv("CANALPEER_KEYSTORE")?.let(::file)
            storePassword = System.getenv("CANALPEER_KEYSTORE_PASSWORD")
            keyAlias = "canalpeer"
            keyPassword = System.getenv("CANALPEER_KEYSTORE_PASSWORD")
        }
    }
    buildTypes {
        release { signingConfig = signingConfigs.getByName("release") }
    }
}

dependencies {
    implementation(files("libs/mobile.aar"))
    implementation("androidx.core:core:1.17.0")
    implementation("androidx.media3:media3-exoplayer:1.11.1")
    implementation("androidx.media3:media3-ui:1.11.1")
    // Android has no MP2 decoder (Fire TV neither): channels with MPEG-1 Layer II
    // audio played mute. Jellyfin's build of Media3's FFmpeg audio extension.
    implementation("org.jellyfin.media3:media3-ffmpeg-decoder:1.9.0+1")
    testImplementation("junit:junit:4.13.2")
    // Local unit tests get Android's stub org.json, which throws: the real one.
    testImplementation("org.json:json:20240303")
}
