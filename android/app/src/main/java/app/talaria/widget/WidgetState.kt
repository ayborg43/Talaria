package app.talaria.widget

/** What the home-screen widget knows: whether you are signed in, how many approvals wait, how many tasks run. */
data class WidgetState(val signedIn: Boolean = false, val pending: Int = 0, val tasks: Int = 0)

enum class Look { Quiet, Working, Asking }

/** The words on the widget for a state: the chip, a line under it, and which colours to use. */
data class WidgetWords(val chip: String, val detail: String, val look: Look)

fun widgetWords(s: WidgetState): WidgetWords = when {
    !s.signedIn -> WidgetWords("Signed out", "Open Talaria to sign in.", Look.Quiet)
    s.pending > 0 -> WidgetWords("${s.pending} waiting", if (s.pending == 1) "One thing needs your answer. Tap to see it." else "${s.pending} things need your answer. Tap to see them.", Look.Asking)
    s.tasks > 0 -> WidgetWords("${s.tasks} running", if (s.tasks == 1) "Talaria is working on a task in the background." else "Talaria is working on ${s.tasks} tasks in the background.", Look.Working)
    else -> WidgetWords("All quiet", "Nothing is running. Ask for something below.", Look.Quiet)
}
