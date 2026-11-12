package app.talaria.phone

import android.Manifest
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.PackageManager
import android.location.Location
import android.location.LocationManager
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import android.os.BatteryManager
import android.os.Build
import android.os.Environment
import android.os.PowerManager
import android.os.StatFs
import android.provider.CalendarContract
import android.provider.ContactsContract
import android.net.Uri
import androidx.core.content.ContextCompat
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.coroutines.withTimeoutOrNull
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonArray
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.add
import kotlinx.serialization.json.put
import java.time.Instant
import java.time.ZoneId
import java.time.ZoneOffset
import kotlin.coroutines.resume

/** What Talaria may be asked to read from this phone. Every one is read-only and every one is off until the person turns it on. */
object Op {
    const val STATUS = "status"
    const val LOCATION = "location"
    const val CONTACTS = "contacts"
    const val CALENDAR = "calendar"
    const val NOTIFICATIONS = "notifications"
    val all = listOf(STATUS, LOCATION, CONTACTS, CALENDAR, NOTIFICATIONS)
}

/** Why a request could not be answered, in words the agent can pass on. */
class Refused(message: String) : Exception(message)

object PhoneOps {
    /** The Android permissions an operation needs (any one of them will do), empty when it needs none. */
    fun permissions(op: String): List<String> = when (op) {
        Op.LOCATION -> listOf(Manifest.permission.ACCESS_COARSE_LOCATION, Manifest.permission.ACCESS_FINE_LOCATION)
        Op.CONTACTS -> listOf(Manifest.permission.READ_CONTACTS)
        Op.CALENDAR -> listOf(Manifest.permission.READ_CALENDAR)
        else -> emptyList()
    }

    /** Whether the phone would answer [op] right now: its permission (or special access) is in place. */
    fun permitted(ctx: Context, op: String): Boolean = when (op) {
        Op.NOTIFICATIONS -> PhoneNotificationListener.enabled(ctx)
        else -> permissions(op).let { p -> p.isEmpty() || p.any { ContextCompat.checkSelfPermission(ctx, it) == PackageManager.PERMISSION_GRANTED } }
    }

    /**
     * Android gives a closed app no location unless the person has allowed it "all the time". The agent usually asks
     * when the app is not on screen, so without this, location only works while Talaria is open.
     */
    fun backgroundLocation(ctx: Context): Boolean =
        Build.VERSION.SDK_INT < 29 || ContextCompat.checkSelfPermission(ctx, Manifest.permission.ACCESS_BACKGROUND_LOCATION) == PackageManager.PERMISSION_GRANTED

    suspend fun run(ctx: Context, op: String, args: JsonObject): JsonElement {
        if (!permitted(ctx, op)) throw Refused("Android has not given Talaria access to this on the phone.")
        return when (op) {
            Op.STATUS -> status(ctx)
            Op.LOCATION -> location(ctx)
            Op.CONTACTS -> contacts(ctx, str(args, "query"))
            Op.CALENDAR -> calendar(ctx, (args["days"] as? JsonPrimitive)?.content?.toIntOrNull()?.coerceIn(1, 14) ?: 2)
            Op.NOTIFICATIONS -> notifications(ctx, str(args, "query"))
            else -> throw Refused("This phone does not know that request.")
        }
    }

    private fun str(o: JsonObject, k: String) = (o[k] as? JsonPrimitive)?.content.orEmpty().trim()

    // ---- status: the phone itself, no permission needed ----

