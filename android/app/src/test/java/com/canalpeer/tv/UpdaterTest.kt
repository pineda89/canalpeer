package com.canalpeer.tv

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class UpdaterTest {
    @Test fun parsesLatestJson() {
        val json = """{ "version": "1.10.3",
            "url": "https://example.com/downloads/canalpeer-1.10.3.apk" }"""
        assertEquals(
            Release("1.10.3", 11003, listOf("https://example.com/downloads/canalpeer-1.10.3.apk")),
            parseLatest(json),
        )
    }

    @Test fun rejectsAnythingElse() {
        assertNull(parseLatest("<!doctype html><html>…</html>")) // the site's fallback page
        assertNull(parseLatest("""{"version": "1.0", "url": "https://x/y.apk"}"""))
        assertNull(parseLatest("""{"version": "1.0.0", "url": "http://x/y.apk"}""")) // https only
        assertNull(parseLatest("""{"version": "10000000000.0.0", "url": "https://x/y.apk"}""")) // no Int holds it
    }

    @Test fun picksTheNewestAndKeepsEveryUrl() {
        val web = """{"version": "1.4.0", "url": "https://canalpeer.com/dl/CanalPeer-1.4.0-android.apk"}"""
        val gh = """{"version": "1.4.0", "url": "https://github.com/pineda89/canalpeer/releases/download/v1.4.0/CanalPeer-1.4.0-android.apk"}"""
        val older = """{"version": "1.3.0", "url": "https://x/old.apk"}"""
        val r = pickLatest(listOf(null, older, web, gh))!!
        assertEquals("1.4.0", r.name)
        assertEquals(2, r.urls.size)
        assertEquals("https://canalpeer.com/dl/CanalPeer-1.4.0-android.apk", r.urls[0])
        assertEquals(1, pickLatest(listOf(null, gh))!!.urls.size) // the web blocked: the repo alone
        assertNull(pickLatest(listOf(null, null)))
    }

    @Test fun versionCodeIsMajorMinorPatch() {
        assertEquals(10203, versionCode("1.2.3"))
    }
}
