package app.talaria.service

import app.talaria.data.Message

/** The small rules behind alerts, kept free of Android so they can be tested. */
object Alerts {
    /** Approvals that are waiting for an answer and have not been announced yet. */
    fun fresh(messages: List<Message>, announced: Collection<String>): List<Message> =
        messages.filter { it.role == "approval" && it.state == "pending" && it.token.isNotEmpty() && it.token !in announced }

    /** Adds [token] to the list of announced approvals, forgetting the oldest beyond [cap]. */
    fun remember(list: List<String>, token: String, cap: Int = 100): List<String> =
        if (token in list) list else (list + token).takeLast(cap)

    /**
     * Whether [nowMinute] (minutes after midnight) falls inside quiet hours that start at [from] and end at [to].
     * The span may cross midnight (22:00 to 07:00). Equal times mean no quiet hours.
     */
    fun inQuietHours(nowMinute: Int, from: Int, to: Int): Boolean = when {
        from == to -> false
        from < to -> nowMinute in from until to
        else -> nowMinute >= from || nowMinute < to
    }

    /** An approval's text as a notification should show it: no code fences, and without the sentence that is for the chat. */
    fun plain(text: String): String = text.lines()
        .filter { !it.trim().startsWith("```") && !it.trim().startsWith("I will carry on with anything else") }
        .joinToString("\n").replace(Regex("\n{3,}"), "\n\n").trim()

    /** "22:00" for 1320. */
    fun clock(minutes: Int): String = "%02d:%02d".format(minutes / 60 % 24, minutes % 60)

    /** How long the alarm that checks on the connection waits, in milliseconds. */
    const val WATCHDOG_MS = 15 * 60 * 1000L
}
