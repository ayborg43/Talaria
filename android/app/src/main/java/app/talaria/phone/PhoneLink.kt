package app.talaria.phone

import android.content.Context
import android.os.Build
import app.talaria.data.Conn
import app.talaria.data.Repo
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.TimeoutCancellationException
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.collectLatest
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeout
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import org.json.JSONArray
import org.json.JSONObject

enum class LinkState { Off, Connecting, Linked }

/** One thing the agent asked of this phone, kept so the person can see what was read and when. */
data class PhoneEvent(val at: Long, val op: String, val ok: Boolean, val note: String)

/**
 * The phone's end of the agent's phone tools. While the person has switched at least one thing on and the app is
 * connected to its server, it holds one WebSocket open; the agent's requests arrive on it, are answered from the phone's
 * own data, and are written down here. Nothing is sent that was not asked for, and a request for something that is
 * switched off, or whose Android permission has been taken back, is refused on the phone whatever the server says.
 */
class PhoneLink(private val ctx: Context, private val repo: Repo) {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
    private val json = Json { ignoreUnknownKeys = true }

    private val _ops = MutableStateFlow(repo.store.phoneOps)
    /** What the person has switched on. */
    val ops: StateFlow<Set<String>> = _ops.asStateFlow()
    private val _state = MutableStateFlow(LinkState.Off)
    val state: StateFlow<LinkState> = _state.asStateFlow()
    private val _log = MutableStateFlow(loadLog())
    val log: StateFlow<List<PhoneEvent>> = _log.asStateFlow()
    @Volatile private var socket: WebSocket? = null

    /** The events the agent asked to be told about. They are watched for only while events are switched on and the link is up. */
    private val triggers: PhoneTriggers = PhoneTriggers(ctx) { id, kind, data ->
        val ws = socket
        if (ws != null && Op.TRIGGERS in _ops.value) {
            send(ws, buildJsonObject { put("t", "event"); put("trigger", id); put("data", data) })
            record(PhoneEvent(System.currentTimeMillis(), "trigger", true, PhoneTriggers.words(kind, data)))
        }
    }

    fun start() {
        scope.launch {
            combine(repo.conn, repo.base, _ops) { c, base, o -> Triple(c is Conn.Online, base, o.isNotEmpty()) }
                .distinctUntilChanged()
                .collectLatest { (online, _, any) -> if (online && any) connectLoop() else _state.value = LinkState.Off }
        }
    }

    fun setOps(next: Set<String>) {
        repo.store.phoneOps = next
        _ops.value = next
        if (Op.TRIGGERS !in next) triggers.stop()
        refresh()
    }

    /** Tells the server what is shared now: call after a permission was granted or taken back. */
    fun refresh() { socket?.let { send(it, buildJsonObject { put("t", "caps"); put("caps", caps()) }) } }

    /** What is switched on and also allowed by Android. */
    private fun caps() = JsonArray(_ops.value.filter { PhoneOps.permitted(ctx, it) }.map { JsonPrimitive(it) })

    private fun send(ws: WebSocket, o: JsonObject) { ws.send(o.toString()) }

    private suspend fun connectLoop() {
        var backoff = 1_000L
        while (currentCoroutineContext().isActive) {
            _state.value = LinkState.Connecting
            val closed = CompletableDeferred<Unit>()
            val ws = repo.api.openSocket("/api/phone/link", object : WebSocketListener() {
                override fun onOpen(webSocket: WebSocket, response: Response) {
                    socket = webSocket
                    send(webSocket, buildJsonObject { put("t", "hello"); put("name", "${Build.MANUFACTURER} ${Build.MODEL}"); put("caps", caps()) })
                    _state.value = LinkState.Linked
                    backoff = 1_000L
                }
                override fun onMessage(webSocket: WebSocket, text: String) { onRequest(webSocket, text) }
                override fun onClosing(webSocket: WebSocket, code: Int, reason: String) { webSocket.close(1000, null) }
                override fun onClosed(webSocket: WebSocket, code: Int, reason: String) { closed.complete(Unit) }
                override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) { closed.complete(Unit) }
            })
            try { closed.await() } finally { ws.cancel(); if (socket === ws) socket = null }
            // with no link there is no one to tell: stop listening until the server sends the list again
            triggers.stop()
            _state.value = LinkState.Connecting
            delay(backoff)
            backoff = (backoff * 2).coerceAtMost(30_000L)
        }
    }

    private fun onRequest(ws: WebSocket, text: String) {
        val m = runCatching { json.parseToJsonElement(text).jsonObject }.getOrNull() ?: return
        if (m["t"]?.jsonPrimitive?.content == "triggers") { // what to watch for, from the server: the whole list each time
            val list = (m["list"] as? kotlinx.serialization.json.JsonArray).orEmpty().mapNotNull { e ->
                val o = e as? JsonObject ?: return@mapNotNull null
                TriggerSpec(
                    o["id"]?.jsonPrimitive?.content ?: return@mapNotNull null, o["kind"]?.jsonPrimitive?.content ?: return@mapNotNull null,
                    o["match"]?.jsonPrimitive?.content.orEmpty(), o["level"]?.jsonPrimitive?.content?.toIntOrNull() ?: 0,
                )
            }
            triggers.set(if (Op.TRIGGERS in _ops.value) list else emptyList())
            return
        }
        if (m["t"]?.jsonPrimitive?.content != "req") return
        val id = m["id"]?.jsonPrimitive?.content?.toLongOrNull() ?: return
        val op = m["op"]?.jsonPrimitive?.content.orEmpty()
        val args = (m["args"] as? JsonObject) ?: JsonObject(emptyMap())
        scope.launch(Dispatchers.IO) {
            val result = runCatching {
                if (op !in _ops.value) throw Refused("The person has not turned this on on their phone.")
                withTimeout(20_000) { PhoneOps.run(ctx, op, args) }
            }
            val err = result.exceptionOrNull()
            record(PhoneEvent(System.currentTimeMillis(), op, err == null, if (err == null) "" else message(err)))
            send(ws, buildJsonObject {
                put("t", "res"); put("id", id); put("ok", err == null)
                if (err == null) put("data", result.getOrThrow()) else put("error", message(err))
            })
        }
    }

    private fun message(e: Throwable) = when (e) {
        is Refused -> e.message.orEmpty()
        is TimeoutCancellationException -> "The phone took too long to answer."
        is SecurityException -> "Android has not given Talaria access to this on the phone."
        else -> "The phone could not read that."
    }

    private fun record(e: PhoneEvent) {
        val next = (listOf(e) + _log.value).take(20)
        _log.value = next
        repo.store.phoneLog = JSONArray().also { a -> next.forEach { a.put(JSONObject().put("at", it.at).put("op", it.op).put("ok", it.ok).put("note", it.note)) } }.toString()
    }

    private fun loadLog(): List<PhoneEvent> = runCatching {
        val a = JSONArray(repo.store.phoneLog)
        (0 until a.length()).map { a.getJSONObject(it).let { o -> PhoneEvent(o.getLong("at"), o.getString("op"), o.getBoolean("ok"), o.optString("note")) } }
    }.getOrDefault(emptyList())
}
