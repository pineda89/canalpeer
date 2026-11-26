package com.canalpeer.tv

import android.app.Activity
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import android.net.wifi.WifiManager
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.util.Log
import android.view.Gravity
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.TextView
import java.net.Inet4Address
import kotlin.concurrent.thread
import mobile.Mobile

private const val TAG = "canalpeer"

/**
 * Send to TV, slid in over the right of [root] (a FrameLayout) as the settings panel is. While
 * open it looks for CanalPeer TVs (NSD, as they advertise) and DLNA TVs (an SSDP search every
 * 5 s), holding a multicast lock. A CanalPeer TV that took the channel, or a DLNA TV
 * picked (the caller starts the send), goes to [onSent].
 * Main thread only.
 */
class SendSheet(
    private val activity: Activity,
    private val root: ViewGroup,
    private val onSent: (Target) -> Unit,
) {
    private val main = Handler(Looper.getMainLooper())
    private val panel: View = LayoutInflater.from(activity).inflate(R.layout.send_sheet, root, false)
    private val rows: LinearLayout = panel.findViewById(R.id.send_rows)
    private val status: TextView = panel.findViewById(R.id.send_status)
    private val nsd: NsdManager? = activity.getSystemService(NsdManager::class.java)
    private val wifi: WifiManager? = activity.applicationContext.getSystemService(WifiManager::class.java)
    private var lock: WifiManager.MulticastLock? = null
    private var discovery: NsdManager.DiscoveryListener? = null
    private var searcher: Thread? = null

    // Bumped on every open and close: late callbacks from an earlier opening are dropped.
    @Volatile private var opening = 0
    private var id = ""
    private var block: () -> String? = { "soon" }
    private val canalpeer = LinkedHashMap<String, Target>() // by NSD service name
    private var dlna = emptyList<Target>()
    private val pending = ArrayDeque<NsdServiceInfo>() // found, not resolved yet
    private var resolving = false
    private var sending = false
    private var error: String? = null
    private var shown: Pair<List<Target>, String?>? = null

    val isOpen get() = panel.visibility == View.VISIBLE

    init {
        panel.isClickable = true // swallow taps on its empty areas
        root.addView(panel)
    }

    /**
     * [id] is the channel to send. While [block] (asked again on every [render]: the
     * player learns it once the core resolved the id) says why, DLNA TVs are listed but off.
     */
    fun open(id: String, block: () -> String?) {
        if (isOpen) return
        this.id = id
        this.block = block
        val gen = ++opening
        canalpeer.clear()
        dlna = emptyList()
        pending.clear()
        resolving = false
        error = null
        shown = null
        val density = activity.resources.displayMetrics.density
        panel.layoutParams = (panel.layoutParams as FrameLayout.LayoutParams).apply {
            width = maxOf((root.width * 0.48f).toInt(), (320 * density).toInt())
            gravity = Gravity.END
        }
        panel.visibility = View.VISIBLE
        panel.translationX = panel.layoutParams.width.toFloat()
        panel.animate().translationX(0f).setDuration(150).start()
        render()
        lock = wifi?.createMulticastLock("canalpeer-send")?.apply {
            setReferenceCounted(false)
            acquire()
        }
        browse(gen)
        searcher = thread(name = "dlna-search") {
            while (opening == gen) {
                val found = parseDlna(Mobile.dlnaSearch(2000))
                main.post {
                    if (opening != gen) return@post
                    dlna = found
                    render()
                }
                try {
                    Thread.sleep(3000) // with the 2 s search: one every 5 s
                } catch (e: InterruptedException) {
                    break
                }
            }
        }
    }

    fun close() {
        if (!isOpen) return
        opening++
        searcher?.interrupt()
        searcher = null
        discovery?.let {
            try {
                nsd?.stopServiceDiscovery(it)
            } catch (e: IllegalArgumentException) { // its start had failed
            }
        }
        discovery = null
        lock?.release()
        lock = null
        panel.animate().translationX(panel.width.toFloat()).setDuration(150).withEndAction { panel.visibility = View.GONE }.start()
    }

    private fun browse(gen: Int) {
        val nsd = nsd ?: return
        val listener = object : NsdManager.DiscoveryListener {
            override fun onDiscoveryStarted(type: String) {}
            override fun onDiscoveryStopped(type: String) {}
            override fun onStartDiscoveryFailed(type: String, code: Int) {
                Log.w(TAG, "NSD browse failed: $code")
            }
            override fun onStopDiscoveryFailed(type: String, code: Int) {}
            override fun onServiceFound(info: NsdServiceInfo) {
                main.post {
                    if (opening != gen) return@post
                    pending += info
                    resolveNext(gen)
                }
            }
            override fun onServiceLost(info: NsdServiceInfo) {
                main.post {
                    if (opening != gen) return@post
                    pending.removeAll { it.serviceName == info.serviceName }
                    canalpeer.remove(info.serviceName)
                    render()
                }
            }
        }
        try {
            nsd.discoverServices("_canalpeer._tcp", NsdManager.PROTOCOL_DNS_SD, listener)
            discovery = listener
        } catch (e: Exception) { // odd firmware: DLNA TVs still show
            Log.e(TAG, "NSD browse failed", e)
        }
    }

    // Before Android 14 NSD resolves one service at a time (a second fails with
    // FAILURE_ALREADY_ACTIVE): one after the other.
    private fun resolveNext(gen: Int) {
        if (resolving || opening != gen) return
        val info = pending.removeFirstOrNull() ?: return
        resolving = true
        nsd?.resolveService(info, object : NsdManager.ResolveListener {
            override fun onServiceResolved(r: NsdServiceInfo) {
                main.post {
                    if (opening != gen) return@post
                    resolving = false
                    ipv4(r)?.let { canalpeer[r.serviceName] = Target(r.serviceName, it, Kind.CANALPEER, port = r.port) }
                    render()
                    resolveNext(gen)
                }
            }
            override fun onResolveFailed(r: NsdServiceInfo, code: Int) {
                main.postDelayed({
                    if (opening != gen) return@postDelayed
                    resolving = false
                    // Busy with one from an earlier opening: again in a moment.
                    if (code == NsdManager.FAILURE_ALREADY_ACTIVE) pending.addFirst(info)
                    else Log.w(TAG, "NSD resolve of ${info.serviceName} failed: $code")
                    resolveNext(gen)
                }, if (code == NsdManager.FAILURE_ALREADY_ACTIVE) 300L else 0L)
            }
        })
    }

    // The URL and the dedup with DLNA (IPv4 there) both want the IPv4 address; TVs at home have one.
    @Suppress("DEPRECATION")
    private fun ipv4(r: NsdServiceInfo): String? =
        (if (Build.VERSION.SDK_INT >= 34) r.hostAddresses else listOfNotNull(r.host))
            .firstOrNull { it is Inet4Address }?.hostAddress

    fun render() {
        if (!isOpen) return
        val movie = block()
        val list = mergeTargets(canalpeer.values.toList(), dlna)
        val line = error ?: if (list.isEmpty()) activity.getString(R.string.send_searching) else null
        status.text = line
        status.visibility = if (line == null) View.GONE else View.VISIBLE
        if (list to movie == shown) return
        shown = list to movie
        rows.removeAllViews()
        val inflater = LayoutInflater.from(activity)
        for (t in list) {
            val row = inflater.inflate(R.layout.send_row, rows, false)
            row.findViewById<TextView>(R.id.send_name).text = t.name
            row.findViewById<TextView>(R.id.send_kind).setText(
                if (t.kind == Kind.CANALPEER) R.string.send_kind_canalpeer else R.string.send_kind_dlna)
            val enabled = targetEnabled(t, movie != null)
            row.isEnabled = enabled
            row.alpha = if (enabled) 1f else 0.5f
            row.findViewById<TextView>(R.id.send_reason).setText(
                if (movie == "movie") R.string.send_movie_dlna else R.string.send_dlna_soon)
            row.findViewById<View>(R.id.send_reason).visibility = if (enabled) View.GONE else View.VISIBLE
            row.setOnClickListener { send(t) }
            rows.addView(row)
        }
    }

    private fun send(t: Target) {
        if (sending || !targetEnabled(t, block() != null)) return
        if (t.kind == Kind.DLNA) {
            close()
            onSent(t)
            return
        }
        sending = true
        error = null
        render()
        val gen = opening
        val body = """{"id":"$id"}""" // a normalized id: 40 hex digits, nothing to escape
        thread(name = "send") {
            val ok = t.post("/play", body)
            main.post {
                sending = false
                if (ok) { // even with the sheet closed meanwhile: the TV plays it, this phone stops
                    close()
                    onSent(t)
                } else if (opening == gen) {
                    error = activity.getString(R.string.send_failed, t.name)
                    render()
                }
            }
        }
    }
}
