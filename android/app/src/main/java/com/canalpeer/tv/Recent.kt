package com.canalpeer.tv

import android.content.Context
import org.json.JSONArray

// The channels this device played, as the core lists them (Mobile.history).
data class RecentEntry(val id: String, val name: String, val at: Long)

fun parseRecent(json: String): List<RecentEntry> = try {
    val a = JSONArray(json)
    (0 until a.length()).map { i ->
        a.getJSONObject(i).let { RecentEntry(it.getString("id"), it.optString("name"), it.optLong("at")) }
    }
} catch (e: Exception) {
    emptyList()
}

// How long ago a channel played, worded by the launcher in the viewer's language.
sealed interface Ago {
    object Now : Ago
    data class Minutes(val n: Int) : Ago
    data class Hours(val n: Int) : Ago
    object Yesterday : Ago
    data class Days(val n: Int) : Ago
}

fun ago(nowSec: Long, atSec: Long): Ago {
    val d = nowSec - atSec
    return when {
        d < 60 -> Ago.Now // a clock moved back lands here too
        d < 3_600 -> Ago.Minutes((d / 60).toInt())
        d < 86_400 -> Ago.Hours((d / 3_600).toInt())
        d < 2 * 86_400 -> Ago.Yesterday
        else -> Ago.Days((d / 86_400).toInt())
    }
}

// An unnamed channel is told apart by its id's ends.
fun shortId(id: String): String = if (id.length <= 8) id else id.take(4) + "…" + id.takeLast(4)

// Where the core keeps the list: persistent, and out of Android's cloud backup,
// so it never leaves the device (the cache dir could be wiped by the system).
fun Context.historyDir(): String = noBackupFilesDir.absolutePath
