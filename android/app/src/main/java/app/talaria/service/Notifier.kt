package app.talaria.service

import android.Manifest
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.content.ContextCompat
import app.talaria.MainActivity
import app.talaria.R
import app.talaria.data.Repo
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.launch

/**
 * Notifications for what needs you while the app is not on screen: an approval waiting, and the agent's replies.
 * Nothing private is put where a lock screen can show it: the full text is for after unlocking.
 */
class Notifier(private val ctx: Context, private val repo: Repo) {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
    private val nm = NotificationManagerCompat.from(ctx)

    init {
        val sys = ctx.getSystemService(NotificationManager::class.java)
        sys.createNotificationChannel(NotificationChannel(APPROVALS, "Approvals", NotificationManager.IMPORTANCE_HIGH).apply {
            description = "When the agent needs your yes or no before it acts."
            lockscreenVisibility = android.app.Notification.VISIBILITY_PRIVATE
        })
        sys.createNotificationChannel(NotificationChannel(REPLIES, "Replies and finished work", NotificationManager.IMPORTANCE_DEFAULT).apply {
            description = "When the agent answers or finishes a task while the app is closed."
            lockscreenVisibility = android.app.Notification.VISIBILITY_PRIVATE
        })
        sys.createNotificationChannel(NotificationChannel(CONNECTION, "Connection", NotificationManager.IMPORTANCE_MIN).apply {
            description = "Shown while the app keeps its link to your server open so alerts reach you."
            setShowBadge(false)
        })
    }

    fun canPost() = repo.store.notifications && (Build.VERSION.SDK_INT < 33 ||
        ContextCompat.checkSelfPermission(ctx, Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED)

    fun start() {
        scope.launch {
            repo.events.collect { ev ->
                val m = ev.msg ?: return@collect
                if (ev.type != "msg") return@collect
                when {
                    m.role == "approval" && m.state == "pending" && m.token.isNotEmpty() -> if (!repo.foreground) approval(m.token, m.text, m.tool, m.always)
                    m.role == "approval" -> nm.cancel(approvalId(m.token))
                    m.role == "assistant" && m.text.isNotBlank() -> if (!repo.foreground) reply(m.text)
                }
            }
        }
    }

    private fun open(to: app.talaria.QuickAction? = null): PendingIntent = PendingIntent.getActivity(
        ctx, to?.ordinal?.plus(1) ?: 0,
        to?.intent(ctx) ?: Intent(ctx, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_CLEAR_TOP),
        PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
    )

    private fun action(token: String, choice: String, label: String): NotificationCompat.Action {
        val i = Intent(ctx, NotificationActions::class.java).setAction("app.talaria.DECIDE").putExtra("token", token).putExtra("choice", choice)
        val pi = PendingIntent.getBroadcast(ctx, (token + choice).hashCode(), i, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        return NotificationCompat.Action.Builder(0, label, pi).build()
    }

    private fun approvalId(token: String) = 1000 + (token.hashCode() and 0xFFFF)

    @Suppress("MissingPermission")
    private fun approval(token: String, text: String, tool: String, always: Boolean) {
        if (!canPost()) return
        val title = "Talaria needs your answer"
        val body = text.ifBlank { tool.ifBlank { "An action is waiting for your approval." } }
        val b = NotificationCompat.Builder(ctx, APPROVALS)
            .setSmallIcon(R.drawable.ic_stat).setContentTitle(title).setContentText(body)
            .setStyle(NotificationCompat.BigTextStyle().bigText(body))
            .setPublicVersion(NotificationCompat.Builder(ctx, APPROVALS).setSmallIcon(R.drawable.ic_stat).setContentTitle(title).setContentText("Open the app to see what it is.").build())
            .setCategory(NotificationCompat.CATEGORY_MESSAGE).setPriority(NotificationCompat.PRIORITY_HIGH)
            .setContentIntent(open(app.talaria.QuickAction.Approvals)).setAutoCancel(false).setOnlyAlertOnce(true)
        // With the app lock on, an approval is not something a notification button may answer: it asks for unlocking first.
        if (!repo.store.lockWithBiometrics) { b.addAction(action(token, "allow", "Allow")); b.addAction(action(token, "deny", "Deny")) }
        nm.notify(approvalId(token), b.build())
    }

    @Suppress("MissingPermission")
    private fun reply(text: String) {
        if (!canPost()) return
        val first = text.trim().lines().first().take(120)
        val b = NotificationCompat.Builder(ctx, REPLIES)
            .setSmallIcon(R.drawable.ic_stat).setContentTitle("Talaria").setContentText(first)
            .setStyle(NotificationCompat.BigTextStyle().bigText(text.trim().take(600)))
            .setPublicVersion(NotificationCompat.Builder(ctx, REPLIES).setSmallIcon(R.drawable.ic_stat).setContentTitle("Talaria").setContentText("New reply").build())
            .setCategory(NotificationCompat.CATEGORY_MESSAGE).setContentIntent(open()).setAutoCancel(true)
        nm.notify(REPLY_ID, b.build())
    }

    fun cancelAll() { nm.cancel(REPLY_ID) }

    fun connectionNotification(host: String) = NotificationCompat.Builder(ctx, CONNECTION)
        .setSmallIcon(R.drawable.ic_stat).setContentTitle("Connected to $host").setContentText("Keeping alerts coming. Turn this off in Settings.")
        .setOngoing(true).setPriority(NotificationCompat.PRIORITY_MIN).setContentIntent(open()).build()

    companion object {
        const val APPROVALS = "approvals"
        const val REPLIES = "replies"
        const val CONNECTION = "connection"
        const val REPLY_ID = 1
    }
}
