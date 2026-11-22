package app.talaria

/**
 * Words (and a picture) that another screen of this app hands to the chat's message box: the clipboard, or the assistant.
 * It lives in memory only, is taken once, and is never written anywhere.
 */
object Handoff {
    private var draft: String? = null
    private var image: android.net.Uri? = null

    @Synchronized fun put(draft: String?, image: android.net.Uri? = null) { this.draft = draft; this.image = image }

    /** What is waiting, once. */
    @Synchronized fun take(): Pair<String?, android.net.Uri?> = (draft to image).also { draft = null; image = null }

    /** Adds [incoming] to what is already in the box without losing it (and without writing the same thing twice). */
    fun merge(current: String, incoming: String): String = when {
        current.isBlank() -> incoming
        current.contains(incoming) -> current
        else -> current.trimEnd() + "\n\n" + incoming
    }
}
