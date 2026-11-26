package com.canalpeer.tv

import android.app.Activity
import android.app.AlertDialog
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.provider.Settings
import android.widget.Toast
import androidx.core.content.FileProvider
import android.util.Log
import java.io.File
import java.net.InetSocketAddress
import java.net.Proxy
import java.net.URL
import kotlin.concurrent.thread
import mobile.Mobile

// Where the current version is announced: the site and the public repo, asked at
// once, so blocking canalpeer.com does not stop updates.
private val LATEST = listOf(
    "https://canalpeer.com/app/latest.json",
    "https://github.com/pineda89/canalpeer/releases/latest/download/latest.json",
)
private const val TIMEOUT_MS = 15_000

data class Release(val name: String, val code: Int, val urls: List<String>)

/** Android's versionCode for "major.minor.patch"; minor and patch stay below 100. */
fun versionCode(name: String): Int {
    val (major, minor, patch) = name.split('.').map(String::toInt)
    return major * 10_000 + minor * 100 + patch
}

/**
 * {"version": "x.y.z", "url": "https://…apk"}, or null for anything else. The
 * digits are bounded: a number versionCode cannot hold would throw on the thread
 * that checks for updates, at every launch.
 */
fun parseLatest(json: String): Release? {
    val version = Regex(""""version"\s*:\s*"(\d{1,4}\.\d{1,2}\.\d{1,2})"""").find(json)?.groupValues?.get(1) ?: return null
    val url = Regex(""""url"\s*:\s*"(https://[^"]+\.apk)"""").find(json)?.groupValues?.get(1) ?: return null
    return Release(version, versionCode(version), listOf(url))
}

/** The newest release any source announces, with the APK of every source that announces it. */
fun pickLatest(bodies: List<String?>): Release? {
    var best: Release? = null
    for (r in bodies.mapNotNull { it?.let(::parseLatest) }) {
        val b = best
        best = when {
            b == null || r.code > b.code -> r
            r.code == b.code -> b.copy(urls = b.urls + r.urls)
            else -> b
        }
    }
    return best
}

// Through the core's local SOCKS proxy while WARP is wanted, so the update check and
// download take the same route as everything else; direct otherwise.
private fun proxy(): Proxy {
    val addr = Mobile.proxyAddr()
    val proxy = if (addr.isEmpty()) Proxy.NO_PROXY
        else Proxy(Proxy.Type.SOCKS, InetSocketAddress("127.0.0.1", addr.substringAfterLast(':').toInt()))
    Log.i("canalpeer", "updater via $proxy")
    return proxy
}

private fun fetch(url: String) = URL(url).openConnection(proxy()).apply {
    connectTimeout = TIMEOUT_MS
    readTimeout = TIMEOUT_MS
}.getInputStream()

object Updater {
    /** Calls back on the main thread with a newer release, or never: no network bothers nobody. */
    fun check(activity: Activity, onNewer: (Release) -> Unit) = thread {
        val bodies = arrayOfNulls<String>(LATEST.size)
        LATEST.mapIndexed { i, url ->
            thread {
                bodies[i] = try {
                    fetch(url).use { it.readBytes().decodeToString() }
                } catch (e: Exception) {
                    null
                }
            }
        }.forEach { it.join() }
        val latest = pickLatest(bodies.toList())
        if (latest != null && latest.code > BuildConfig.VERSION_CODE) activity.runOnUiThread { onNewer(latest) }
    }

    /**
     * Asks, then downloads and opens the installer. [onProgress] gets 0-100 (-1 when the
     * size is unknown) while the APK downloads, on the main thread; [onDone] gets
     * whether the installer opened.
     */
    fun offer(activity: Activity, release: Release, onProgress: (Int) -> Unit = {}, onDone: (Boolean) -> Unit = {}) {
        if (activity.isFinishing) return
        AlertDialog.Builder(activity)
            .setMessage(activity.getString(R.string.update_available, release.name, BuildConfig.VERSION_NAME))
            .setPositiveButton(R.string.update_install) { _, _ ->
                // Before Android 8 the global "unknown sources" switch rules, and it is
                // on: that is how CanalPeer itself got installed.
                if (Build.VERSION.SDK_INT < 26 || activity.packageManager.canRequestPackageInstalls()) {
                    onProgress(0)
                    install(activity, release, onProgress, onDone)
                } else {
                    explainPermission(activity) { onDone(false) }
                }
            }
            .setNegativeButton(R.string.update_later) { _, _ -> onDone(false) }
            .setOnCancelListener { onDone(false) }
            .show()
    }

    // The first update needs "install unknown apps" for CanalPeer. Say so before
    // sending a non-technical viewer into system settings.
    private fun explainPermission(activity: Activity, onDone: () -> Unit) {
        AlertDialog.Builder(activity)
            .setMessage(R.string.update_permission)
            .setPositiveButton(R.string.update_open_settings) { _, _ ->
                activity.startActivity(Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES,
                    Uri.parse("package:${activity.packageName}")))
                onDone()
            }
            .setNegativeButton(R.string.update_later) { _, _ -> onDone() }
            .setOnCancelListener { onDone() }
            .show()
    }

    private fun install(activity: Activity, release: Release, onProgress: (Int) -> Unit, onDone: (Boolean) -> Unit) = thread {
        val opened = try {
            val apk = File(activity.cacheDir, "update.apk")
            // The first source that serves the APK; the next one if it is blocked.
            var error: Exception? = null
            for (url in release.urls) {
                error = try {
                    download(url, apk) { activity.runOnUiThread { onProgress(it) } }
                    null
                } catch (e: Exception) {
                    e
                }
                if (error == null) break
            }
            error?.let { throw it }
            // Android refuses another signature for this package, but would
            // install any other package as a new app from our own prompt.
            if (activity.packageManager.getPackageArchiveInfo(apk.path, 0)?.packageName != activity.packageName) {
                apk.delete()
                throw IllegalStateException("the download is not CanalPeer")
            }
            val uri = FileProvider.getUriForFile(activity, "${activity.packageName}.files", apk)
            activity.startActivity(Intent(Intent.ACTION_VIEW)
                .setDataAndType(uri, "application/vnd.android.package-archive")
                .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK))
            true
        } catch (e: Exception) {
            activity.runOnUiThread {
                Toast.makeText(activity, R.string.update_failed, Toast.LENGTH_LONG).show()
            }
            false
        }
        activity.runOnUiThread { onDone(opened) }
    }

    // Reports whole percents only, so the UI is posted ~100 times, not per buffer.
    private fun download(url: String, to: File, onProgress: (Int) -> Unit) {
        val conn = URL(url).openConnection(proxy()).apply {
            connectTimeout = TIMEOUT_MS
            readTimeout = TIMEOUT_MS
        }
        val total = conn.contentLengthLong
        conn.getInputStream().use { input ->
            to.outputStream().use { out ->
                val buf = ByteArray(64 * 1024)
                var done = 0L
                var shown = -2
                while (true) {
                    val n = input.read(buf)
                    if (n < 0) break
                    out.write(buf, 0, n)
                    done += n
                    val pct = if (total > 0) (done * 100 / total).toInt() else -1
                    if (pct != shown) onProgress(pct).also { shown = pct }
                }
            }
        }
    }
}
