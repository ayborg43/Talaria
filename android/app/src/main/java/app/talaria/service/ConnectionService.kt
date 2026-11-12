package app.talaria.service

import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.IBinder
import androidx.core.app.ServiceCompat
import app.talaria.TalariaApp
import app.talaria.data.Servers

/**
 * Keeps the app's process, and so its event stream, alive while the screen is off. It does nothing itself: its
 * only job is to be a foreground service, which Android allows to keep running. The person can turn it off.
 */
class ConnectionService : Service() {
    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val app = application as TalariaApp
        if (!app.repo.store.signedIn || !app.repo.store.notifications) { stopSelf(); return START_NOT_STICKY }
        val n = app.notifier.connectionNotification(Servers.display(app.repo.store.baseUrl))
        try {
            // The "special use" service type exists from Android 14; earlier versions take no type.
            if (android.os.Build.VERSION.SDK_INT >= 34) ServiceCompat.startForeground(this, 7, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE)
            else startForeground(7, n)
        }
        catch (e: Exception) { stopSelf(); return START_NOT_STICKY } // Android refuses a foreground start from the background; the app tries again when opened
        app.repo.start()
        return START_STICKY
    }

    companion object {
        fun sync(context: Context) {
            val app = context.applicationContext as TalariaApp
            val i = Intent(context, ConnectionService::class.java)
            if (app.repo.store.signedIn && app.repo.store.notifications) context.startForegroundService(i) else context.stopService(i)
        }
    }
}
