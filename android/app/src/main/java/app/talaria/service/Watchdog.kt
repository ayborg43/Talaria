package app.talaria.service

import android.app.AlarmManager
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.os.SystemClock
import app.talaria.TalariaApp
import app.talaria.data.Api
import app.talaria.data.Conn
import app.talaria.data.StateDto
import app.talaria.data.fixedCredentials
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch

/**
 * The safety net under the live connection. Some phones stop background services to save battery, and
 * Android may pause the network while the phone sleeps. Every so often (about every quarter hour) this wakes up,
 * starts the live connection again if it is gone, and looks at every saved server for something waiting for an answer.
 * It is one short check, not a second connection, and it has no cost while notifications are off.
 */
object Watchdog {
    private fun pending(ctx: Context) = PendingIntent.getBroadcast(
        ctx, 5, Intent(ctx, WatchdogReceiver::class.java), PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
    )

    fun arm(ctx: Context) {
        val am = ctx.getSystemService(AlarmManager::class.java) ?: return
        // Inexact on purpose: no special permission, and Android may batch it with other wake-ups to save power.
        am.setAndAllowWhileIdle(AlarmManager.ELAPSED_REALTIME_WAKEUP, SystemClock.elapsedRealtime() + Alerts.WATCHDOG_MS, pending(ctx))
    }

    fun cancel(ctx: Context) { ctx.getSystemService(AlarmManager::class.java)?.cancel(pending(ctx)) }

    suspend fun look(app: TalariaApp) {
        val store = app.repo.store
        if (!store.signedIn || !store.notifications) return
        // Android 12 and later refuse to start a service from here unless the app is in view: then the look below is what counts.
        runCatching { ConnectionService.sync(app) }
        for (origin in store.savedServers) {
            if (origin == store.baseUrl && app.repo.conn.value is Conn.Online) continue // the live link is doing this one
            val cookie = store.cookieFor(origin)
            if (cookie.isEmpty()) continue
            val st = runCatching { Api(fixedCredentials(origin, cookie)).getJson<StateDto>("/api/state") }.getOrNull() ?: continue
            app.notifier.catchUp(origin, st.messages)
        }
    }
}

class WatchdogReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val app = context.applicationContext as TalariaApp
        Watchdog.arm(context) // the next one first, so nothing below can end the chain
        val pending = goAsync()
        CoroutineScope(Dispatchers.IO).launch {
            try { Watchdog.look(app) } finally { pending.finish() }
        }
    }
}

/** After a restart or an update the live connection and the watchdog start again by themselves. */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        runCatching { ConnectionService.sync(context) }
    }
}
