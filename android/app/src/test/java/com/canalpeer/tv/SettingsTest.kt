package com.canalpeer.tv

import org.junit.Assert.assertEquals
import org.junit.Test

class SettingsTest {
    @Test fun nextIndexClampsWithoutWrapping() {
        assertEquals(0, nextIndex(0, 3, -1))
        assertEquals(1, nextIndex(0, 3, 1))
        assertEquals(2, nextIndex(2, 3, 1))
    }

    @Test fun vpnModeIndexFallsBackToAutomatic() {
        assertEquals(1, vpnModeIndex("on"))
        assertEquals(0, vpnModeIndex("weird"))
        assertEquals("off", vpnModes[2])
    }

    @Test fun chooseSetsOnlyAChangedValue() {
        var value = 1
        val sets = mutableListOf<Int>()
        val row = SettingRow(0, listOf(0, 0, 0), { 0 }, { value }, { value = it; sets += it })
        assertEquals(false, row.choose(1))
        assertEquals(true, row.choose(2))
        assertEquals(listOf(2), sets)
    }
}
