package app.talaria.assist

import android.app.Activity
import android.content.ClipboardManager
import android.content.Context
import android.widget.Toast
import app.talaria.ClipAsk
import app.talaria.Handoff
import app.talaria.QuickAction

/**
 * "Ask about the clipboard". Since Android 10 only the app on screen may read the clipboard, so this has no screen of its
 * own (it is see-through) and reads once, when it has focus, only because the person tapped the tile or the shortcut. The
 * text goes to the message box for editing and is never sent from here; nothing is logged or stored.
 */
class AskClipboardActivity : Activity() {
    private var done = false

    override fun onWindowFocusChanged(hasFocus: Boolean) {
        super.onWindowFocusChanged(hasFocus)
        if (!hasFocus || done) return
        done = true
        val cm = getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
        // The description says what the clip is without handing over what is in it: a private one is turned away before it is read.
        val desc = cm.primaryClipDescription
        val sensitive = desc?.extras?.getBoolean(ClipAsk.SENSITIVE_KEY, false) == true
        val textType = desc?.hasMimeType("text/*") == true
        val result = when {
            desc == null -> ClipAsk.Result.Empty
            sensitive -> ClipAsk.Result.Sensitive
            !textType -> ClipAsk.Result.NoText
            else -> ClipAsk.read(cm.primaryClip?.takeIf { it.itemCount > 0 }?.getItemAt(0)?.text, true, false)
        }
        when (result) {
            is ClipAsk.Result.Text -> {
                Handoff.put(result.text)
                startActivity(QuickAction.Ask.intent(this))
                if (result.cut) Toast.makeText(this, "The text was long, so only the start is used.", Toast.LENGTH_SHORT).show()
            }
            ClipAsk.Result.Empty -> say("The clipboard is empty. Copy some text first.")
            ClipAsk.Result.NoText -> say("The clipboard has no text in it.")
            ClipAsk.Result.Sensitive -> say("What is on the clipboard is marked private, so Talaria did not read it.")
        }
        finish()
    }

    private fun say(text: String) = Toast.makeText(this, text, Toast.LENGTH_LONG).show()
}
