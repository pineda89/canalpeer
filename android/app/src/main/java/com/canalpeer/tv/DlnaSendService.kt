package com.canalpeer.tv

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import android.net.wifi.WifiManager
import android.os.Build
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.os.PowerManager
import android.util.Log
import androidx.core.app.NotificationCompat
import androidx.core.app.ServiceCompat
import androidx.core.content.ContextCompat
import java.net.Inet4Address
import kotlin.concurrent.thread
import mobile.Mobile
import mobile.Options
import mobile.Session

private const val TAG = "canalpeer"

/**
 * A live channel sent to a DLNA TV: this phone plays it in the core and serves it
 * on the LAN while the TV reads it, under a notification with Stop, whatever the
 * app's screens do. The phone's own player is gone by then: the core's stream has
 * one reader, and the player's retries would take it from the TV.
 */
class DlnaSendService : Service() {
    sealed interface State {
        val tv: Target

        data class Sending(override val tv: Target, val starting: Boolean) : State

        /** [reason]: an [endRule] reason, "failed" or "no_wifi"; null when stopped from the phone. */
        data class Ended(override val tv: Target, val reason: String?) : State
    }

    private val main = Handler(Looper.getMainLooper())
    private var worker: Thread? = null // main thread only
    @Volatile private var ongoing: Notification? = null // the send's notification as last posted

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_REFRESH) { // notifications allowed after the send posted its own
            ongoing?.let { notify(ONGOING, it) }
            return START_NOT_STICKY
        }
        val t = intent?.let(::targetOf)
        val id = intent?.getStringExtra(EXTRA_ID)
        if (intent?.action != ACTION_START || t == null || id == null) { // Stop
            worker?.interrupt() ?: stopSelf()
            return START_NOT_STICKY
        }
        channel()
        ServiceCompat.startForeground(this, ONGOING, sending(t, starting = true).also { ongoing = it },
            if (Build.VERSION.SDK_INT >= 29) ServiceInfo.FOREGROUND_SERVICE_TYPE_MEDIA_PLAYBACK else 0)
        worker?.interrupt() // another send replaces it
        worker = thread(name = "dlna-send") { send(id, t) }
        return START_NOT_STICKY
    }

    override fun onDestroy() {
        worker?.interrupt()
        super.onDestroy()
    }

    private fun send(id: String, t: Target) {
        val me = Thread.currentThread()
        report(me, State.Sending(t, starting = true))
        @Suppress("DEPRECATION") // LOW_LATENCY (29+) is for foreground apps; this one is a service
        val wifiLock = getSystemService(WifiManager::class.java)
            ?.createWifiLock(WifiManager.WIFI_MODE_FULL_HIGH_PERF, "canalpeer-send")?.apply { acquire() }
        val wake = getSystemService(PowerManager::class.java)
            ?.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "canalpeer:send")?.apply { acquire() }
        var session: Session? = null
        var played = false
        var reason: String? = null
        try {
            val ip = wifiIp() ?: return run { reason = "no_wifi" }
            val s = Mobile.start(Options().apply {
                contentID = id
                cacheDir = this@DlnaSendService.cacheDir.absolutePath
                dataDir = historyDir()
                vpn = vpnMode
            })
            session = s
            while (true) {
                val phase = s.status().phase
                if (phase == "playing") break
                if (phase == "failed") return run { reason = "failed" }
                Thread.sleep(1000)
            }
            if (me.isInterrupted) return // stopped during the status call: the TV is never told
            val url = s.share(ip)
            // The core wrote the name to the history as it started playing.
            val title = parseRecent(Mobile.history(historyDir())).firstOrNull { it.id == id }?.name
            played = true
            Mobile.dlnaPlay(t.control, url, title?.ifBlank { null } ?: "CanalPeer")
            report(me, State.Sending(t, starting = false))
            notify(ONGOING, sending(t, starting = false).also { ongoing = it })
            val states = ArrayList<String>()
            while (reason == null && !me.isInterrupted) {
                Thread.sleep(5000)
                states += Mobile.dlnaState(t.control)
                // The channel died here: no reason to blame the TV.
                reason = if (s.status().phase == "failed") "failed" else endRule(states, s.shareIdleMs())
            }
        } catch (e: InterruptedException) { // Stop
        } catch (e: Exception) {
            Log.e(TAG, "DLNA send to ${t.name} failed", e)
            // Stop during a call into the core is still Stop: the interrupt waits for a sleep.
            if (!me.isInterrupted) reason = "failed"
        } finally {
            if (played && reason != "tv_gone") Mobile.dlnaStop(t.control) // a dead TV would only time out
            session?.stop()
            wifiLock?.release()
            wake?.release()
            main.post {
                if (worker !== me) return@post // replaced by another send: its notification stays
                worker = null
                reason?.let { notify(ENDED, ended(t, it)) }
                publish(State.Ended(t, reason))
                ServiceCompat.stopForeground(this, ServiceCompat.STOP_FOREGROUND_REMOVE)
                stopSelf()
            }
        }
    }

    private fun report(me: Thread, s: State) = main.post { if (worker === me) publish(s) }

    // Android 11+ hides the interfaces from Go: the active Wi-Fi network's IPv4, as Android knows it.
    private fun wifiIp(): String? {
        val cm = getSystemService(ConnectivityManager::class.java) ?: return null
        @Suppress("DEPRECATION") // allNetworks: the API 31 callback way is for listening, this is one look
        return cm.allNetworks.filter { cm.getNetworkCapabilities(it)?.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) == true }
            .flatMap { cm.getLinkProperties(it)?.linkAddresses.orEmpty() }
            .map { it.address }.firstOrNull { it is Inet4Address && !it.isLoopbackAddress }?.hostAddress
    }

    private fun channel() {
        if (Build.VERSION.SDK_INT < 26) return
        getSystemService(NotificationManager::class.java).createNotificationChannel(
            NotificationChannel(CHANNEL, getString(R.string.send_to_tv), NotificationManager.IMPORTANCE_LOW))
    }

    private fun notify(n: Int, notification: Notification) {
        try {
            getSystemService(NotificationManager::class.java).notify(n, notification)
        } catch (e: SecurityException) { // notifications refused: the send goes on without
        }
    }

    private fun sending(t: Target, starting: Boolean): Notification {
        val stop = PendingIntent.getService(this, 0, Intent(this, DlnaSendService::class.java),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        return base(t).setContentTitle(getString(R.string.sending_to, t.name))
            .setContentText(if (starting) getString(R.string.send_starting) else null)
            .setOngoing(true)
            .addAction(0, getString(R.string.send_stop), stop)
            .build()
    }

    private fun ended(t: Target, reason: String) = base(t)
        .setContentTitle(getString(R.string.send_stopped, t.name, reasonText(this, reason)))
        .setAutoCancel(true)
        .build()

    private fun base(t: Target) = NotificationCompat.Builder(this, CHANNEL)
        .setSmallIcon(R.drawable.ic_cast)
        .setContentIntent(PendingIntent.getActivity(this, 0, PlayingOnActivity.intent(this, t),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT))

    companion object {
        private const val CHANNEL = "send"
        private const val ONGOING = 1
        private const val ENDED = 2
        private const val ACTION_START = "start"
        private const val ACTION_REFRESH = "refresh"
        private const val EXTRA_ID = "id"

        /** The send now, or how the last one ended; null before any. Main thread. */
        var state: State? = null
            private set
        private val listeners = mutableSetOf<(State) -> Unit>()

        fun observe(l: (State) -> Unit) { listeners += l }
        fun unobserve(l: (State) -> Unit) { listeners -= l }

        private fun publish(s: State) {
            state = s
            listeners.toList().forEach { it(s) }
        }

        /** Sends the live channel [id] to the DLNA TV [t]. Call from the foreground. */
        fun start(context: Context, id: String, t: Target) {
            publish(State.Sending(t, starting = true)) // before PlayingOnActivity looks: not a stale end
            ContextCompat.startForegroundService(context,
                PlayingOnActivity.intent(context, t).setClass(context, DlnaSendService::class.java)
                    .setAction(ACTION_START).putExtra(EXTRA_ID, id))
            context.getSystemService(NotificationManager::class.java)?.cancel(ENDED)
        }

        fun stop(context: Context) {
            if (state is State.Sending) context.startService(Intent(context, DlnaSendService::class.java))
        }

        /** Shows the send's notification again: on Android 13+ it was hidden until notifications were allowed. */
        fun refresh(context: Context) {
            if (state is State.Sending) context.startService(Intent(context, DlnaSendService::class.java).setAction(ACTION_REFRESH))
        }

        private fun targetOf(i: Intent): Target? = PlayingOnActivity.targetOf(i).takeIf { it.control.isNotEmpty() }

        fun reasonText(context: Context, reason: String): String = context.getString(when (reason) {
            "tv_stopped" -> R.string.send_reason_tv_stopped
            "tv_not_reading" -> R.string.send_reason_tv_not_reading
            "tv_gone" -> R.string.send_reason_tv_gone
            "no_wifi" -> R.string.send_reason_no_wifi
            else -> R.string.send_reason_failed
        })
    }
}