    fun status(ctx: Context): JsonElement {
        val bm = ctx.getSystemService(BatteryManager::class.java)
        val sticky = ctx.registerReceiver(null, IntentFilter(Intent.ACTION_BATTERY_CHANGED))
        val plugged = sticky?.getIntExtra(BatteryManager.EXTRA_PLUGGED, 0) ?: 0
        val cm = ctx.getSystemService(ConnectivityManager::class.java)
        val caps = cm.getNetworkCapabilities(cm.activeNetwork)
        val link = when {
            caps == null -> "offline"
            caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) -> "wifi"
            caps.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) -> "mobile data"
            caps.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) -> "ethernet"
            else -> "other"
        }
        val pm = ctx.getSystemService(PowerManager::class.java)
        val nm = ctx.getSystemService(android.app.NotificationManager::class.java)
        val dnd = when (nm.currentInterruptionFilter) {
            android.app.NotificationManager.INTERRUPTION_FILTER_ALL -> "off"
            android.app.NotificationManager.INTERRUPTION_FILTER_PRIORITY -> "priority only"
            android.app.NotificationManager.INTERRUPTION_FILTER_ALARMS -> "alarms only"
            android.app.NotificationManager.INTERRUPTION_FILTER_NONE -> "total silence"
            else -> "unknown"
        }
        val free = StatFs(Environment.getDataDirectory().path).availableBytes
        val zone = ZoneId.systemDefault()
        return buildJsonObject {
            put("device", "${Build.MANUFACTURER} ${Build.MODEL}")
            put("android", Build.VERSION.RELEASE)
            put("batteryPercent", bm.getIntProperty(BatteryManager.BATTERY_PROPERTY_CAPACITY))
            put("charging", plugged != 0)
            put("batterySaver", pm.isPowerSaveMode)
            put("network", link)
            put("metered", cm.isActiveNetworkMetered)
            put("freeStorageGB", Math.round(free / 1.0e8) / 10.0)
            put("doNotDisturb", dnd)
            put("screenOn", pm.isInteractive)
            put("timeZone", zone.id)
            put("localTime", java.time.OffsetDateTime.now(zone).withNano(0).toString())
        }
    }

    // ---- location: the last known position, from the phone's own sensors (no Google services) ----

    @android.annotation.SuppressLint("MissingPermission")
    suspend fun location(ctx: Context): JsonElement {
        val lm = ctx.getSystemService(LocationManager::class.java)
        val fine = ContextCompat.checkSelfPermission(ctx, Manifest.permission.ACCESS_FINE_LOCATION) == PackageManager.PERMISSION_GRANTED
        // Without the precise permission, Android itself blurs what it gives. The providers that need it are not asked.
        val names = lm.getProviders(true).filter { fine || it == LocationManager.NETWORK_PROVIDER || it == LocationManager.PASSIVE_PROVIDER }
        var best: Location? = names.mapNotNull { runCatching { lm.getLastKnownLocation(it) }.getOrNull() }.maxByOrNull { it.time }
        val fresh = best != null && System.currentTimeMillis() - best.time < 10 * 60_000
        if (!fresh && Build.VERSION.SDK_INT >= 30) {
            val p = names.firstOrNull { it == LocationManager.NETWORK_PROVIDER } ?: names.firstOrNull { it != LocationManager.PASSIVE_PROVIDER }
            if (p != null) {
                val now = withTimeoutOrNull(8_000) {
                    suspendCancellableCoroutine<Location?> { c ->
                        val signal = android.os.CancellationSignal()
                        c.invokeOnCancellation { signal.cancel() }
                        lm.getCurrentLocation(p, signal, ContextCompat.getMainExecutor(ctx)) { c.resume(it) }
                    }
                }
                if (now != null) best = now
            }
        }
        val loc = best ?: throw Refused(
            if (!backgroundLocation(ctx)) "Android only lets Talaria look up the location while the app is open. The person can allow location all the time in Talaria's phone settings, or open Talaria and ask again."
            else "The phone has no position yet. Location may be switched off in its quick settings.",
        )
        return buildJsonObject {
            // Three decimals is about a hundred metres: enough to say where, not to name a doorway.
            put("latitude", Math.round(loc.latitude * 1000) / 1000.0)
            put("longitude", Math.round(loc.longitude * 1000) / 1000.0)
            put("accuracyMeters", loc.accuracy.toInt())
            put("minutesAgo", ((System.currentTimeMillis() - loc.time) / 60_000).coerceAtLeast(0))
            put("source", loc.provider ?: "unknown")
        }
    }

    // ---- contacts: a lookup by name, never the whole book ----

    fun contacts(ctx: Context, query: String): JsonElement {
        if (query.isBlank()) throw Refused("Say whose contact to look for.")
        class Person(var name: String = "", val phones: MutableSet<String> = linkedSetOf(), val emails: MutableSet<String> = linkedSetOf())
        // Grouped by the contact's id, not its name: a row can come back without one.
        val people = linkedMapOf<Long, Person>()
        fun person(id: Long, name: String?) = people.getOrPut(id) { Person() }.also { if (it.name.isBlank() && !name.isNullOrBlank()) it.name = name }
        ctx.contentResolver.query(
            Uri.withAppendedPath(ContactsContract.CommonDataKinds.Phone.CONTENT_FILTER_URI, Uri.encode(query)),
            arrayOf(ContactsContract.CommonDataKinds.Phone.CONTACT_ID, ContactsContract.CommonDataKinds.Phone.DISPLAY_NAME, ContactsContract.CommonDataKinds.Phone.NUMBER), null, null, null,
        )?.use { c -> while (c.moveToNext() && people.size < 8) person(c.getLong(0), c.getString(1)).phones.add(c.getString(2).orEmpty()) }
        ctx.contentResolver.query(
            Uri.withAppendedPath(ContactsContract.CommonDataKinds.Email.CONTENT_FILTER_URI, Uri.encode(query)),
            arrayOf(ContactsContract.CommonDataKinds.Email.CONTACT_ID, ContactsContract.CommonDataKinds.Email.DISPLAY_NAME, ContactsContract.CommonDataKinds.Email.ADDRESS), null, null, null,
        )?.use { c -> while (c.moveToNext() && people.size < 8) person(c.getLong(0), c.getString(1)).emails.add(c.getString(2).orEmpty()) }
        return buildJsonObject {
            put("matches", buildJsonArray {
                people.values.forEach { p ->
                    add(buildJsonObject {
                        put("name", p.name)
                        put("phones", JsonArray(p.phones.take(4).map { JsonPrimitive(it) }))
                        put("emails", JsonArray(p.emails.take(3).map { JsonPrimitive(it) }))
                    })
                }
            })
        }
    }

    // ---- calendar: the phone's own calendars (the server may have others) ----

    fun calendar(ctx: Context, days: Int): JsonElement {
        val zone = ZoneId.systemDefault()
        val now = System.currentTimeMillis()
        val end = now + days * 86_400_000L
        val events = mutableListOf<JsonObject>()
        CalendarContract.Instances.query(
            ctx.contentResolver,
            arrayOf(CalendarContract.Instances.TITLE, CalendarContract.Instances.BEGIN, CalendarContract.Instances.END, CalendarContract.Instances.ALL_DAY, CalendarContract.Instances.EVENT_LOCATION, CalendarContract.Instances.CALENDAR_DISPLAY_NAME),
            now, end,
        )?.use { c ->
            while (c.moveToNext() && events.size < 30) {
                val allDay = c.getInt(3) == 1
                val b = Instant.ofEpochMilli(c.getLong(1)); val e = Instant.ofEpochMilli(c.getLong(2))
                events += buildJsonObject {
                    put("title", c.getString(0).orEmpty().take(120))
                    // An all-day event is stored as midnight to midnight in UTC: shown as dates, it lands on the right days.
                    if (allDay) { put("allDay", true); put("date", b.atZone(ZoneOffset.UTC).toLocalDate().toString()) }
                    else { put("start", b.atZone(zone).toOffsetDateTime().withNano(0).toString()); put("end", e.atZone(zone).toOffsetDateTime().withNano(0).toString()) }
                    c.getString(4)?.takeIf { it.isNotBlank() }?.let { put("where", it.take(120)) }
                    c.getString(5)?.takeIf { it.isNotBlank() }?.let { put("calendar", it.take(60)) }
                }
            }
        }
        return buildJsonObject { put("days", days); put("events", JsonArray(events.sortedBy { (it["start"] ?: it["date"]).toString() })) }
    }

    // ---- notifications: what is showing now ----

    fun notifications(ctx: Context, query: String): JsonElement {
        val live = PhoneNotificationListener.live ?: throw Refused("Notification access is on but not connected yet. Try again in a moment.")
        val q = query.lowercase()
        val own = ctx.packageName
        val pm = ctx.packageManager
        val rows = runCatching { live.activeNotifications }.getOrNull().orEmpty()
            .filter { it.packageName != own && !it.isOngoing && it.notification.visibility != android.app.Notification.VISIBILITY_SECRET && (it.notification.flags and android.app.Notification.FLAG_GROUP_SUMMARY) == 0 }
            .sortedByDescending { it.postTime }
            .map {
                val ex = it.notification.extras
                val app = runCatching { pm.getApplicationLabel(pm.getApplicationInfo(it.packageName, 0)).toString() }.getOrDefault(it.packageName)
                val title = ex.getCharSequence(android.app.Notification.EXTRA_TITLE)?.toString().orEmpty()
                val text = (ex.getCharSequence(android.app.Notification.EXTRA_BIG_TEXT) ?: ex.getCharSequence(android.app.Notification.EXTRA_TEXT))?.toString().orEmpty()
                Triple(app, title, text) to it.postTime
            }
            .filter { q.isEmpty() || "${it.first.first} ${it.first.second} ${it.first.third}".lowercase().contains(q) }
            .take(25)
        return buildJsonObject {
            put("notifications", buildJsonArray {
                rows.forEach { (t, at) ->
                    add(buildJsonObject {
                        put("app", t.first); put("title", t.second.take(120)); put("text", t.third.take(300))
                        put("minutesAgo", ((System.currentTimeMillis() - at) / 60_000).coerceAtLeast(0))
                    })
                }
            })
        }
    }
}
