package app.talaria

import android.content.Context
import android.content.Intent

/** Things you can ask for from outside the app: a long-press on the icon, the quick-settings tile, the widget, a notification. */
enum class QuickAction(val action: String) {
    NewChat("app.talaria.action.NEW_CHAT"),
    Talk("app.talaria.action.TALK"),
    Ask("app.talaria.action.ASK"),
    Approvals("app.talaria.action.APPROVALS"),
    Photo("app.talaria.action.PHOTO");

    fun intent(ctx: Context): Intent =
        Intent(ctx, MainActivity::class.java).setAction(action).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP or Intent.FLAG_ACTIVITY_SINGLE_TOP)

    companion object {
        fun from(action: String?): QuickAction? = entries.firstOrNull { it.action == action }
    }
}
