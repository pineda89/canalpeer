package com.canalpeer.tv

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import com.sun.net.httpserver.HttpServer
import java.net.InetSocketAddress

class TargetsTest {
    private fun cp(name: String, ip: String) = Target(name, ip, Kind.CANALPEER, port = 4000)
    private fun dlna(name: String, ip: String) = Target(name, ip, Kind.DLNA, control = "http://$ip/ctl")

    // A Fire TV with CanalPeer open that also answers DLNA shows once, as CanalPeer.
    @Test fun sameIpShowsOnceAsCanalPeer() {
        val merged = mergeTargets(listOf(cp("Fire TV", "192.168.1.20")), listOf(dlna("Fire TV DLNA", "192.168.1.20")))
        assertEquals(listOf(cp("Fire TV", "192.168.1.20")), merged)
    }

    // NSD can report one TV twice (one per interface): one row.
    @Test fun duplicatesWithinAKindShowOnce() {
        val merged = mergeTargets(listOf(cp("Salón", "10.0.0.2"), cp("Salón", "10.0.0.2")), listOf(dlna("LG", "10.0.0.3"), dlna("LG", "10.0.0.3")))
        assertEquals(listOf(dlna("LG", "10.0.0.3"), cp("Salón", "10.0.0.2")), merged)
    }

    // By name, whatever the kind and the case.
    @Test fun sortedByName() {
        val merged = mergeTargets(listOf(cp("zeta", "1.1.1.3"), cp("Beta", "1.1.1.2")), listOf(dlna("alpha", "1.1.1.1")))
        assertEquals(listOf("alpha", "Beta", "zeta"), merged.map { it.name })
    }

    @Test fun nothingFound() = assertEquals(emptyList<Target>(), mergeTargets(emptyList(), emptyList()))

    // DLNA sends are live only; CanalPeer TVs play movies themselves.
    @Test fun moviesOnlyGoToCanalPeer() {
        assertTrue(targetEnabled(cp("Fire TV", "1.1.1.1"), isMovie = true))
        assertTrue(targetEnabled(cp("Fire TV", "1.1.1.1"), isMovie = false))
        assertFalse(targetEnabled(dlna("LG", "1.1.1.2"), isMovie = true))
        assertTrue(targetEnabled(dlna("LG", "1.1.1.2"), isMovie = false))
    }

    // A DLNA TV without a friendly name still gets a row to tap.
    @Test fun dlnaListParsed() {
        val json = """[{"name":"LG","ip":"192.168.1.30","control":"http://192.168.1.30:1/ctl"},{"name":"","ip":"192.168.1.31","control":"c"}]"""
        assertEquals(listOf(
            Target("LG", "192.168.1.30", Kind.DLNA, control = "http://192.168.1.30:1/ctl"),
            Target("192.168.1.31", "192.168.1.31", Kind.DLNA, control = "c"),
        ), parseDlna(json))
        assertEquals(emptyList<Target>(), parseDlna("[]"))
        assertEquals(emptyList<Target>(), parseDlna("not json"))
    }

    // The receiver is Go's net/http: 200 is sent, anything else or nobody there is not.
    @Test fun postToACanalPeerTv() {
        val server = HttpServer.create(InetSocketAddress("127.0.0.1", 0), 0)
        var got = ""
        server.createContext("/") { x ->
            got = x.requestMethod + " " + x.requestURI + " " + x.requestBody.readBytes().decodeToString()
            x.sendResponseHeaders(if (x.requestURI.path == "/play") 200 else 404, -1)
            x.close()
        }
        server.start()
        try {
            val tv = Target("TV", "127.0.0.1", Kind.CANALPEER, port = server.address.port)
            assertTrue(tv.post("/play", """{"id":"ab"}"""))
            assertEquals("""POST /play {"id":"ab"}""", got)
            assertFalse(tv.post("/other"))
        } finally {
            server.stop(0)
        }
        assertFalse(Target("TV", "127.0.0.1", Kind.CANALPEER, port = server.address.port).post("/play"))
    }
}
