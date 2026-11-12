package app.talaria

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import kotlin.math.PI
import kotlin.math.sin

/** adb shell am broadcast -a app.talaria.DEBUG_HEARD -n app.talaria/.DebugReceiver */
class DebugReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != "app.talaria.DEBUG_HEARD") return
        val voice = (context.applicationContext as TalariaApp).voice
        val tone = ShortArray(24_000) { i -> (sin(2 * PI * 300 * i / 16_000) * 0.3 * Short.MAX_VALUE).toInt().toShort() }
        voice.heardForTest(tone)
    }
}
