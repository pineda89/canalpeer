package com.canalpeer.tv

/** Time actually shown since the stream was last (re)opened. */
class PlayClock(private val now: () -> Long) {
    private var shown = 0L
    private var since = -1L // playing since, -1 when not playing

    fun playing(on: Boolean) {
        if (on && since < 0) since = now()
        if (!on && since >= 0) {
            shown += now() - since
            since = -1
        }
    }

    fun reset() {
        shown = 0
        since = -1
    }

    fun shownMs(): Long = shown + if (since >= 0) now() - since else 0
}
