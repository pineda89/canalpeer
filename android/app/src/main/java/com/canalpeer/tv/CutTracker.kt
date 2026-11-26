package com.canalpeer.tv

data class Cut(val startMs: Long, val durationMs: Long, val reason: String)

/** Counts what the viewer sees as a cut: playback stopping after it had started. */
class CutTracker(private val now: () -> Long) {
    var count = 0
        private set
    private var started = false
    private var openAt = -1L
    private var reason = ""
    private var seeking = false

    /** The viewer jumped: the stall that follows is theirs, not a cut. */
    fun onSeek() {
        seeking = true
    }

    fun onBuffering() {
        if (!seeking) open("rebuffer")
    }

    fun onError() = open("error")

    fun onReady(): Cut? {
        started = true
        seeking = false
        if (openAt < 0) return null
        val cut = Cut(openAt, now() - openAt, reason)
        openAt = -1
        return cut
    }

    /** How long the current cut has lasted, 0 when playing. */
    fun openForMs(): Long = if (openAt < 0) 0 else now() - openAt

    private fun open(why: String) {
        if (!started || openAt >= 0) return
        openAt = now()
        reason = why
        count++
    }
}
