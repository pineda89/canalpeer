package com.canalpeer.tv

import android.Manifest
import android.app.Activity
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.view.View
import android.widget.Button
import android.widget.TextView
import kotlin.concurrent.thread

/**
 * The phone after sending the channel to a TV: which TV, and Stop. Back goes to
 * the launcher and the TV keeps playing. A DLNA TV plays what this phone sends
 * (DlnaSendService): its state shows here, and Stop ends the send.
 */
class PlayingOnActivity : Activity() {
    private lateinit var target: Target
    private lateinit var line: TextView
    private val onSend: (DlnaSendService.State) -> Unit = { render() }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_playing_on)
        target = targetOf(intent)
        findViewById<TextView>(R.id.playing_on).text =
            getString(if (target.kind == Kind.DLNA) R.string.sending_to else R.string.playing_on, target.name)
        val stop = findViewById<Button>(R.id.playing_stop)
        val error = findViewById<TextView>(R.id.playing_error)
        line = error
        if (target.kind == Kind.DLNA) {
            stop.setOnClickListener {
                DlnaSendService.stop(this)
                toLauncher()
            }
            askNotifications()
            return
        }
        stop.setOnClickListener {
            stop.isEnabled = false
            error.visibility = View.GONE
            thread(name = "stop") {
                val ok = target.post("/stop")
                runOnUiThread {
                    if (isFinishing || isDestroyed) return@runOnUiThread
                    if (ok) return@runOnUiThread toLauncher()
                    error.text = getString(R.string.send_failed, target.name)
                    error.visibility = View.VISIBLE
                    stop.isEnabled = true
                }
            }
        }
    }

    override fun onStart() {
        super.onStart()
        if (target.kind != Kind.DLNA) return
        DlnaSendService.observe(onSend)
        render()
    }

    override fun onStop() {
        super.onStop()
        DlnaSendService.unobserve(onSend)
    }

    // DLNA: starting, or how the send ended. Stopped from the notification: nothing left to show.
    private fun render() {
        val s = DlnaSendService.state?.takeIf { it.tv == target }
        val text = when {
            s is DlnaSendService.State.Sending && s.starting -> getString(R.string.send_starting)
            s is DlnaSendService.State.Ended && s.reason == null -> return toLauncher()
            s is DlnaSendService.State.Ended ->
                getString(R.string.send_stopped, target.name, DlnaSendService.reasonText(this, s.reason!!))
            !notificationsAllowed() -> getString(R.string.send_notif_permission)
            else -> null
        }
        line.setTextColor(getColor(if (s is DlnaSendService.State.Ended) R.color.err else R.color.muted))
        line.text = text
        line.visibility = if (text == null) View.GONE else View.VISIBLE
        // Ended: the button only goes back (stop() is a no-op then); the screen keeps a way out.
        findViewById<Button>(R.id.playing_stop).setText(if (s is DlnaSendService.State.Ended) R.string.stop else R.string.send_stop)
    }

    // The send's notification has its Stop. Asked once; a refusal does not stop the send.
    private fun askNotifications() {
        if (notificationsAllowed()) return
        val prefs = getSharedPreferences("send", MODE_PRIVATE)
        if (prefs.getBoolean("asked", false)) return
        prefs.edit().putBoolean("asked", true).apply()
        requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1)
    }

    override fun onRequestPermissionsResult(code: Int, permissions: Array<out String>, results: IntArray) {
        if (notificationsAllowed()) DlnaSendService.refresh(this)
        render()
    }

    private fun notificationsAllowed() = Build.VERSION.SDK_INT < 33 ||
        checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED

    @Deprecated("Back leaves the TV playing")
    override fun onBackPressed() = toLauncher()

    companion object {
        private const val EXTRA_NAME = "name"
        private const val EXTRA_IP = "ip"
        private const val EXTRA_KIND = "kind"
        private const val EXTRA_PORT = "port"
        private const val EXTRA_CONTROL = "control"

        fun targetOf(i: Intent) = Target(
            i.getStringExtra(EXTRA_NAME) ?: "",
            i.getStringExtra(EXTRA_IP) ?: "",
            Kind.valueOf(i.getStringExtra(EXTRA_KIND) ?: Kind.CANALPEER.name),
            i.getIntExtra(EXTRA_PORT, 0),
            i.getStringExtra(EXTRA_CONTROL) ?: "",
        )

        fun intent(context: Context, t: Target): Intent = Intent(context, PlayingOnActivity::class.java)
            .putExtra(EXTRA_NAME, t.name)
            .putExtra(EXTRA_IP, t.ip)
            .putExtra(EXTRA_KIND, t.kind.name)
            .putExtra(EXTRA_PORT, t.port)
            .putExtra(EXTRA_CONTROL, t.control)
    }
}
