package app.talaria.phone

import android.provider.AlarmClock
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import java.net.URLEncoder
import java.time.LocalDateTime
import java.time.OffsetDateTime
import java.time.ZoneId

/**
 * What the agent may ask this phone to DO, read and checked. This file has no Android objects in it on purpose, so every
 * rule here can be tested without a phone: a request is turned into a [Plan] (an intent's action, address and extras as
 * plain values), and [PhoneWrites] only hands that plan to Android.
 *
 * The limits repeat the server's. The phone does not rely on the server having checked: whatever arrives is read again,
 * and a request that does not fit is refused. Text is only ever data: it becomes a label, a title, an address or a message
 * body, and is never read as a command.
 */
sealed interface Write {
    data class Alarm(val hour: Int, val minute: Int, val label: String) : Write
    data class Timer(val seconds: Int, val label: String) : Write
    /** [begin] is when it starts, in milliseconds. */
    data class Reminder(val title: String, val note: String, val begin: Long) : Write
    data class MapAt(val query: String, val lat: Double?, val lng: Double?) : Write
    data class SmsDraft(val to: String, val body: String) : Write
}

/** An intent, as plain values: what to start, with what address and extras. Nothing here can send, call or delete. */
data class Plan(val action: String, val data: String?, val extras: Map<String, Any>)

object WriteArgs {
    const val MAX_LABEL = 60
    const val MAX_TITLE = 100
    const val MAX_NOTE = 300
    const val MAX_QUERY = 200
    const val MAX_BODY = 480
    const val MAX_TIMER_SECONDS = 24 * 60 * 60
    private const val ONE_MINUTE = 60_000L
    private const val FIVE_YEARS = 5 * 366 * 86_400_000L
    private const val THIRTY_MINUTES = 30 * ONE_MINUTE

    private val number = Regex("^\\+?[0-9][0-9 ()\\-.]{1,22}$")

    /** Reads one request. Throws [Refused], with words the agent can pass on, when it does not fit. */
    fun parse(op: String, a: JsonObject, nowMs: Long = System.currentTimeMillis(), zone: ZoneId = ZoneId.systemDefault()): Write = when (op) {
        Op.ALARM -> Write.Alarm(whole(a, "hour", 0, 23), whole(a, "minute", 0, 59), tidy(text(a, "label"), MAX_LABEL, false))
        Op.TIMER -> Write.Timer(whole(a, "seconds", 1, MAX_TIMER_SECONDS), tidy(text(a, "label"), MAX_LABEL, false))
        Op.REMINDER -> {
            val title = tidy(text(a, "title"), MAX_TITLE, false)
            if (title.isEmpty()) throw Refused("A reminder needs a title.")
            Write.Reminder(title, tidy(text(a, "note"), MAX_NOTE, true), whenMs(text(a, "when"), nowMs, zone))
        }
        Op.MAP -> map(a)
        Op.SMS_DRAFT -> sms(a)
        else -> throw Refused("This phone does not do that.")
    }

    private fun map(a: JsonObject): Write {
        val query = tidy(text(a, "query"), MAX_QUERY, false)
        val lat = number(a, "lat"); val lng = number(a, "lng")
        if (lat != null || lng != null) {
            if (lat == null || lng == null || lat.isNaN() || lng.isNaN() || Math.abs(lat) > 90 || Math.abs(lng) > 180) throw Refused("A point needs both numbers: latitude from -90 to 90 and longitude from -180 to 180.")
            return Write.MapAt(query, lat, lng)
        }
        if (query.isEmpty()) throw Refused("Say which place to show.")
        return Write.MapAt(query, null, null)
    }

    private fun sms(a: JsonObject): Write {
        val to = text(a, "to").trim()
        val digits = to.count { it in '0'..'9' }
        if (!number.matches(to) || digits < 3 || digits > 15) throw Refused("A text needs a phone number to be written to.")
        val body = tidy(text(a, "body"), MAX_BODY + 1, true)
        if (body.isEmpty()) throw Refused("A text needs something written in it.")
        if (body.codePointCount(0, body.length) > MAX_BODY) throw Refused("That text is longer than $MAX_BODY characters.")
        return Write.SmsDraft(to, body)
    }

    // ---- the intent each request becomes ----

