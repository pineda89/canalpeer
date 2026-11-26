package com.canalpeer.tv

import android.app.Activity
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.util.Log
import mobile.Mobile
import mobile.Receiver

private const val TAG = "canalpeer"

/** Phones send channels; only a TV receives them. */
fun advertises(touchscreen: Boolean) = !touchscreen

// Many TV boxes (and the Android TV emulator) claim a touchscreen: a TV is a TV.
fun touchDevice(pm: PackageManager) = pm.hasSystemFeature(PackageManager.FEATURE_TOUCHSCREEN) &&
    listOf(PackageManager.FEATURE_LEANBACK, PackageManager.FEATURE_TELEVISION, "amazon.hardware.fire_tv")
        .none(pm::hasSystemFeature)

/** How many screens want the receiver: up() is true for the first, down() for the last. */
class Users {
    private var n = 0
    val inUse get() = n > 0
    fun up() = n++ == 0
    fun down() = n > 0 && --n == 0
}

/**
 * While CanalPeer is on screen on a TV, phones on the LAN find it as
 * _canalpeer._tcp and send it a channel to play, or stop. Each screen calls
 * start in onStart and stop in onStop; the next screen starts before the last
 * one stops, so the receiver stays up across them. Main thread only.
 */
object TvReceiver {
    private val main = Handler(Looper.getMainLooper())
    private val users = Users()
    private var nsd: NsdManager? = null
    private var registration: NsdManager.RegistrationListener? = null

    fun start(activity: Activity) {
        if (!advertises(touchDevice(activity.packageManager))) return
        if (!users.up()) return
        val app = activity.applicationContext
        val port = try {
            Mobile.listen(object : Receiver {
                override fun play(id: String) {
                    main.post { if (users.inUse) play(app, id) } // not if the screens went meanwhile
                }
                override fun stop() {
                    main.post { if (users.inUse) leaveCurrentPlayer() }
                }
            }).toInt()
        } catch (e: Exception) {
            Log.e(TAG, "receiver failed", e)
            return
        }
        Log.i(TAG, "receiver on port $port")
        val name = Settings.Global.getString(app.contentResolver, Settings.Global.DEVICE_NAME)
            .takeUnless { it.isNullOrBlank() } ?: Build.MODEL
        val listener = object : NsdManager.RegistrationListener {
            override fun onServiceRegistered(info: NsdServiceInfo) {
                Log.i(TAG, "advertised as ${info.serviceName}")
            }
            override fun onRegistrationFailed(info: NsdServiceInfo, code: Int) {
                Log.w(TAG, "advertising failed: $code")
            }
            override fun onServiceUnregistered(info: NsdServiceInfo) {}
            override fun onUnregistrationFailed(info: NsdServiceInfo, code: Int) {}
        }
        try {
            nsd = app.getSystemService(NsdManager::class.java).apply {
                registerService(NsdServiceInfo().apply {
                    serviceName = name
                    serviceType = "_canalpeer._tcp"
                    setPort(port)
                }, NsdManager.PROTOCOL_DNS_SD, listener)
            }
            registration = listener
        } catch (e: Exception) { // odd firmware: no NSD. Nothing stays half up.
            Log.e(TAG, "advertising failed", e)
            Mobile.stopListening()
        }
    }

    fun stop() {
        if (!users.down()) return
        registration?.let {
            try {
                nsd?.unregisterService(it)
            } catch (e: IllegalArgumentException) { // its registration had failed
            }
        }
        registration = null
        Mobile.stopListening()
    }

    // NEW_TASK: the app context has no task of its own. A player already on top
    // gets the channel through onNewIntent (singleTop) and switches to it.
    private fun play(context: Context, id: String) = context.startActivity(
        Intent(Intent.ACTION_VIEW, Uri.parse("acestream://$id"), context, PlayerActivity::class.java)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP))
}
