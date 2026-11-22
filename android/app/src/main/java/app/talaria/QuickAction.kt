package app.talaria

import android.content.Context
import android.content.Intent

/** Things you can ask for from outside the app: a long-press on the icon, the quick-settings tile, the widget, a notification. */
enum class QuickAction(val action: String) {
    NewChat("app.talaria.action.NEW_CHAT"),
    Talk("app.talaria.action.TALK"),
    Ask("app.talaria.action.ASK"),
    Approvals("app.talaria.action.APPROVALS"),
    Photo("app.talaria.action.PHOTO"),
    /** Tasks, on the Board tab: what the board widget opens. */
    Board("app.talaria.action.BOARD"),
    /** Opened as the phone's assistant: the message box, with a screenshot attached if Android supplied one. */
    Assist("app.talaria.action.ASSIST");

    fun intent(ctx: Context): Intent =
        Intent(ctx, MainActivity::class.java).setAction(action).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP or Intent.FLAG_ACTIVITY_SINGLE_TOP)

    companion object {
        /** The system's own "assist" request (power-button long-press with Talaria as the assistant) is the same as [Assist]. */
        fun from(action: String?): QuickAction? = if (action == Intent.ACTION_ASSIST) Assist else entries.firstOrNull { it.action == action }
    }
}
