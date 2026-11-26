package com.canalpeer.tv

import android.app.Activity
import android.app.AlertDialog
import android.content.Context
import java.util.Locale
import mobile.Mobile

/** The notes show once after each update, never on a fresh install. */
fun showNotes(seenCode: Int, code: Int, updated: Boolean) = updated && seenCode < code

private const val NOTES_SEEN = "notes_seen"

/**
 * What changed in this version, from core/changelog (the same text as the site
 * and GitHub), in Spanish or English: viewers find new things such as picture in
 * picture without being told.
 */
fun Activity.offerNotes() {
    val prefs = getSharedPreferences("settings", Context.MODE_PRIVATE)
    val info = packageManager.getPackageInfo(packageName, 0)
    if (!showNotes(prefs.getInt(NOTES_SEEN, 0), BuildConfig.VERSION_CODE, info.firstInstallTime != info.lastUpdateTime)) return
    prefs.edit().putInt(NOTES_SEEN, BuildConfig.VERSION_CODE).apply()
    val lang = if (Locale.getDefault().language == "es") "es" else "en"
    val notes = Mobile.notes(BuildConfig.VERSION_NAME, lang).takeIf { it.isNotEmpty() } ?: return
    AlertDialog.Builder(this)
        .setTitle(getString(R.string.whats_new, BuildConfig.VERSION_NAME))
        .setMessage(notes.lines().joinToString("\n\n") { "• $it" })
        .setPositiveButton(android.R.string.ok, null)
        .show()
}
