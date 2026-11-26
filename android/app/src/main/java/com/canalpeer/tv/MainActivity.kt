package com.canalpeer.tv

import android.app.Activity
import android.content.ClipData
import android.content.ClipboardManager
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Typeface
import android.net.Uri
import android.os.Bundle
import android.text.Editable
import android.text.SpannableStringBuilder
import android.text.Spanned
import android.text.TextWatcher
import android.text.style.ForegroundColorSpan
import android.text.style.StyleSpan
import android.view.KeyEvent
import android.view.View
import android.view.inputmethod.EditorInfo
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import mobile.Mobile

/** Launcher screen, as the iPhone's: type or paste an id when there is no link to click. */
class MainActivity : Activity() {
    private lateinit var input: EditText
    private lateinit var error: TextView
    private lateinit var settings: SettingsPanel

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)
        Mobile.setVpn(vpnMode, historyDir()) // before anything goes out, the update check included
        Mobile.reportOpen(if (touchDevice(packageManager)) "android" else "android-tv", BuildConfig.VERSION_NAME, vpnMode, historyDir())
        input = findViewById(R.id.input)
        error = findViewById(R.id.error)
        findViewById<TextView>(R.id.wordmark).text = SpannableStringBuilder("canalpeer").apply {
            setSpan(ForegroundColorSpan(getColor(R.color.accent)), 5, 9, Spanned.SPAN_EXCLUSIVE_EXCLUSIVE)
            setSpan(StyleSpan(Typeface.BOLD), 5, 9, Spanned.SPAN_EXCLUSIVE_EXCLUSIVE)
        }
        findViewById<TextView>(R.id.version).text = getString(R.string.version, BuildConfig.VERSION_NAME)
        val gear = findViewById<View>(R.id.settings_button)
        settings = SettingsPanel(this, findViewById(android.R.id.content),
            { settingRows(onVpn = { Mobile.setVpn(it, historyDir()) }, onDecoding = {}) },
            { if (Mobile.vpnInUse()) getString(R.string.vpn_in_use) else null })
        gear.setOnClickListener { settings.open(returnTo = gear) }
        findViewById<Button>(R.id.watch).setOnClickListener { watch() }
        findViewById<Button>(R.id.paste).setOnClickListener { paste() }
        input.setOnEditorActionListener { _, action, _ ->
            (action == EditorInfo.IME_ACTION_GO).also { if (it) watch() }
        }
        input.addTextChangedListener(object : TextWatcher {
            override fun beforeTextChanged(s: CharSequence?, start: Int, count: Int, after: Int) {}
            override fun onTextChanged(s: CharSequence?, start: Int, before: Int, count: Int) {}
            override fun afterTextChanged(s: Editable?) = showError(false)
        })
        // The form is at most 420dp wide, as on the iPhone; full width below that.
        val form = findViewById<View>(R.id.form)
        val max = (420 * resources.displayMetrics.density).toInt()
        form.post { if (form.width > max) form.layoutParams = form.layoutParams.apply { width = max } }
        // On a TV the remote starts in the field; on a phone that would pop the keyboard up.
        if (!packageManager.hasSystemFeature(PackageManager.FEATURE_TOUCHSCREEN)) input.requestFocus()
        Updater.check(this) { release ->
            val banner = findViewById<TextView>(R.id.banner)
            val offerText = getString(R.string.update_banner, release.name)
            var busy = false
            banner.text = offerText
            banner.visibility = View.VISIBLE
            // The APK is ~80 MB, minutes on a TV: the banner shows the download.
            banner.setOnClickListener {
                if (busy) return@setOnClickListener
                Updater.offer(this, release,
                    onProgress = { pct ->
                        busy = true
                        banner.text = if (pct < 0) getString(R.string.update_downloading)
                            else getString(R.string.update_downloading_pct, pct)
                    },
                    onDone = { opened ->
                        busy = false
                        banner.text = if (opened) getString(R.string.update_installer) else offerText
                    })
            }
        }
        Mobile.sync(cacheDir.absolutePath)
        offerNotes()
    }

    // The panel takes the remote's keys (and Back) while it is open.
    override fun dispatchKeyEvent(event: KeyEvent): Boolean =
        (settings.isOpen && event.action == KeyEvent.ACTION_DOWN && settings.onKey(event.keyCode)) ||
            super.dispatchKeyEvent(event)

    // A TV remote cannot paste into the field. The link goes in and the focus moves
    // to Watch, so the next OK plays it.
    private fun paste() {
        val clip = getSystemService(ClipboardManager::class.java).primaryClip
        val text = clip?.takeIf { it.itemCount > 0 }?.getItemAt(0)?.coerceToText(this)?.toString()?.trim()
        if (text.isNullOrEmpty()) {
            Toast.makeText(this, R.string.paste_empty, Toast.LENGTH_LONG).show()
            return
        }
        input.setText(text)
        input.setSelection(text.length)
        findViewById<View>(R.id.watch).requestFocus()
    }

    private fun watch() {
        val id = try {
            Mobile.normalize(input.text.toString().trim())
        } catch (e: Exception) {
            showError(true)
            return
        }
        play(id)
    }

    private fun play(id: String) =
        startActivity(Intent(Intent.ACTION_VIEW, Uri.parse("acestream://$id"), this, PlayerActivity::class.java))

    override fun onStart() {
        super.onStart()
        TvReceiver.start(this)
    }

    override fun onStop() {
        super.onStop()
        TvReceiver.stop()
    }

    override fun onResume() {
        super.onResume()
        renderRecent() // the player may have just added one
    }

    // The channels this device played, under Watch. focusAt: the row to put the
    // focus on after a removal, so the remote does not fall off the list.
    private fun renderRecent(focusAt: Int = -1) {
        val list = findViewById<LinearLayout>(R.id.recent_list)
        val entries = parseRecent(Mobile.history(historyDir()))
        list.removeAllViews()
        val shown = if (entries.isEmpty()) View.GONE else View.VISIBLE
        list.visibility = shown
        findViewById<View>(R.id.recent_title).visibility = shown
        val now = System.currentTimeMillis() / 1000
        entries.forEachIndexed { i, e ->
            val row = layoutInflater.inflate(R.layout.recent_row, list, false)
            row.findViewById<TextView>(R.id.recent_name).text =
                e.name.ifEmpty { getString(R.string.recent_unnamed, shortId(e.id)) }
            row.findViewById<TextView>(R.id.recent_when).text = agoText(ago(now, e.at))
            row.findViewById<TextView>(R.id.recent_id).text = shortId(e.id)
            row.findViewById<View>(R.id.recent_play).apply {
                setOnClickListener { play(e.id) }
                setOnLongClickListener { copyLink(e.id); true }
            }
            row.findViewById<View>(R.id.recent_remove).setOnClickListener {
                Mobile.forget(historyDir(), e.id)
                renderRecent(focusAt = i)
            }
            list.addView(row)
        }
        if (focusAt >= 0) {
            val next = list.getChildAt(minOf(focusAt, list.childCount - 1))
            (next?.findViewById<View>(R.id.recent_play) ?: findViewById<View>(R.id.watch)).requestFocus()
        }
    }

    // The whole link, to paste elsewhere (another device, a chat).
    private fun copyLink(id: String) {
        getSystemService(ClipboardManager::class.java)
            .setPrimaryClip(ClipData.newPlainText("CanalPeer", "acestream://$id"))
        Toast.makeText(this, R.string.recent_copied, Toast.LENGTH_SHORT).show()
    }

    private fun agoText(a: Ago): String = when (a) {
        Ago.Now -> getString(R.string.recent_now)
        is Ago.Minutes -> getString(R.string.recent_minutes, a.n)
        is Ago.Hours -> getString(R.string.recent_hours, a.n)
        Ago.Yesterday -> getString(R.string.recent_yesterday)
        is Ago.Days -> getString(R.string.recent_days, a.n)
    }

    private fun showError(on: Boolean) {
        error.visibility = if (on) View.VISIBLE else View.GONE
        input.setBackgroundResource(if (on) R.drawable.field_bg_error else R.drawable.field_bg)
        input.setPadding((14 * resources.displayMetrics.density).toInt(), 0, (14 * resources.displayMetrics.density).toInt(), 0)
    }
}
