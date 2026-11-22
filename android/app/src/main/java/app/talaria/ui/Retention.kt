package app.talaria.ui

import app.talaria.data.RetentionDto

/** The words and choices on the "Old conversations" screen, free of Android so they can be tested. */
object Retention {
    /** A choice of how many days: 0 is forever. */
    data class Choice(val days: Int, val label: String)

    /** For archiving: the server takes 0 or 7 to 3650. */
    val archiveChoices = listOf(Choice(0, "Forever"), Choice(30, "30 days"), Choice(90, "90 days"), Choice(180, "6 months"), Choice(365, "A year"))

    /** For forgetting the searchable history: the server takes 0 or 30 to 3650. */
    val historyChoices = listOf(Choice(0, "Forever"), Choice(90, "90 days"), Choice(180, "6 months"), Choice(365, "A year"), Choice(730, "Two years"))

    /** The label for [days]; a number the list does not have (set from the web app or the file) is written as "N days". */
    fun label(choices: List<Choice>, days: Int): String =
        choices.firstOrNull { it.days == days }?.label ?: if (days <= 0) "Forever" else if (days == 1) "1 day" else "$days days"

    /** The choices to offer: the usual ones, plus the current one when it is not among them, so it is shown and stays chosen. */
    fun offered(choices: List<Choice>, days: Int): List<Choice> =
        if (days <= 0 || choices.any { it.days == days }) choices else (choices + Choice(days, label(choices, days))).sortedBy { it.days }

    /** "If it ran now: 2 to archive, 1 to delete", or "Nothing is due now." Only meant for when keep_days is set. */
    fun dueLine(v: RetentionDto): String =
        if (v.would_archive <= 0 && v.would_delete <= 0) "Nothing is due now."
        else "If it ran now: ${v.would_archive} to archive, ${v.would_delete} to delete."

    /** What the last tidy did, or null when none has run since the server started. [when_] is the time already written for reading. */
    fun lastLine(v: RetentionDto, when_: String): String? {
        if (v.last_at.isBlank()) return null
        val parts = listOfNotNull(
            v.last_archived.takeIf { it > 0 }?.let { "$it archived" },
            v.last_deleted.takeIf { it > 0 }?.let { "$it deleted" },
            v.last_pruned.takeIf { it > 0 }?.let { "$it ${if (it == 1) "message" else "messages"} forgotten" },
        )
        return "Last tidy" + (if (when_.isNotBlank()) " $when_" else "") + ": " + (if (parts.isEmpty()) "nothing to do" else parts.joinToString(", ")) + "."
    }

    /** What archiving does, said for the chosen number. */
    fun archiveNote(days: Int, grace: Int): String =
        if (days <= 0) "Nothing is archived or removed. This is the default."
        else "A conversation nobody has touched for ${label(archiveChoices, days).lowercase()} is archived. One archived this way is deleted for good $grace days later. " +
            "The main conversation, the one that is open, channels, conversations with a named agent and anything you archived yourself are never removed."

    /** The warning under the history row, only when it is set. */
    fun historyWarning(days: Int): String? =
        if (days <= 0) null
        else "Anything older than ${label(historyChoices, days).lowercase()} is forgotten for good: you can no longer search it, and the agent will not remember it."

    /** Whether "Tidy now" has anything it could do: one of the two numbers is set. */
    fun canTidy(v: RetentionDto): Boolean = v.keep_days > 0 || (v.can_prune_history && v.history_keep_days > 0)

    /** The confirmation text for "Tidy now": what will be archived, deleted and forgotten. */
    fun confirmText(v: RetentionDto): String {
        val lines = mutableListOf<String>()
        if (v.keep_days > 0) {
            if (v.would_archive <= 0 && v.would_delete <= 0) lines += "No conversation is due to be archived or deleted right now."
            if (v.would_archive > 0) lines += "${plural(v.would_archive, "conversation")} will be archived."
            if (v.would_delete > 0) lines += "${plural(v.would_delete, "conversation")} will be deleted for good and cannot be brought back."
        }
        if (v.can_prune_history && v.history_keep_days > 0) lines += "The searchable history older than ${label(historyChoices, v.history_keep_days).lowercase()} will be forgotten for good."
        return lines.joinToString(" ")
    }

    private fun plural(n: Int, word: String) = if (n == 1) "1 $word" else "$n ${word}s"
}
