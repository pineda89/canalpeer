package com.canalpeer.tv

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class NotesTest {
    // Versions before this one stored nothing: an update from them still shows the notes.
    @Test fun afterAnUpdate() = assertTrue(showNotes(seenCode = 0, code = 10406, updated = true))

    @Test fun notOnAFreshInstall() = assertFalse(showNotes(seenCode = 0, code = 10406, updated = false))

    @Test fun onlyOncePerVersion() = assertFalse(showNotes(seenCode = 10406, code = 10406, updated = true))

    @Test fun againForTheNextVersion() = assertTrue(showNotes(seenCode = 10406, code = 10407, updated = true))
}
