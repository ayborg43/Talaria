package app.talaria

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import kotlinx.coroutines.launch
import kotlin.math.PI
import kotlin.math.sin

/**
 * adb shell am broadcast -a app.talaria.DEBUG_HEARD -n app.talaria/.DebugReceiver      (feeds the voice pipeline a tone)
 * adb shell am broadcast -a app.talaria.DEBUG_SAY --es text "what time is it" -n app.talaria/.DebugReceiver  (as if heard)
 * adb shell am broadcast -a app.talaria.DEBUG_WATCHDOG -n app.talaria/.DebugReceiver   (runs the quarter-hour check now)
 */
class DebugReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action == "app.talaria.DEBUG_WATCHDOG") {
            val pending = goAsync()
            kotlinx.coroutines.CoroutineScope(kotlinx.coroutines.Dispatchers.IO).launch {
                try { app.talaria.service.Watchdog.look(context.applicationContext as TalariaApp) } finally { pending.finish() }
            }
            return
        }
        if (intent.action == "app.talaria.DEBUG_SAY") {
            (context.applicationContext as TalariaApp).voice.heardTextForTest(intent.getStringExtra("text") ?: "what time is it")
            return
        }
        if (intent.action != "app.talaria.DEBUG_HEARD") return
        val voice = (context.applicationContext as TalariaApp).voice
        val tone = ShortArray(24_000) { i -> (sin(2 * PI * 300 * i / 16_000) * 0.3 * Short.MAX_VALUE).toInt().toShort() }
        voice.heardForTest(tone)
    }
}
