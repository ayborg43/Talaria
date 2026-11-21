package app.talaria.widget

import app.talaria.data.CardDto

/**
 * What the board widget knows: whether you are signed in, whether this server has a board, and how many cards wait in
 * Ready, work in Running and wait for a look in Review. [known] is false until a first look at the server has happened.
 */
data class BoardWidgetState(
    val signedIn: Boolean = false, val known: Boolean = false, val available: Boolean = true,
    val ready: Int = 0, val running: Int = 0, val review: Int = 0,
) {
    /** One short line kept on the phone, so the widget can be drawn at once while it looks again. */
    fun encode(): String = when {
        !known -> ""
        !available -> "off"
        else -> "$ready,$running,$review"
    }

    companion object {
        fun decode(signedIn: Boolean, s: String): BoardWidgetState = when {
            s == "off" -> BoardWidgetState(signedIn, known = true, available = false)
            else -> s.split(",").mapNotNull { it.toIntOrNull()?.coerceAtLeast(0) }.takeIf { it.size == 3 }
                ?.let { BoardWidgetState(signedIn, known = true, ready = it[0], running = it[1], review = it[2]) }
                ?: BoardWidgetState(signedIn)
        }

        /** The three numbers, from what the server's board holds. */
        fun of(cards: List<CardDto>): BoardWidgetState = BoardWidgetState(
            signedIn = true, known = true,
            ready = cards.count { it.column == "ready" }, running = cards.count { it.column == "running" }, review = cards.count { it.column == "review" },
        )
    }
}

/** The words the board widget says instead of the numbers when it has none to show, or null when it has them. */
fun boardWidgetNote(s: BoardWidgetState): String? = when {
    !s.signedIn -> "Open Talaria to sign in."
    !s.known -> "Open the board to see your cards."
    !s.available -> "No board on this server."
    else -> null
}
