package app.talaria

/**
 * What "Ask about the clipboard" does with the clip, free of Android so it can be tested. Only text is used, only up to
 * [MAX_CHARS] of it, and nothing the phone marks as sensitive (a password manager's copy, say).
 */
object ClipAsk {
    const val MAX_CHARS = 4_000
    /** The extra Android 13 and later put on a clip's description when it holds something private. */
    const val SENSITIVE_KEY = "android.content.extra.IS_SENSITIVE"

    sealed interface Result {
        /** [text] is the message to start with; [cut] says the clip was longer. */
        data class Text(val text: String, val cut: Boolean) : Result
        data object Empty : Result
        data object NoText : Result
        data object Sensitive : Result
    }

    /** [text] is the clip's text (null when it has none), [hasTextType] whether its type is text, [sensitive] the flag above. */
    fun read(text: CharSequence?, hasTextType: Boolean, sensitive: Boolean): Result {
        if (sensitive) return Result.Sensitive // checked first: a private clip is not even looked at
        if (!hasTextType) return Result.NoText
        val t = text?.toString()?.trim().orEmpty()
        if (t.isEmpty()) return Result.Empty
        if (t.length <= MAX_CHARS) return Result.Text(prompt(t), false)
        // Never end on half of an emoji.
        var end = MAX_CHARS
        if (Character.isHighSurrogate(t[end - 1])) end--
        return Result.Text(prompt(t.substring(0, end).trimEnd() + "…"), true)
    }

    fun prompt(clip: String) = "About this text: $clip"
}
