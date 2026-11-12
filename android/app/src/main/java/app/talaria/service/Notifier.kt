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
import androidx.core.app.RemoteInput
import app.talaria.data.Conn
import app.talaria.data.Message
import app.talaria.data.Repo
import app.talaria.data.Servers
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.filterNotNull
import kotlinx.coroutines.flow.map
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
        // An approval answered anywhere (in the app, on the web, by a timeout) comes back without its token, so the
        // notices are matched against what is still waiting instead: any notice for the live server that is not waiting goes.
        scope.launch {
            kotlinx.coroutines.flow.combine(
                repo.conn,
                repo.messages.map { list -> list.filter { it.role == "approval" && it.state == "pending" && it.token.isNotEmpty() }.map { approvalId(it.token) }.toSet() }.distinctUntilChanged(),
            ) { c, waiting -> if (c is Conn.Online) waiting else null }
                .filterNotNull().collect { reconcile(it) }
        }
        scope.launch {
            repo.events.collect { ev ->
                val m = ev.msg ?: return@collect
                if (ev.type != "msg") return@collect
                when {
                    m.role == "approval" && m.state == "pending" && m.token.isNotEmpty() -> if (!repo.foreground) approval(null, m)
                    m.role == "approval" -> { if (m.token.isNotEmpty()) nm.cancel(approvalId(m.token)) }
                    m.role == "assistant" && m.text.isNotBlank() -> if (!repo.foreground) reply(m.text)
                }
            }
        }
    }

    /**
     * Approvals found by looking at a server directly (the app was not connected, or it is a server other than the
     * live one). [origin] is the server they belong to.
     */
    fun catchUp(origin: String, messages: List<Message>) {
        val other = origin != repo.store.baseUrl
        if (!other && repo.foreground) return
        Alerts.fresh(messages, emptyList()).forEach { approval(origin.takeIf { other }, it) }
    }

    @Suppress("MissingPermission")
    private fun reconcile(waitingIds: Set<Int>) {
        if (!canPost()) return
        ctx.getSystemService(NotificationManager::class.java).activeNotifications
            .filter { it.notification.group == GROUP_APPROVALS && it.id != SUMMARY_ID }
            .forEach { sbn ->
                val server = sbn.notification.extras.getString(EXTRA_SERVER)
                if ((server == null || server == repo.store.baseUrl) && sbn.id !in waitingIds) nm.cancel(sbn.id)
            }
        refreshSummary()
    }

    /** [server], when given, is switched to when the notification is opened. */
    private fun open(to: app.talaria.QuickAction? = null, server: String? = null): PendingIntent {
        val i = (to?.intent(ctx) ?: Intent(ctx, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_CLEAR_TOP))
        if (server != null) i.putExtra("server", server)
        return PendingIntent.getActivity(ctx, (to?.ordinal?.plus(1) ?: 0) * 31 + (server?.hashCode() ?: 0), i, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
    }

    private fun action(token: String, choice: String, label: String): NotificationCompat.Action {
        val i = Intent(ctx, NotificationActions::class.java).setAction("app.talaria.DECIDE").putExtra("token", token).putExtra("choice", choice)
        val pi = PendingIntent.getBroadcast(ctx, (token + choice).hashCode(), i, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        return NotificationCompat.Action.Builder(0, label, pi).build()
    }

    private fun approvalId(token: String) = 1000 + (token.hashCode() and 0xFFFF)

    /** [server] is set for a server that is not the live one: its notice names it, and opening it switches there. */
    @Suppress("MissingPermission")
    private fun approval(server: String?, m: Message) {
        if (!canPost()) return
        if (!repo.store.announce(m.token)) return // shown already
        val title = if (server != null) "${Servers.display(server)} needs your answer" else "Talaria needs your answer"
        val body = Alerts.plain(m.text).ifBlank { m.tool.ifBlank { "An action is waiting for your approval." } }
        val b = NotificationCompat.Builder(ctx, APPROVALS)
            .setSmallIcon(R.drawable.ic_stat).setContentTitle(title).setContentText(body)
            .setStyle(NotificationCompat.BigTextStyle().bigText(body))
            .setPublicVersion(NotificationCompat.Builder(ctx, APPROVALS).setSmallIcon(R.drawable.ic_stat).setContentTitle(title).setContentText("Open the app to see what it is.").build())
            .setCategory(NotificationCompat.CATEGORY_MESSAGE).setPriority(NotificationCompat.PRIORITY_HIGH)
            .setGroup(GROUP_APPROVALS)
            .apply { if (server != null) addExtras(android.os.Bundle().apply { putString(EXTRA_SERVER, server) }) }
            .setContentIntent(open(app.talaria.QuickAction.Approvals, server)).setAutoCancel(false).setOnlyAlertOnce(true)
        // With the app lock on, an approval is not something a notification button may answer: it asks for unlocking first.
        // Nor can one on a server the phone is not talking to now: that answer has to be given in the app.
        if (server == null && !repo.store.lockWithBiometrics) { b.addAction(action(m.token, "allow", "Allow")); b.addAction(action(m.token, "deny", "Deny")) }
        nm.notify(approvalId(m.token), b.build())
        refreshSummary()
    }

    /** Two or more approvals waiting fold into one stack with a heading. */
    @Suppress("MissingPermission")
    private fun refreshSummary() {
        if (!canPost()) return
        val waiting = ctx.getSystemService(NotificationManager::class.java).activeNotifications.filter { it.notification.group == GROUP_APPROVALS && it.id != SUMMARY_ID }
        if (waiting.size < 2) { nm.cancel(SUMMARY_ID); return }
        val lines = waiting.mapNotNull { it.notification.extras.getCharSequence(android.app.Notification.EXTRA_TEXT) }
        val box = NotificationCompat.InboxStyle().also { st -> lines.take(5).forEach { st.addLine(it) } }
        nm.notify(SUMMARY_ID, NotificationCompat.Builder(ctx, APPROVALS)
            .setSmallIcon(R.drawable.ic_stat).setContentTitle("${waiting.size} things need your answer").setContentText("Open the app to answer them.")
            .setStyle(box).setGroup(GROUP_APPROVALS).setGroupSummary(true).setOnlyAlertOnce(true)
            .setPublicVersion(NotificationCompat.Builder(ctx, APPROVALS).setSmallIcon(R.drawable.ic_stat).setContentTitle("${waiting.size} things need your answer").build())
            .setContentIntent(open(app.talaria.QuickAction.Approvals)).build())
    }

    @Suppress("MissingPermission")
    private fun reply(text: String) {
        if (!canPost()) return
        val first = text.trim().lines().first().take(120)
        val now = java.time.LocalTime.now().let { it.hour * 60 + it.minute }
        val quiet = repo.store.quietHours && Alerts.inQuietHours(now, repo.store.quietFrom, repo.store.quietTo)
        val b = NotificationCompat.Builder(ctx, REPLIES).setSilent(quiet)
            .setSmallIcon(R.drawable.ic_stat).setContentTitle("Talaria").setContentText(first)
            .setStyle(NotificationCompat.BigTextStyle().bigText(text.trim().take(600)))
            .setPublicVersion(NotificationCompat.Builder(ctx, REPLIES).setSmallIcon(R.drawable.ic_stat).setContentTitle("Talaria").setContentText("New reply").build())
            .setCategory(NotificationCompat.CATEGORY_MESSAGE).setContentIntent(open()).setAutoCancel(true)
        // Answering from the notification is not offered while the app lock is on, as with approvals.
        if (!repo.store.lockWithBiometrics) {
            val reply = RemoteInput.Builder(KEY_REPLY).setLabel("Reply to Talaria").build()
            val pi = PendingIntent.getBroadcast(ctx, 77, Intent(ctx, NotificationActions::class.java).setAction(NotificationActions.REPLY), PendingIntent.FLAG_MUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
            b.addAction(NotificationCompat.Action.Builder(0, "Reply", pi).addRemoteInput(reply).setAllowGeneratedReplies(true).setSemanticAction(NotificationCompat.Action.SEMANTIC_ACTION_REPLY).build())
        }
        nm.notify(REPLY_ID, b.build())
    }

    fun cancelAll() { nm.cancel(REPLY_ID) }

    /** [status] says whether the link is working, so a notice that has been sitting there for hours still tells the truth. */
    fun connectionNotification(host: String, status: String = "Keeping alerts coming. Turn this off in Settings.") = NotificationCompat.Builder(ctx, CONNECTION)
        .setSmallIcon(R.drawable.ic_stat).setContentTitle(host).setContentText(status)
        .setOngoing(true).setOnlyAlertOnce(true).setPriority(NotificationCompat.PRIORITY_MIN).setContentIntent(open()).build()

    @Suppress("MissingPermission")
    fun updateConnection(host: String, status: String) { if (canPost()) nm.notify(CONNECTION_ID, connectionNotification(host, status)) }

    companion object {
        const val APPROVALS = "approvals"
        const val REPLIES = "replies"
        const val CONNECTION = "connection"
        const val REPLY_ID = 1
        const val SUMMARY_ID = 2
        const val CONNECTION_ID = 7
        const val GROUP_APPROVALS = "approvals"
        const val KEY_REPLY = "reply"
        const val EXTRA_SERVER = "app.talaria.server"
    }
}
