package app.talaria.ui

/**
 * Telling a shared agent file from anything else that is shared to Talaria, and naming the file when one is sent out.
 * A shared piece of text goes to the chat as before unless it is plainly an agent file.
 */
object AgentFile {
    /** The most of a shared file that is read: the server takes 8 KB, so anything much longer is not an agent file. */
    const val READ_LIMIT = 20 * 1024

    /** Reads at most [READ_LIMIT] + 1 bytes (so "too long" can be told from "just fits") without `readNBytes`, which needs Android 13. */
    fun readLimited(stream: java.io.InputStream): ByteArray {
        val out = java.io.ByteArrayOutputStream()
        val buf = ByteArray(4096)
        while (out.size() < READ_LIMIT + 1) {
            val n = stream.read(buf, 0, minOf(buf.size, READ_LIMIT + 1 - out.size()))
            if (n < 0) break
            out.write(buf, 0, n)
        }
        return out.toByteArray()
    }

    fun isAgentFileName(name: String?): Boolean = name?.trim()?.endsWith(".agent.md", ignoreCase = true) == true

    /** A `---` header with `talaria-agent:` in it, as `tal` and the web app write. Plain text that merely starts with dashes is not one. */
    fun looksLikeAgentFile(text: String?): Boolean {
        val t = text?.removePrefix("\uFEFF")?.trimStart() ?: return false
        if (!t.startsWith("---")) return false
        val head = t.lineSequence().drop(1).takeWhile { it.trim() != "---" }.take(12)
        return head.any { it.trim().startsWith("talaria-agent:") }
    }

    /** Whether a shared file's own type is one an agent file may arrive as (so its content is worth a look). */
    fun mayBeAgentType(type: String?): Boolean = type == null || type == "text/markdown" || type == "text/x-markdown" || type == "text/plain"

    /** True when a stream shared with [type] and [name] should be read to see if it is an agent file. */
    fun worthReading(type: String?, name: String?): Boolean = isAgentFileName(name) || mayBeAgentType(type)

    /** Whether the shared thing goes to the agent-import screen: a file named `*.agent.md`, or one whose content says so. */
    fun isAgentShare(name: String?, content: String?): Boolean = isAgentFileName(name) || looksLikeAgentFile(content)

    /** "Researcher.agent.md" for the agent named "Researcher": letters and digits kept, anything else a dash, as the server does. */
    fun fileName(agentName: String): String {
        val s = agentName.map { if (it in 'a'..'z' || it in 'A'..'Z' || it in '0'..'9') it else '-' }.joinToString("").trim('-').ifEmpty { "agent" }
        return "$s.agent.md"
    }
}
