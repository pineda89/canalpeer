package com.canalpeer.tv

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class OverlayTest {
    @Test fun reconnectingIsHiddenWhileVideoPlays() {
        // A long pause leaves minutes queued: a stalled swarm must not cover working video.
        assertEquals(Overlay.NONE, overlayFor("reconnecting", paused = false, buffering = false, everReady = true, bufferingMs = 0))
        assertEquals(Overlay.RECONNECTING, overlayFor("reconnecting", paused = false, buffering = true, everReady = true, bufferingMs = 100))
    }

    @Test fun subSecondHiccupIsNotAnnounced() {
        assertEquals(Overlay.NONE, overlayFor("playing", paused = false, buffering = true, everReady = true, bufferingMs = 500))
        assertEquals(Overlay.RECONNECTING, overlayFor("playing", paused = false, buffering = true, everReady = true, bufferingMs = 2500))
    }

    @Test fun startupAndPause() {
        assertEquals(Overlay.LOADING, overlayFor("playing", paused = false, buffering = true, everReady = false, bufferingMs = 9000))
        assertEquals(Overlay.PAUSED, overlayFor("playing", paused = true, buffering = false, everReady = true, bufferingMs = 0))
        assertEquals(Overlay.FAILED, overlayFor("failed", paused = false, buffering = false, everReady = false, bufferingMs = 0))
    }

    @Test fun reopenOnlyWhenDataIsWaiting() {
        assertTrue(shouldReopen("playing", queuedMs = 3000, stuckMs = 8000, sinceLastReopenMs = 60_000))
        assertFalse(shouldReopen("playing", queuedMs = 0, stuckMs = 20_000, sinceLastReopenMs = 60_000))
        assertFalse(shouldReopen("reconnecting", queuedMs = 3000, stuckMs = 20_000, sinceLastReopenMs = 60_000))
        assertFalse(shouldReopen("playing", queuedMs = 3000, stuckMs = 7000, sinceLastReopenMs = 60_000))
        assertFalse(shouldReopen("playing", queuedMs = 3000, stuckMs = 20_000, sinceLastReopenMs = 2000))
    }
}
