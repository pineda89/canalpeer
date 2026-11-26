package com.canalpeer.tv

private val stopped = setOf("STOPPED", "NO_MEDIA_PRESENT")

/**
 * Why a DLNA send ends by itself, or null while it goes on. [states] are the TV's
 * transport states, one per 5 s poll, oldest first ("" when it did not answer);
 * [idleMs] is how long ago it last read the stream while bytes waited for it, -1
 * before its first read. A paused TV is never idle. STOPPED counts only once it played: many renderers stay
 * STOPPED while they buffer.
 */
fun endRule(states: List<String>, idleMs: Long): String? = when {
    "PLAYING" in states && states.size >= 2 && states.takeLast(2).all { it in stopped } -> "tv_stopped"
    "PLAYING" in states && states.size >= 2 && states.takeLast(2).all { it == "" } -> "tv_gone" // switched off
    states.lastOrNull() == "PAUSED_PLAYBACK" -> null // HTTP_STALLING: a paused TV stops reading
    idleMs >= 10_000 -> "tv_not_reading"
    idleMs < 0 && states.size >= 6 -> "tv_not_reading" // 30 s and it never asked for the stream
    else -> null
}

/** A DLNA TV gets only live channels, and the core knows a movie only once it resolved the id. */
fun liveChannel(phase: String?, vod: Boolean): Boolean =
    !vod && phase in setOf("searching", "playing", "reconnecting")

/** Why a DLNA TV is off: "movie" once the core knows it is one, "soon" while it does not know yet; null when it is on. */
fun dlnaBlock(phase: String?, vod: Boolean): String? = when {
    vod -> "movie"
    liveChannel(phase, vod) -> null
    else -> "soon"
}
