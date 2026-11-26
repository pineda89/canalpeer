package com.canalpeer.tv

import android.app.Activity
import android.view.Gravity
import android.view.KeyEvent
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.LinearLayout
import android.widget.TextView
import androidx.core.content.ContextCompat

/** One setting: a title, its options, a help line for the chosen one. All indexes point into [options]. */
data class SettingRow(
    val title: Int,
    val options: List<Int>,
    val help: (Int) -> Int,
    val get: () -> Int,
    val set: (Int) -> Unit,
)

val vpnModes = listOf("auto", "on", "off")

fun vpnModeIndex(mode: String): Int = vpnModes.indexOf(mode).coerceAtLeast(0)

/** Applies option [i] only if it differs from the current one, so an edge press doesn't re-fire the side effects. */
fun SettingRow.choose(i: Int): Boolean = (i != get()).also { if (it) set(i) }

/** The option a Left/Right press lands on: the neighbour, stopping at the ends. */
fun nextIndex(current: Int, size: Int, delta: Int): Int = (current + delta).coerceIn(0, size - 1)

fun Activity.settingRows(onVpn: (String) -> Unit, onDecoding: (Boolean) -> Unit): List<SettingRow> = listOf(
    SettingRow(
        R.string.vpn, listOf(R.string.vpn_auto, R.string.vpn_on, R.string.vpn_off),
        { listOf(R.string.vpn_auto_help, R.string.vpn_on_help, R.string.vpn_off_help)[it] },
        { vpnModeIndex(vpnMode) },
        { vpnMode = vpnModes[it]; onVpn(vpnMode) },
    ),
    SettingRow(
        R.string.decoding, listOf(R.string.decoding_auto, R.string.decoding_software),
        { if (it == 1) R.string.video_software_hint else R.string.video_auto_hint },
        { if (softwareVideo) 1 else 0 },
        { softwareVideo = it == 1; onDecoding(softwareVideo) },
    ),
)

/**
 * The settings side panel, slid in over the right of [root] (a FrameLayout) so the picture stays
 * visible. The activity sends its key events to [onKey] while [isOpen]. [extra] is a line under the
 * first row (the VPN one), or null for none.
 */
class SettingsPanel(
    private val activity: Activity,
    private val root: ViewGroup,
    private val rows: () -> List<SettingRow>,
    private val extra: () -> String?,
) {
    private val panel: View = LayoutInflater.from(activity).inflate(R.layout.settings_panel, root, false)
    private val column: LinearLayout = panel.findViewById(R.id.settings_rows)
    private var shown = emptyList<SettingRow>()
    private var options = emptyList<List<Button>>() // per row
    private var returnFocus: View? = null

    val isOpen get() = panel.visibility == View.VISIBLE

    init {
        panel.isClickable = true // swallow taps on its empty areas
        root.addView(panel)
    }

    /** [returnTo] gets the focus back on close: a tapped ⚙ never had it, so the caller names it. */
    fun open(returnTo: View? = activity.currentFocus) {
        if (isOpen) return
        val density = activity.resources.displayMetrics.density
        panel.layoutParams = (panel.layoutParams as android.widget.FrameLayout.LayoutParams).apply {
            width = maxOf((root.width * 0.48f).toInt(), (320 * density).toInt())
            gravity = Gravity.END
        }
        returnFocus = returnTo
        panel.visibility = View.VISIBLE // refresh() ignores a closed panel
        build()
        panel.translationX = panel.layoutParams.width.toFloat()
        panel.animate().translationX(0f).setDuration(150).start()
        options.firstOrNull()?.getOrNull(shown[0].get())?.requestFocus()
    }

    fun close() {
        if (!isOpen) return
        panel.animate().translationX(panel.width.toFloat()).setDuration(150).withEndAction { panel.visibility = View.GONE }.start()
        returnFocus?.requestFocus()
    }

    /** Re-reads the values (and the extra line) after something changed behind the panel. */
    fun refresh() {
        if (!isOpen) return
        shown.forEachIndexed { r, row ->
            val sel = row.get()
            options[r].forEachIndexed { i, b -> b.setTextColor(ContextCompat.getColor(activity, if (i == sel) R.color.accent else R.color.text)) }
            panel.findViewWithTag<View>("row$r").apply {
                findViewById<TextView>(R.id.setting_help).setText(row.help(sel))
                findViewById<TextView>(R.id.setting_extra).apply {
                    val line = if (r == 0) extra() else null
                    text = line
                    visibility = if (line.isNullOrEmpty()) View.GONE else View.VISIBLE
                }
            }
        }
    }

    /** Up/Down between rows, Left/Right change the value now, Back closes. True when the key was used. */
    fun onKey(keyCode: Int): Boolean {
        if (!isOpen) return false
        val r = options.indexOfFirst { row -> row.any { it.isFocused } }.coerceAtLeast(0)
        when (keyCode) {
            KeyEvent.KEYCODE_BACK -> close()
            KeyEvent.KEYCODE_DPAD_UP, KeyEvent.KEYCODE_DPAD_DOWN -> {
                val to = nextIndex(r, shown.size, if (keyCode == KeyEvent.KEYCODE_DPAD_UP) -1 else 1)
                options[to][shown[to].get()].requestFocus()
            }
            KeyEvent.KEYCODE_DPAD_LEFT, KeyEvent.KEYCODE_DPAD_RIGHT -> {
                val row = shown[r]
                choose(r, nextIndex(row.get(), row.options.size, if (keyCode == KeyEvent.KEYCODE_DPAD_LEFT) -1 else 1))
            }
            else -> return false
        }
        return true
    }

    private fun choose(r: Int, i: Int) {
        shown[r].choose(i)
        refresh()
        options[r][i].requestFocus()
    }

    private fun build() {
        while (column.childCount > 1) column.removeViewAt(1) // keep the title
        shown = rows()
        val inflater = LayoutInflater.from(activity)
        options = shown.mapIndexed { r, row ->
            val view = inflater.inflate(R.layout.settings_row, column, false)
            view.tag = "row$r"
            view.findViewById<TextView>(R.id.setting_title).setText(row.title)
            column.addView(view)
            val bar = view.findViewById<LinearLayout>(R.id.setting_options)
            row.options.mapIndexed { i, label ->
                Button(activity).apply {
                    setText(label)
                    isAllCaps = false
                    textSize = 15f
                    typeface = androidx.core.content.res.ResourcesCompat.getFont(activity, R.font.outfit)
                    stateListAnimator = null
                    background = ContextCompat.getDrawable(activity, R.drawable.recent_row_bg)
                    minimumWidth = 0
                    setPadding(0, 0, 0, 0)
                    setOnClickListener { choose(r, i) }
                    bar.addView(this, LinearLayout.LayoutParams(0, (48 * activity.resources.displayMetrics.density).toInt(), 1f).apply { if (i > 0) marginStart = (8 * activity.resources.displayMetrics.density).toInt() })
                }
            }
        }
        refresh()
    }
}
