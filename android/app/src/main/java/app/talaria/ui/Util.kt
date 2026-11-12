package app.talaria.ui

import java.time.Duration
import java.time.Instant
import java.time.OffsetDateTime
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.util.Locale

fun parseTime(s: String): Instant? = if (s.isBlank()) null else runCatching { OffsetDateTime.parse(s).toInstant() }.getOrNull()

/** "just now", "5 min ago", "2 h ago", "yesterday", or a date. */
fun ago(s: String, now: Instant = Instant.now()): String {
    val t = parseTime(s) ?: return ""
    val d = Duration.between(t, now)
    return when {
        d.isNegative || d.seconds < 45 -> "just now"
        d.toMinutes() < 60 -> "${d.toMinutes().coerceAtLeast(1)} min ago"
        d.toHours() < 24 -> "${d.toHours()} h ago"
        d.toDays() < 2 -> "yesterday"
        d.toDays() < 7 -> "${d.toDays()} days ago"
        else -> DateTimeFormatter.ofPattern("d MMM", Locale.getDefault()).withZone(ZoneId.systemDefault()).format(t)
    }
}

fun clock(s: String): String {
    val t = parseTime(s) ?: return ""
    return DateTimeFormatter.ofPattern("HH:mm", Locale.getDefault()).withZone(ZoneId.systemDefault()).format(t)
}

fun dayAndClock(s: String): String {
    val t = parseTime(s) ?: return ""
    return DateTimeFormatter.ofPattern("EEE d MMM, HH:mm", Locale.getDefault()).withZone(ZoneId.systemDefault()).format(t)
}

fun seconds(ms: Long): String = when {
    ms < 1000 -> "${ms} ms"
    ms < 60_000 -> String.format(Locale.US, "%.1f s", ms / 1000.0)
    else -> "${ms / 60_000} min"
}

fun humanTool(name: String): String = name.replace('_', ' ')

fun elapsed(ageMs: Long): String {
    val s = ageMs / 1000
    return when { s < 60 -> "${s}s"; s < 3600 -> "${s / 60}m ${s % 60}s"; else -> "${s / 3600}h ${(s % 3600) / 60}m" }
}
