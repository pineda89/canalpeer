package com.canalpeer.tv

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class CutTrackerTest {
    private var t = 0L
    private val tracker = CutTracker { t }

    @Test fun bufferingBeforeFirstReadyIsNotACut() {
        tracker.onBuffering()
        assertNull(tracker.onReady())
        assertEquals(0, tracker.count)
    }

    @Test fun rebufferIsOneCutWithDuration() {
        tracker.onReady()
        t = 1000; tracker.onBuffering()
        t = 2000; tracker.onBuffering()
        t = 4000
        assertEquals(Cut(1000, 3000, "rebuffer"), tracker.onReady())
        assertEquals(1, tracker.count)
    }

    @Test fun errorAfterReadyCountsOnce() {
        tracker.onReady()
        tracker.onError()
        tracker.onBuffering()
        assertEquals("error", tracker.onReady()!!.reason)
        assertEquals(1, tracker.count)
    }

    @Test fun readyWithoutCutReturnsNull() {
        tracker.onReady()
        assertNull(tracker.onReady())
    }

    @Test fun openForMeasuresOnlyAnOpenCut() {
        tracker.onReady()
        assertEquals(0L, tracker.openForMs())
        t = 1000; tracker.onBuffering()
        t = 9500
        assertEquals(8500L, tracker.openForMs())
        tracker.onReady()
        assertEquals(0L, tracker.openForMs())
    }

    @Test fun aSeekIsNotACut() {
        tracker.onReady()
        tracker.onSeek()
        tracker.onBuffering()
        assertNull(tracker.onReady())
        assertEquals(0, tracker.count)
        tracker.onBuffering() // the next stall is real again
        tracker.onReady()
        assertEquals(1, tracker.count)
    }
}
