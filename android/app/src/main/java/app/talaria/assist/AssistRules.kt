package app.talaria.assist

/** The small rules behind being the phone's assistant, free of Android so they can be tested. */
object AssistRules {
    /** `VoiceInteractionSession.SHOW_WITH_SCREENSHOT`. */
    const val SHOW_WITH_SCREENSHOT = 2

    /** What the message box says when a screenshot came with the request. */
    const val SCREEN_QUESTION = "What is on my screen?"

    /** Android sends a screenshot only if the person allowed "Use screenshot" and this flag is set: otherwise there is nothing to wait for. */
    fun expectsScreenshot(showFlags: Int) = showFlags and SHOW_WITH_SCREENSHOT != 0

    /** How long the session waits for the screenshot before opening the app without one. */
    const val WAIT_MS = 1_500L

    /** The message box text for this request: the screen question only when there is a screenshot to ask about. */
    fun draft(hasScreenshot: Boolean): String? = if (hasScreenshot) SCREEN_QUESTION else null

    /** Whether to start listening: only with the switch on, and never when a screenshot is waiting to be looked at and a question edited. */
    fun listen(switchOn: Boolean, hasScreenshot: Boolean) = switchOn && !hasScreenshot
}
