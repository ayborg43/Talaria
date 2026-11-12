package app.talaria.data

import java.net.URI

/**
 * Which server address the app will use. Talaria is self-hosted, so an address on the home network or a private VPN
 * may be plain http; anything else carries a password and a voice across the internet and must be https.
 */
object Servers {
    class Bad(message: String) : Exception(message)

    /** The origin the user meant ("https://host:port", no path), or a [Bad] that says what to fix. */
    fun normalize(input: String): String {
        var s = input.trim()
        if (s.isEmpty()) throw Bad("Enter the address of your Talaria server.")
        val explicit = Regex("^[a-zA-Z][a-zA-Z0-9+.-]*://").containsMatchIn(s)
        if (!explicit) s = "placeholder://$s"
        val uri = try { URI(s) } catch (_: Exception) { throw Bad("That does not look like an address.") }
        val host = uri.host ?: throw Bad("That does not look like an address.")
        val scheme = when {
            explicit && uri.scheme in setOf("http", "https") -> uri.scheme
            explicit -> throw Bad("Use an address that starts with https:// or http://.")
            isPrivate(host) -> "http"
            else -> "https"
        }
        if (scheme == "http" && !isPrivate(host)) {
            throw Bad("This address would send your password over the internet in the clear. Use https://.")
        }
        val port = if (uri.port > 0) ":${uri.port}" else ""
        val shownHost = if (host.contains(':') && !host.startsWith("[")) "[$host]" else host
        return "$scheme://$shownHost$port"
    }

    /** On this device or this network: loopback, the emulator's host alias, RFC 1918, link-local, Tailscale, .local. */
    fun isPrivate(host: String): Boolean {
        val h = host.lowercase().trim('[', ']')
        if (h == "localhost" || h == "10.0.2.2" || h.endsWith(".local") || h.endsWith(".lan") || h.endsWith(".internal") || h.endsWith(".ts.net")) return true
        if (h == "::1" || h.startsWith("fe80:") || h.startsWith("fc") || h.startsWith("fd")) return h.contains(':')
        val p = h.split('.')
        if (p.size == 4 && p.all { it.toIntOrNull() in 0..255 }) {
            val a = p[0].toInt(); val b = p[1].toInt()
            return a == 10 || a == 127 || (a == 192 && b == 168) || (a == 172 && b in 16..31) || (a == 169 && b == 254) || (a == 100 && b in 64..127)
        }
        return !h.contains('.') // a bare name such as "mini" is resolved on the local network
    }

    fun display(origin: String): String = origin.removePrefix("https://").removePrefix("http://")
}