    /**
     * What to start. With [skipUi] an alarm or timer is set quietly by the clock app, which only works while Talaria is on
     * screen (or from a tap on a notice); every other kind opens an app for the person to look at.
     */
    fun plan(w: Write, skipUi: Boolean): Plan = when (w) {
        is Write.Alarm -> Plan(
            AlarmClock.ACTION_SET_ALARM, null,
            buildMap {
                put(AlarmClock.EXTRA_HOUR, w.hour); put(AlarmClock.EXTRA_MINUTES, w.minute)
                if (w.label.isNotEmpty()) put(AlarmClock.EXTRA_MESSAGE, w.label)
                put(AlarmClock.EXTRA_SKIP_UI, skipUi)
            },
        )
        is Write.Timer -> Plan(
            AlarmClock.ACTION_SET_TIMER, null,
            buildMap {
                put(AlarmClock.EXTRA_LENGTH, w.seconds)
                if (w.label.isNotEmpty()) put(AlarmClock.EXTRA_MESSAGE, w.label)
                put(AlarmClock.EXTRA_SKIP_UI, skipUi)
            },
        )
        // A new calendar entry, filled in. The calendar app asks the person to save it; nothing is written until they do.
        is Write.Reminder -> Plan(
            "android.intent.action.INSERT", "content://com.android.calendar/events",
            buildMap {
                put("title", w.title)
                if (w.note.isNotEmpty()) put("description", w.note)
                put("beginTime", w.begin); put("endTime", w.begin + THIRTY_MINUTES)
            },
        )
        is Write.MapAt -> Plan("android.intent.action.VIEW", geoUri(w), emptyMap())
        // ACTION_SENDTO opens the messaging app's compose screen with the text in place. It sends nothing: the person presses send.
        is Write.SmsDraft -> Plan("android.intent.action.SENDTO", "smsto:" + smsNumber(w.to), mapOf("sms_body" to w.body))
    }

    /** A geo: address. A place name is percent-encoded so it can only ever be a search, never more of the address. */
    fun geoUri(w: Write.MapAt): String {
        if (w.lat != null && w.lng != null) {
            val p = "${coord(w.lat)},${coord(w.lng)}"
            return "geo:$p?q=$p" + if (w.query.isNotEmpty()) "(" + encode(w.query) + ")" else ""
        }
        return "geo:0,0?q=" + encode(w.query)
    }

    /** Only the digits and a leading plus survive: no spaces, brackets or anything that could extend the address. */
    fun smsNumber(to: String): String = (if (to.trim().startsWith("+")) "+" else "") + to.filter { it in '0'..'9' }

    private fun coord(d: Double) = String.format(java.util.Locale.ROOT, "%.6f", d).trimEnd('0').trimEnd('.')

    private fun encode(s: String) = URLEncoder.encode(s, "UTF-8").replace("+", "%20")

    // ---- reading numbers, text and times ----

    private fun text(o: JsonObject, k: String): String = (o[k] as? JsonPrimitive)?.takeIf { it.isString }?.content.orEmpty()

    private fun number(o: JsonObject, k: String): Double? = (o[k] as? JsonPrimitive)?.takeIf { !it.isString }?.content?.toDoubleOrNull()

    private fun whole(o: JsonObject, k: String, lo: Int, hi: Int): Int {
        val v = number(o, k) ?: throw Refused("$k is needed.")
        if (v != Math.floor(v) || v < lo || v > hi) throw Refused("$k must be a whole number from $lo to $hi.")
        return v.toInt()
    }

    /** A reminder's time: with a zone it is that moment, without one it is this phone's own local time. It must still be to come. */
    private fun whenMs(s: String, nowMs: Long, zone: ZoneId): Long {
        val t = s.trim()
        val ms = runCatching { OffsetDateTime.parse(t).toInstant().toEpochMilli() }.getOrNull()
            ?: runCatching { LocalDateTime.parse(t).atZone(zone).toInstant().toEpochMilli() }.getOrNull()
            ?: throw Refused("The time of a reminder must be a date and time such as 2026-03-14T09:30:00+00:00.")
        if (ms < nowMs - ONE_MINUTE) throw Refused("That time has already passed.")
        if (ms > nowMs + FIVE_YEARS) throw Refused("That time is more than five years away.")
        return ms
    }

    /**
     * Removes what has no place in a label or a message: control characters and invisible formatting characters (which
     * can hide or reorder text in what the person reads), folds blanks together unless several lines are allowed, and
     * cuts to [max] characters.
     */
    fun tidy(s: String, max: Int, multiline: Boolean): String {
        val kept = StringBuilder()
        s.codePoints().forEach { cp ->
            val type = Character.getType(cp)
            when {
                cp == '\n'.code && multiline -> kept.append('\n')
                cp == '\n'.code || cp == '\t'.code || cp == '\r'.code -> kept.append(' ')
                type == Character.CONTROL.toInt() -> {}
                type == Character.FORMAT.toInt() && cp != 0x200D -> {}
                else -> kept.appendCodePoint(cp)
            }
        }
        val folded = if (multiline) kept.split('\n').joinToString("\n") { l -> l.trim().split(Regex("\\s+")).filter { it.isNotEmpty() }.joinToString(" ") }.trim()
        else kept.trim().split(Regex("\\s+")).filter { it.isNotEmpty() }.joinToString(" ")
        // cut on a whole character, not in the middle of a pair
        return if (folded.codePointCount(0, folded.length) > max) folded.substring(0, folded.offsetByCodePoints(0, max)).trim() else folded
    }
}
