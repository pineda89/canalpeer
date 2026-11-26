package com.canalpeer.tv

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class RecentTest {
    @Test fun parsesTheCoreList() = assertEquals(
        listOf(RecentEntry("a", "X", 5)),
        parseRecent("""[{"id":"a","name":"X","at":5}]"""))

    @Test fun malformedIsEmpty() = assertTrue(parseRecent("nope").isEmpty())

    @Test fun justNow() = assertEquals(Ago.Now, ago(1000, 1000))

    // A clock moved back must not print negative numbers.
    @Test fun futureIsJustNow() = assertEquals(Ago.Now, ago(1000, 2000))

    @Test fun minutes() = assertEquals(Ago.Minutes(2), ago(10_000, 10_000 - 125))

    @Test fun hours() = assertEquals(Ago.Hours(2), ago(100_000, 100_000 - 7_300))

    @Test fun yesterday() = assertEquals(Ago.Yesterday, ago(200_000, 200_000 - 90_000))

    @Test fun days() = assertEquals(Ago.Days(3), ago(1_000_000, 1_000_000 - 3 * 86_400 - 5))

    @Test fun shortensIds() = assertEquals("f16d…2c6a", shortId("f16d68fa5ff64f3894a45062ca28e4e66f142c6a"))
}
