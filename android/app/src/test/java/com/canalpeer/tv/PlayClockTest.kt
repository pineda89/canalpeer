package com.canalpeer.tv

import org.junit.Assert.assertEquals
import org.junit.Test

class PlayClockTest {
    private var t = 0L
    private val clock = PlayClock { t }

    @Test fun countsOnlyWhilePlaying() {
        clock.playing(true)
        t = 3000; clock.playing(false)
        t = 9000 // paused: does not count
        clock.playing(true)
        t = 10_000
        assertEquals(4000, clock.shownMs())
    }

    @Test fun resetStartsAgainFromTheReopen() {
        clock.playing(true)
        t = 5000; clock.reset()
        t = 6500
        assertEquals(0, clock.shownMs()) // a reopen shows nothing until it plays again
        clock.playing(true)
        t = 7000
        assertEquals(500, clock.shownMs())
    }
}
