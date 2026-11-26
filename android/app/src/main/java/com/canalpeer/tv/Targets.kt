package com.canalpeer.tv

import java.net.InetSocketAddress
import java.net.Proxy
import java.net.Socket
import org.json.JSONArray

enum class Kind { CANALPEER, DLNA }

/** A TV the phone can send to: a CanalPeer TV (ip:port, its receiver) or a DLNA renderer (its control URL). */
data class Target(val name: String, val ip: String, val kind: Kind, val port: Int = 0, val control: String = "")

/** One row per TV: a TV found both ways is the CanalPeer one (it plays the channel itself). By name. */
fun mergeTargets(canalpeer: List<Target>, dlna: List<Target>): List<Target> =
    (canalpeer + dlna).distinctBy { it.ip }.sortedBy { it.name.lowercase() }

/** A DLNA TV gets a live stream from the phone, which a movie is not. */
fun targetEnabled(t: Target, isMovie: Boolean): Boolean = !(isMovie && t.kind == Kind.DLNA)

/** Mobile.dlnaSearch's JSON; a TV without a name shows its IP. */
fun parseDlna(json: String): List<Target> = try {
    val a = JSONArray(json)
    (0 until a.length()).map { i ->
        val o = a.getJSONObject(i)
        val ip = o.getString("ip")
        Target(o.optString("name").ifBlank { ip }, ip, Kind.DLNA, control = o.getString("control"))
    }
} catch (e: Exception) {
    emptyList()
}

/**
 * POSTs to a CanalPeer TV's receiver; true on 200. Straight to the LAN, never
 * through WARP's proxy, on a plain socket: the app's network security config
 * allows cleartext HTTP only to 127.0.0.1, and a LAN IP cannot be listed there.
 * Blocking: call it off the main thread.
 */
fun Target.post(path: String, body: String = ""): Boolean = try {
    Socket(Proxy.NO_PROXY).use { s ->
        s.connect(InetSocketAddress(ip, port), 4000)
        s.soTimeout = 4000
        val b = body.toByteArray()
        s.getOutputStream().write(("POST $path HTTP/1.0\r\nHost: $ip:$port\r\nContent-Type: application/json\r\n" +
            "Content-Length: ${b.size}\r\n\r\n").toByteArray() + b)
        s.getInputStream().bufferedReader().readLine()?.split(' ')?.getOrNull(1) == "200" // "HTTP/1.1 200 OK"
    }
} catch (e: java.io.IOException) {
    false
}
