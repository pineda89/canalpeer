package com.canalpeer.tv

import org.junit.Assert.assertEquals
import org.junit.Test

class BackTest {
    @Test fun closesTheControlsFirst() =
        assertEquals(Back.HIDE_CONTROLS, backAction(controlsShown = true, updateWaiting = true))

    @Test fun returnsToWhereverTheLinkCameFrom() =
        assertEquals(Back.CALLER, backAction(controlsShown = false, updateWaiting = false))

    // Updates are offered on the launcher: a viewer who only ever opens links must still see them.
    @Test fun goesToTheLauncherWhenAnUpdateWaits() =
        assertEquals(Back.LAUNCHER, backAction(controlsShown = false, updateWaiting = true))
}
