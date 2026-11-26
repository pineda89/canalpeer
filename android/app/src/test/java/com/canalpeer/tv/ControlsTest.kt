package com.canalpeer.tv

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ControlsTest {
    private var now = 0L
    private val controls = Controls { now }

    @Test fun visibleRightAfterATouch() {
        controls.touched()
        assertTrue(controls.visible(paused = false))
    }

    @Test fun hiddenAfterFourSeconds() {
        controls.touched()
        now = 4_001
        assertFalse(controls.visible(paused = false))
    }

    @Test fun pausedKeepsThemUp() {
        controls.touched()
        now = 10_000
        assertTrue(controls.visible(paused = true))
    }

    @Test fun anotherTouchRestartsTheWait() {
        controls.touched()
        now = 3_000
        controls.touched()
        now = 6_999
        assertTrue(controls.visible(paused = false))
        now = 7_001
        assertFalse(controls.visible(paused = false))
    }

    @Test fun hiddenBeforeAnyTouch() {
        assertFalse(controls.visible(paused = false))
    }

    // Back on a remote closes them while paused too; otherwise it could never leave.
    @Test fun hidingWinsOverPause() {
        controls.touched()
        controls.hide()
        assertFalse(controls.visible(paused = true))
    }

    @Test fun aTouchAfterHidingShowsThemAgain() {
        controls.hide()
        controls.touched()
        assertTrue(controls.visible(paused = true))
    }
}
