package com.canalpeer.tv

enum class Overlay { NONE, RESOLVING, SEARCHING, LOADING, PAUSED, RECONNECTING, FAILED }

/** What the player screen tells the viewer, from the core phase and the player state. */
fun overlayFor(phase: String, paused: Boolean, buffering: Boolean, everReady: Boolean, bufferingMs: Long): Overlay = when {
    phase == "failed" -> Overlay.FAILED
    phase == "resolving" -> Overlay.RESOLVING
    phase == "searching" -> Overlay.SEARCHING
    paused -> Overlay.PAUSED
    buffering && !everReady -> Overlay.LOADING
    // Only while the picture is actually stopped, and not for sub-second hiccups:
    // the message would alarm more than the blip.
    buffering && (phase == "reconnecting" || bufferingMs > 2_000) -> Overlay.RECONNECTING
    else -> Overlay.NONE
}

const val STUCK_MS = 8_000L

/**
 * Measured on a Fire TV: the player can sit in BUFFERING for a minute while data
 * waits in the core. Reopen only when there is data to read, at most every STUCK_MS.
 */
fun shouldReopen(phase: String, queuedMs: Long, stuckMs: Long, sinceLastReopenMs: Long): Boolean =
    phase == "playing" && queuedMs > 0 && stuckMs >= STUCK_MS && sinceLastReopenMs >= STUCK_MS
