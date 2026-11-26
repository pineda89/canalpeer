package com.canalpeer.tv

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class TvReceiverTest {
    // Phones send; only a TV (no touchscreen) receives.
    @Test fun onlyATvAdvertises() {
        assertTrue(advertises(touchscreen = false))
        assertFalse(advertises(touchscreen = true))
    }

    // Launcher to player: the player starts before the launcher stops, so the
    // receiver stays up through the hand-over and goes down with the last screen.
    @Test fun staysUpWhileAnyScreenUsesIt() {
        val users = Users()
        assertTrue(users.up()) // launcher: listen
        assertFalse(users.up()) // player
        assertFalse(users.down()) // launcher stops
        assertTrue(users.down()) // player stops: close
    }

    // A channel queued just before the last screen stopped must not open the player.
    @Test fun inUseOnlyBetweenFirstUpAndLastDown() {
        val users = Users()
        assertFalse(users.inUse)
        users.up()
        users.up()
        users.down()
        assertTrue(users.inUse)
        users.down()
        assertFalse(users.inUse)
    }

    @Test fun aStrayDownDoesNotGoNegative() {
        val users = Users()
        assertFalse(users.down())
        assertTrue(users.up())
    }
}
