package app.talaria.ui

import app.talaria.data.DigestSchedule

/** The words on the "while you were away" screen. */
object Digest {
    enum class Span(val hours: Int, val label: String) { Day(24, "Last 24 hours"), Week(168, "Last 7 days") }

    /** "08:00" for anything the time picker can give; null for anything else. */
    fun clock(hour: Int, minute: Int): String? = if (hour in 0..23 && minute in 0..59) "%02d:%02d".format(hour, minute) else null

    fun hourOf(at: String): Int = at.substringBefore(':').toIntOrNull()?.coerceIn(0, 23) ?: 8
    fun minuteOf(at: String): Int = at.substringAfter(':', "").toIntOrNull()?.coerceIn(0, 59) ?: 0

    /** What the schedule says in one line, for under the switch. [next] is already written for reading, or empty. */
    fun scheduleLine(s: DigestSchedule, next: String): String {
        if (!s.enabled) return "Off. Turn it on and Talaria writes a summary into your chat, with nothing to ask a model."
        val at = s.at.ifBlank { "08:00" }
        val when_ = if (s.weekly) "every Monday at $at" else "every day at $at"
        return "Arrives $when_" + (if (next.isNotBlank()) ". Next: $next." else ".")
    }

    /** When the digest has nothing in it, the server sends an empty text. */
    fun body(text: String?): String = text?.trim().orEmpty().ifEmpty { "Nothing happened on its own in that time." }
}
