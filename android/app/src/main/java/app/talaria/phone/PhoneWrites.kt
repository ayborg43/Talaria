package app.talaria.phone

import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.ActivityNotFoundException
import android.content.Context
import android.content.Intent
import android.net.Uri
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import app.talaria.MainActivity
import app.talaria.R
import app.talaria.TalariaApp
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.time.format.FormatStyle
import java.util.concurrent.atomic.AtomicInteger

/**
 * Carries out what the agent asked the phone to do (phone_do), after the server has asked the person and [WriteArgs] has
 * checked it again. It only ever starts another app's screen with something filled in: it never sends a message, places
 * a call or deletes anything, and it has no permission that could (no SMS, no phone, no calendar write).
 *
 * Android 10 and later do not let an app start another app's screen from the background. So:
 *  - while Talaria is open on the phone, the screen is started at once. The clock app can set an alarm or timer quietly
 *    (no screen at all); the calendar, maps and messaging apps open for the person to look at.
 *  - otherwise a notice is posted ("tap to set the alarm"), and the tap, which Android does allow, starts it. Nothing
 *    happens until then, and the agent is told so.
 */
object PhoneWrites {
    private const val CHANNEL = "phone_actions"
    private const val BASE_ID = 7000
    private val next = AtomicInteger()

    fun run(ctx: Context, op: String, args: JsonObject): JsonElement {
        val w = WriteArgs.parse(op, args)
        val app = ctx.applicationContext as TalariaApp
        return if (app.repo.foreground) {
            // The person is looking at Talaria, so Android lets it start the screen now.
            try {
                ctx.startActivity(intent(ctx, WriteArgs.plan(w, skipUi = true)).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
            } catch (e: ActivityNotFoundException) {
                throw Refused("This phone has no app that can " + what(w) + ".")
            } catch (e: SecurityException) {
                throw Refused("Android did not let Talaria " + what(w) + ".")
            }
            val quiet = w is Write.Alarm || w is Write.Timer
            if (quiet) app.repo.toast("Asked your clock app: " + say(w))
            reply(
                if (quiet) "asked_clock_app" else "opened",
                if (quiet) "The clock app was asked to ${say(w)}. It does not report back, so this was not checked."
                else "The app was opened with it filled in. " + when (w) {
                    is Write.Reminder -> "The calendar will not keep it until the user presses Save."
                    is Write.SmsDraft -> "The text is only written: it is not sent until the user presses send."
                    else -> "Nothing more is needed."
                },
            )
        } else {
            if (!NotificationManagerCompat.from(ctx).areNotificationsEnabled()) {
                throw Refused("Talaria is not open on the phone, and Android only lets an app open another one from the background after a tap on a notice. Notices are off for Talaria, so this cannot be done now. Ask again while Talaria is open on the phone.")
            }
            notice(ctx, w)
            reply("waiting_for_tap", "Talaria is not open on the phone, and Android does not let it open another app from the background. A notice was posted instead; nothing happens until the user taps it.")
        }
    }

    /** The notice whose tap starts the screen. Android allows an app to start one from a tap on its own notice. */
    private fun notice(ctx: Context, w: Write) {
        val sys = ctx.getSystemService(NotificationManager::class.java)
        sys.createNotificationChannel(NotificationChannel(CHANNEL, "Things you asked Talaria to do", NotificationManager.IMPORTANCE_HIGH).apply {
            description = "When Talaria is closed and prepared something for you, such as an alarm, a reminder or a text: tap to finish it."
            lockscreenVisibility = android.app.Notification.VISIBILITY_PRIVATE
        })
        val id = BASE_ID + next.getAndIncrement() % 50
        // The tap shows the clock app's own screen (the person is there anyway), so it is not set quietly.
        val go = intent(ctx, WriteArgs.plan(w, skipUi = false)).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        val tap = PendingIntent.getActivity(ctx, id, go, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        val open = PendingIntent.getActivity(ctx, id + 100, Intent(ctx, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP), PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        val generic = NotificationCompat.Builder(ctx, CHANNEL).setSmallIcon(R.drawable.ic_stat).setContentTitle("Talaria has something ready").setContentText("Unlock to see it.").build()
        val n = NotificationCompat.Builder(ctx, CHANNEL)
            .setSmallIcon(R.drawable.ic_stat)
            .setContentTitle(title(w))
            .setContentText(noticeText(w))
            .setStyle(NotificationCompat.BigTextStyle().bigText(noticeText(w)))
            .setContentIntent(tap)
            .addAction(0, "Open Talaria", open)
            .setAutoCancel(true)
            .setPriority(NotificationCompat.PRIORITY_HIGH)
            .setCategory(NotificationCompat.CATEGORY_REMINDER)
            .setVisibility(NotificationCompat.VISIBILITY_PRIVATE).setPublicVersion(generic)
            .setTimeoutAfter(2 * 60 * 60 * 1000L) // a stale one goes away by itself
            .build()
        try { sys.notify(id, n) } catch (e: SecurityException) { throw Refused("Android did not let Talaria post a notice, so this cannot be done now.") }
    }

    private fun intent(ctx: Context, p: Plan): Intent {
        val i = Intent(p.action)
        p.data?.let { i.data = Uri.parse(it) }
        p.extras.forEach { (k, v) ->
            when (v) {
                is Int -> i.putExtra(k, v); is Long -> i.putExtra(k, v); is Boolean -> i.putExtra(k, v); is String -> i.putExtra(k, v)
            }
        }
        return i
    }

    private fun reply(status: String, detail: String): JsonElement = buildJsonObject { put("status", status); put("detail", detail) }

    /** What an alarm or timer comes to, as the clock app is told and the person sees in a toast. */
    fun say(w: Write): String = when (w) {
        is Write.Alarm -> "set an alarm for %02d:%02d".format(w.hour, w.minute) + if (w.label.isNotEmpty()) " (${w.label})" else ""
        is Write.Timer -> "start a ${length(w.seconds)} timer" + if (w.label.isNotEmpty()) " (${w.label})" else ""
        else -> what(w)
    }

    /** The line under a notice's title: what the tap will open. */
    fun noticeText(w: Write): String = when (w) {
        is Write.Alarm, is Write.Timer -> say(w).replaceFirstChar { it.uppercase() }
        is Write.Reminder -> "\u201c${w.title}\u201d for " + DateTimeFormatter.ofLocalizedDateTime(FormatStyle.MEDIUM, FormatStyle.SHORT).format(Instant.ofEpochMilli(w.begin).atZone(ZoneId.systemDefault())) + ". You press Save in the calendar."
        is Write.MapAt -> "Show " + w.query.ifEmpty { "that point" } + " on the map."
        is Write.SmsDraft -> "To ${w.to}: \u201c${w.body.take(160)}\u201d. Not sent: you press send."
    }

    private fun title(w: Write) = when (w) {
        is Write.Alarm -> "Tap to set your alarm"
        is Write.Timer -> "Tap to start your timer"
        is Write.Reminder -> "Tap to review your reminder"
        is Write.MapAt -> "Tap to see it on the map"
        is Write.SmsDraft -> "Tap to see your text (not sent)"
    }

    private fun what(w: Write) = when (w) {
        is Write.Alarm -> "set alarms"
        is Write.Timer -> "start timers"
        is Write.Reminder -> "make calendar entries"
        is Write.MapAt -> "show maps"
        is Write.SmsDraft -> "write texts"
    }

    internal fun length(sec: Int): String = buildList {
        if (sec >= 3600) add("${sec / 3600} h")
        if (sec % 3600 >= 60) add("${sec % 3600 / 60} min")
        if (sec % 60 > 0 || sec == 0) add("${sec % 60} s")
    }.joinToString(" ")

    /** The words for what a request came to, as the "Recently asked" list shows them. */
    fun statusWords(status: String) = when (status) {
        "waiting_for_tap" -> "Waiting for your tap on a notice"
        "asked_clock_app" -> "Asked the clock app"
        "opened" -> "Opened for you"
        else -> status
    }
}
