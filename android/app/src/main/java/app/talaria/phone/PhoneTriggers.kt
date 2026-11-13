package app.talaria.phone

import android.bluetooth.BluetoothDevice
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.net.wifi.WifiInfo
import android.os.BatteryManager
import android.os.Build
import android.os.Handler
import android.os.Looper
import androidx.core.content.ContextCompat
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/** One thing the server asked this phone to watch for. */
data class TriggerSpec(val id: String, val kind: String, val match: String, val level: Int)

/**
 * Watches for the events the agent was asked to react to (battery low, charger in or out, a Wi-Fi network joined, a Bluetooth
 * device connected) and tells [fire] when one happens. It only listens while at least one thing is asked for, with
 * system broadcasts that cost nothing while nothing happens. It reports the event, never what else is on the phone.
 */
class PhoneTriggers(private val ctx: Context, private val fire: (id: String, kind: String, data: JsonObject) -> Unit) {
    private val main = Handler(Looper.getMainLooper())
    @Volatile private var specs: List<TriggerSpec> = emptyList()
    private var listening = false
    private var lastLevel = -1
    private var lastSsid = ""
    private val cm: ConnectivityManager = ctx.getSystemService(ConnectivityManager::class.java)

    /** Replaces what is watched for; an empty list stops all listening. */
    fun set(list: List<TriggerSpec>) {
        specs = list
        main.post { sync() }
    }

    fun stop() = set(emptyList())

    private fun of(kind: String) = specs.filter { it.kind == kind }

    private fun sync() {
        val want = specs.isNotEmpty()
        if (want && !listening) start() else if (!want && listening) halt()
    }

    private val battery = object : BroadcastReceiver() {
        override fun onReceive(c: Context, i: Intent) {
            val scale = i.getIntExtra(BatteryManager.EXTRA_SCALE, 100).coerceAtLeast(1)
            val level = i.getIntExtra(BatteryManager.EXTRA_LEVEL, -1) * 100 / scale
            val plugged = i.getIntExtra(BatteryManager.EXTRA_PLUGGED, 0) != 0
            val before = lastLevel
            lastLevel = level
            if (level < 0 || plugged || before < 0) return
            // it fires on the way down past the level, not on every percent below it
            of("battery_low").filter { before > it.level.coerceAtLeast(1) && level <= it.level }.forEach { fire(it.id, it.kind, buildJsonObject { put("level", level) }) }
        }
    }

    private val power = object : BroadcastReceiver() {
        override fun onReceive(c: Context, i: Intent) {
            val kind = if (i.action == Intent.ACTION_POWER_CONNECTED) "charging_start" else "charging_stop"
            of(kind).forEach { fire(it.id, it.kind, buildJsonObject {}) }
        }
    }

    private val bluetooth = object : BroadcastReceiver() {
        @Suppress("DEPRECATION")
        override fun onReceive(c: Context, i: Intent) {
            val device = if (Build.VERSION.SDK_INT >= 33) i.getParcelableExtra(BluetoothDevice.EXTRA_DEVICE, BluetoothDevice::class.java) else i.getParcelableExtra<BluetoothDevice>(BluetoothDevice.EXTRA_DEVICE)
            val name = try { device?.name.orEmpty() } catch (_: SecurityException) { "" } // without the permission a device has no name here
            of("bluetooth").filter { it.match.isBlank() || it.match.equals(name, ignoreCase = true) }.forEach { fire(it.id, it.kind, buildJsonObject { put("name", name) }) }
        }
    }

    private fun wifiChanged(caps: NetworkCapabilities) {
        // The network's name is only given from Android 10, and only with the location permission.
        val info = (if (Build.VERSION.SDK_INT >= 29) caps.transportInfo as? WifiInfo else null) ?: return
        val ssid = info.ssid.orEmpty().trim('"').takeIf { it != "<unknown ssid>" }.orEmpty()
        if (ssid == lastSsid) return
        lastSsid = ssid
        // an empty name means Android is not telling this app the name: only "any network" triggers can match
        of("wifi").filter { it.match.isBlank() || (ssid.isNotEmpty() && it.match.equals(ssid, ignoreCase = true)) }.forEach { fire(it.id, it.kind, buildJsonObject { put("name", ssid) }) }
    }

    /** From Android 12 a callback must ask to be given the network's name; before that it is given without asking. */
    @androidx.annotation.RequiresApi(31)
    private inner class WifiCallback31 : ConnectivityManager.NetworkCallback(FLAG_INCLUDE_LOCATION_INFO) {
        override fun onCapabilitiesChanged(network: Network, caps: NetworkCapabilities) = wifiChanged(caps)
        override fun onLost(network: Network) { lastSsid = "" }
    }

    private inner class WifiCallbackOld : ConnectivityManager.NetworkCallback() {
        override fun onCapabilitiesChanged(network: Network, caps: NetworkCapabilities) = wifiChanged(caps)
        override fun onLost(network: Network) { lastSsid = "" }
    }

    private val wifi: ConnectivityManager.NetworkCallback by lazy { if (Build.VERSION.SDK_INT >= 31) WifiCallback31() else WifiCallbackOld() }

    private fun start() {
        listening = true
        val notExported = ContextCompat.RECEIVER_NOT_EXPORTED
        lastLevel = -1
        runCatching { ContextCompat.registerReceiver(ctx, battery, IntentFilter(Intent.ACTION_BATTERY_CHANGED), notExported) }
        runCatching { ContextCompat.registerReceiver(ctx, power, IntentFilter().apply { addAction(Intent.ACTION_POWER_CONNECTED); addAction(Intent.ACTION_POWER_DISCONNECTED) }, notExported) }
        runCatching { ContextCompat.registerReceiver(ctx, bluetooth, IntentFilter(BluetoothDevice.ACTION_ACL_CONNECTED), notExported) }
        runCatching { cm.registerNetworkCallback(NetworkRequest.Builder().addTransportType(NetworkCapabilities.TRANSPORT_WIFI).build(), wifi) }
    }

    private fun halt() {
        listening = false
        runCatching { ctx.unregisterReceiver(battery) }
        runCatching { ctx.unregisterReceiver(power) }
        runCatching { ctx.unregisterReceiver(bluetooth) }
        runCatching { cm.unregisterNetworkCallback(wifi) }
        lastSsid = ""
    }

    companion object {
        /** Words for an event, for the "recently asked" list. */
        fun words(kind: String, data: JsonObject): String = when (kind) {
            "battery_low" -> "Battery at ${(data["level"] as? JsonPrimitive)?.content ?: "?"}%"
            "charging_start" -> "Charger plugged in"
            "charging_stop" -> "Charger unplugged"
            "wifi" -> "Joined Wi-Fi ${(data["name"] as? JsonPrimitive)?.content.orEmpty()}".trim()
            "bluetooth" -> "Connected to ${(data["name"] as? JsonPrimitive)?.content.orEmpty().ifBlank { "a Bluetooth device" }}"
            else -> kind
        }
    }
}
