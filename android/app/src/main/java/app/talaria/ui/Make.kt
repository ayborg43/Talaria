package app.talaria.ui

import app.talaria.data.ChatMeta
import app.talaria.data.FileMeta
import java.time.Duration
import java.time.Instant

/** What the agent can make, as the Make screen lists it. */
enum class MakeKind(val key: String, val label: String, val one: String, val ask: String) {
    Doc("doc", "Docs", "Document", "Make a document about "),
    Page("page", "Pages", "Web page", "Make a web page for "),
    Slides("slides", "Slides", "Slides", "Make a slide deck about "),
    Dashboard("dashboard", "Dashboards", "Dashboard", "Make a dashboard of "),
    Pdf("pdf", "PDFs", "PDF", "");

    companion object {
        fun of(key: String): MakeKind? = entries.firstOrNull { it.key == key }
        val startable = listOf(Doc, Page, Slides, Dashboard)
    }
}

/** The kind of a made file, or "" if it is not one of the made kinds. */
fun kindOf(f: FileMeta): String = f.kind.ifBlank { if (f.mime == "application/pdf") "pdf" else "" }

/** Files the agent made, newest first. */
fun madeFiles(all: List<FileMeta>): List<FileMeta> =
    all.filter { it.origin == "generated" && MakeKind.of(kindOf(it)) != null }.sortedByDescending { it.created }

/** "expires in 5h", "expires in 2d", or "expiring". */
fun expiresIn(created: String, retentionHours: Int, now: Instant = Instant.now()): String {
    val t = parseTime(created) ?: return ""
    val m = Duration.between(now, t.plus(Duration.ofHours(retentionHours.toLong()))).toMinutes()
    return when {
        m <= 0 -> "expiring"
        m < 60 -> "expires in ${m}m"
        m < 2880 -> "expires in ${m / 60}h"
        else -> "expires in ${m / 1440}d"
    }
}

fun fileSize(b: Long): String = when {
    b < 1024 -> "$b B"
    b < 1 shl 20 -> "${b / 1024} KB"
    else -> String.format(java.util.Locale.US, "%.1f MB", b / 1048576.0)
}

/** Pinned conversations first (in the order they were listed), then the rest; only those matching the search words. */
fun arrangeChats(chats: List<ChatMeta>, pinned: Set<String>, query: String): List<ChatMeta> {
    val q = query.trim().lowercase()
    val shown = if (q.isEmpty()) chats else chats.filter { it.title.lowercase().contains(q) || it.agent.lowercase().contains(q) }
    return shown.filter { it.id in pinned } + shown.filter { it.id !in pinned }
}

/** Messages containing every word typed, for searching inside a conversation. */
fun <T> searchIn(items: List<T>, text: (T) -> String, query: String): List<T> {
    val words = query.trim().lowercase().split(Regex("\\s+")).filter { it.isNotEmpty() }
    if (words.isEmpty()) return items
    return items.filter { val t = text(it).lowercase(); words.all { w -> t.contains(w) } }
}

private val UPLOADED_TITLE = Regex("^Uploaded (.+?) — /api/files/.*$")

/** A conversation's name as listed: one that began with an upload is named for the file, not the line the server wrote. */
fun chatTitle(title: String): String = UPLOADED_TITLE.matchEntire(title.trim())?.let { "Sent ${it.groupValues[1]}" } ?: title.ifBlank { "New chat" }
