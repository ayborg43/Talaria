package app.talaria.phone

import android.content.Context
import android.service.notification.NotificationListenerService
import androidx.core.app.NotificationManagerCompat

/**
 * Android only lets an app read other apps' notifications once the person has switched on "notification access" for it,
 * in a settings page of its own. This service does nothing but make that possible, and keep a handle so a request from
 * the agent can read what is showing. It stores nothing.
 */
class PhoneNotificationListener : NotificationListenerService() {
    override fun onListenerConnected() { live = this }
    override fun onListenerDisconnected() { if (live === this) live = null }

    companion object {
        @Volatile var live: PhoneNotificationListener? = null
        fun enabled(ctx: Context) = NotificationManagerCompat.getEnabledListenerPackages(ctx).contains(ctx.packageName)
    }
}
