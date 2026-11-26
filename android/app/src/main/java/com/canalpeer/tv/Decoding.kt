package com.canalpeer.tv

import android.content.Context

/**
 * The order to try video decoders in: the device's hardware first, software if it
 * fails to start (ExoPlayer moves to the next one with decoder fallback on). A
 * decoder that started and then [failed] while playing is left out of the next try,
 * unless that would leave none. A viewer whose device shows no picture or stutters
 * can put software first; the hardware stays behind it, so no channel is left without
 * picture. The sort keeps each group's order.
 */
fun <T> decoderOrder(
    all: List<T>,
    softwareFirst: Boolean,
    failed: (T) -> Boolean = { false },
    isSoftware: (T) -> Boolean,
): List<T> = all.filterNot(failed).ifEmpty { all }.sortedBy { isSoftware(it) != softwareFirst }

private const val PREFS = "settings"
private const val SOFTWARE_VIDEO = "software_video"

var Context.softwareVideo: Boolean
    get() = getSharedPreferences(PREFS, Context.MODE_PRIVATE).getBoolean(SOFTWARE_VIDEO, false)
    set(on) = getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit().putBoolean(SOFTWARE_VIDEO, on).apply()

private const val VPN_MODE = "vpn_mode"

/** How the app reaches the network: "auto" (direct, WARP if nothing connects), "on" or "off". */
var Context.vpnMode: String
    get() = getSharedPreferences(PREFS, Context.MODE_PRIVATE).getString(VPN_MODE, "auto") ?: "auto"
    set(mode) = getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit().putString(VPN_MODE, mode).apply()
