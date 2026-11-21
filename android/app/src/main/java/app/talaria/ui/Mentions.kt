package app.talaria.ui

/**
 * `@name` in the message box: while the last word typed starts with "@", the names of the person's saved agents that begin
 * with what has been typed are offered, and choosing one writes `@name ` in its place. Nothing here talks to a server. The
 * message goes through the ordinary send path as typed; the server is what reads a leading `@name ` as a hand-off.
 */
object Mentions {
    /** How many names are offered at once. */
    const val MAX = 8
    private const val LONGEST_NAME = 30 // an agent's name is at most this long, so anything longer after the "@" is not a name

    /** Where the "@" being completed is in [draft], or -1. It counts only at the start or after a space, so an email address does not. */
    internal fun at(draft: String): Int {
        var i = draft.lastIndexOf('@')
        while (i >= 0) {
            if (i == 0 || draft[i - 1].isWhitespace()) {
                val typed = draft.substring(i + 1)
                return if ('\n' in typed || typed.length > LONGEST_NAME) -1 else i
            }
            i = if (i == 0) -1 else draft.lastIndexOf('@', i - 1)
        }
        return -1
    }

    /** Whether a name is being typed at the end of [draft]. */
    fun typing(draft: String): Boolean = at(draft) >= 0

    /** The names to offer (at most [MAX]) for what has been typed after the "@": those that start with it, whatever the case. */
    fun suggest(draft: String, names: List<String>): List<String> {
        val i = at(draft)
        if (i < 0) return emptyList()
        val typed = draft.substring(i + 1)
        // "@Research " with Research chosen already is finished, even if a longer name begins the same way
        if (typed.endsWith(" ") && names.any { it.equals(typed.trim(), ignoreCase = true) }) return emptyList()
        return names.filter { it.startsWith(typed, ignoreCase = true) }.distinct().take(MAX)
    }

    /** [draft] with the "@" being typed, and what follows it, replaced by `@name ` (a space after, ready to go on). */
    fun insert(draft: String, name: String): String {
        val i = at(draft)
        return (if (i < 0) draft + (if (draft.isEmpty() || draft.last().isWhitespace()) "" else " ") else draft.substring(0, i)) + "@" + name + " "
    }
}
