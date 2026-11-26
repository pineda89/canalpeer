package com.canalpeer.tv

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class SendRulesTest {
    @Test fun playingGoesOn() {
        assertNull(endRule(listOf("PLAYING", "PLAYING"), 300))
        assertNull(endRule(listOf("TRANSITIONING"), 0))
    }

    @Test fun twoStoppedPollsEndIt() {
        assertEquals("tv_stopped", endRule(listOf("PLAYING", "STOPPED", "STOPPED"), 200))
        assertEquals("tv_stopped", endRule(listOf("PLAYING", "STOPPED", "NO_MEDIA_PRESENT"), 200))
        assertNull(endRule(listOf("PLAYING", "STOPPED"), 200)) // one may be a blip
        assertNull(endRule(listOf("STOPPED", "PLAYING"), 200))
    }

    // Many renderers stay STOPPED while they buffer the TS: only a TV that played stops.
    @Test fun stoppedBeforePlayingIsBuffering() {
        assertNull(endRule(listOf("STOPPED", "STOPPED"), 200))
        assertNull(endRule(listOf("TRANSITIONING", "STOPPED", "NO_MEDIA_PRESENT"), 200))
    }

    // The TV switched off: it answers nothing ("") and reads nothing.
    @Test fun tenSecondsWithoutReadingEndIt() {
        assertNull(endRule(listOf("PLAYING", ""), 9_999))
        assertEquals("tv_not_reading", endRule(listOf("PLAYING", "PLAYING"), 10_000))
    }

    @Test fun beforeTheFirstReadItIsNotEnded() {
        assertNull(endRule(listOf("TRANSITIONING", "", "", "", ""), -1))
    }

    // A TV that took the URI and never asked for it does not hold a send for ever.
    @Test fun thirtySecondsWithoutAFirstReadEndIt() {
        assertEquals("tv_not_reading", endRule(List(6) { "TRANSITIONING" }, -1))
    }

    // What the channel is shows only once the core resolved it.
    @Test fun dlnaOnlyForAKnownLiveChannel() {
        assertTrue(liveChannel("playing", vod = false))
        assertTrue(liveChannel("searching", vod = false))
        assertTrue(liveChannel("reconnecting", vod = false))
        assertFalse(liveChannel("resolving", vod = false)) // a movie looks live here
        assertFalse(liveChannel(null, vod = false)) // no status yet
        assertFalse(liveChannel("failed", vod = false))
        assertFalse(liveChannel("playing", vod = true))
    }

    // HTTP_STALLING: a paused TV simply stops reading and the pipe fills.
    @Test fun aPausedTvIsNotIdle() {
        assertNull(endRule(listOf("PLAYING", "PAUSED_PLAYBACK"), 60_000))
        assertNull(endRule(listOf("PLAYING", "PAUSED_PLAYBACK", "PAUSED_PLAYBACK"), 60_000))
        assertEquals("tv_not_reading", endRule(listOf("PAUSED_PLAYBACK", "PLAYING"), 60_000)) // resumed: normal rules
    }

    // The TV switched off: two silent polls after it played end it, no 10 s wait.
    @Test fun aTvThatStopsAnsweringEndsIt() {
        assertEquals("tv_gone", endRule(listOf("PLAYING", "", ""), 0))
        assertNull(endRule(listOf("PLAYING", ""), 0)) // one may be a blip
        assertNull(endRule(listOf("PLAYING", "", "PLAYING"), 0))
        assertNull(endRule(listOf("TRANSITIONING", "", ""), -1)) // never played
    }

    @Test fun theDlnaLineSaysWhy() {
        assertEquals("movie", dlnaBlock("playing", vod = true))
        assertEquals("soon", dlnaBlock("resolving", vod = false))
        assertEquals("soon", dlnaBlock("failed", vod = false))
        assertEquals("soon", dlnaBlock(null, vod = false))
        assertNull(dlnaBlock("playing", vod = false))
        assertNull(dlnaBlock("searching", vod = false))
    }
}
