package app.talaria.service

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import androidx.core.app.NotificationManagerCompat
import app.talaria.TalariaApp
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch

/** Allow or Deny pressed in a notification: sent to the server straight away, with no screen needed. */
class NotificationActions : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action == REPLY) { reply(context, intent); return }
        val token = intent.getStringExtra("token") ?: return
        val choice = intent.getStringExtra("choice") ?: return
        val repo = (context.applicationContext as TalariaApp).repo
        val pending = goAsync()
        CoroutineScope(Dispatchers.IO).launch {
            try {
                repo.decide(token, choice)
            } catch (_: Exception) {
                // Already answered elsewhere, expired, or the server is out of reach: the chat shows where it stands.
            } finally {
                NotificationManagerCompat.from(context).cancel(1000 + (token.hashCode() and 0xFFFF))
                pending.finish()
            }
        }
    }

    /** A reply typed into a notification goes to the agent as if it had been typed in the app. */
    private fun reply(context: Context, intent: Intent) {
        val text = androidx.core.app.RemoteInput.getResultsFromIntent(intent)?.getCharSequence(Notifier.KEY_REPLY)?.toString()?.trim().orEmpty()
        if (text.isEmpty()) return
        val repo = (context.applicationContext as TalariaApp).repo
        val pending = goAsync()
        CoroutineScope(Dispatchers.IO).launch {
            try { repo.send(text) } catch (_: Exception) { /* the chat shows where it stands */ }
            finally { NotificationManagerCompat.from(context).cancel(Notifier.REPLY_ID); pending.finish() }
        }
    }

    companion object { const val REPLY = "app.talaria.REPLY" }
}
