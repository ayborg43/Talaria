package app.talaria

import app.talaria.data.Servers
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

class ServersTest {
    @Test fun a_bare_public_name_gets_https() = assertEquals("https://talaria.example.com", Servers.normalize("talaria.example.com"))

    @Test fun a_bare_private_address_gets_http_and_keeps_its_port() {
        assertEquals("http://192.168.1.20:8080", Servers.normalize("192.168.1.20:8080"))
        assertEquals("http://10.0.2.2:18081", Servers.normalize("10.0.2.2:18081"))
    }

    @Test fun paths_and_slashes_are_dropped() = assertEquals("https://talaria.example.com", Servers.normalize(" https://talaria.example.com/app/#chat "))

    @Test fun plain_http_over_the_internet_is_refused() {
        val e = assertThrows(Servers.Bad::class.java) { Servers.normalize("http://talaria.example.com") }
        assertTrue(e.message!!.contains("https"))
    }

    @Test fun other_schemes_and_empty_input_are_refused() {
        assertThrows(Servers.Bad::class.java) { Servers.normalize("ftp://host") }
        assertThrows(Servers.Bad::class.java) { Servers.normalize("   ") }
    }

    @Test fun private_ranges() {
        listOf("localhost", "127.0.0.1", "10.1.2.3", "172.16.0.1", "172.31.255.1", "192.168.0.9", "100.64.0.1", "mini", "box.local", "host.ts.net").forEach { assertTrue(it, Servers.isPrivate(it)) }
        listOf("8.8.8.8", "172.32.0.1", "100.128.0.1", "talaria.example.com", "11.0.0.1").forEach { assertFalse(it, Servers.isPrivate(it)) }
    }

    @Test fun ipv6_loopback_is_private() = assertTrue(Servers.isPrivate("[::1]"))
}
