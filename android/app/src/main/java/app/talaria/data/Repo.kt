package app.talaria.data

import android.content.Context
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.delay
import kotlinx.coroutines.withTimeoutOrNull
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asSharedFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import java.io.IOException

sealed interface Conn {
    data object SignedOut : Conn
    data object Connecting : Conn
    data object Online : Conn
    data class Offline(val reason: String) : Conn
}

/**
 * The conversation as the phone knows it, kept current over the server's event stream. It lives as long as the app's
 * process does, so a notification can answer an approval with no screen open.
 */
class Repo(context: Context) {
    val store = SessionStore(context)
    val api = Api(store).also { it.onAuthLost = { _sessionEnded.value = true; signOutLocal(ended = true) } }
    private val _sessionEnded = MutableStateFlow(false)
    /** The server stopped accepting this phone's session: the sign-in screen says so. */
    val sessionEnded: StateFlow<Boolean> = _sessionEnded.asStateFlow()

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
    private var loop: Job? = null
    /** Counts server switches, so an answer that was in flight from the old server is not shown on the new one. */
    @Volatile private var generation = 0
    /** Cuts the wait before the next try short: the network came back. */
    private val wake = kotlinx.coroutines.channels.Channel<Unit>(kotlinx.coroutines.channels.Channel.CONFLATED)
    fun nudge() { wake.trySend(Unit) }

    private val _base = MutableStateFlow(store.baseUrl)
    /** The server this phone is talking to now. */
    val base: StateFlow<String> = _base.asStateFlow()
    private val _servers = MutableStateFlow(store.savedServers)
    /** Every server the phone has a session for. */
    val servers: StateFlow<List<String>> = _servers.asStateFlow()
    private val _adding = MutableStateFlow(false)
    /** The sign-in screen is showing to add another server, while one is still signed in. */
    val adding: StateFlow<Boolean> = _adding.asStateFlow()
    fun beginAdd() { _adding.value = true }
    fun cancelAdd() { _adding.value = false }

    private val _conn = MutableStateFlow<Conn>(if (store.signedIn) Conn.Connecting else Conn.SignedOut)
    val conn: StateFlow<Conn> = _conn.asStateFlow()

    private val _messages = MutableStateFlow<List<Message>>(emptyList())
    val messages: StateFlow<List<Message>> = _messages.asStateFlow()
    private val _running = MutableStateFlow(false)
    val running: StateFlow<Boolean> = _running.asStateFlow()
    private val _typing = MutableStateFlow(false)
    val typing: StateFlow<Boolean> = _typing.asStateFlow()
    private val _workers = MutableStateFlow<List<Worker>>(emptyList())
    val workers: StateFlow<List<Worker>> = _workers.asStateFlow()
    private val _paused = MutableStateFlow(false)
    val paused: StateFlow<Boolean> = _paused.asStateFlow()
    private val _model = MutableStateFlow("")
    val model: StateFlow<String> = _model.asStateFlow()
    private val _profile = MutableStateFlow(Profile())
    val profile: StateFlow<Profile> = _profile.asStateFlow()
    private val _channel = MutableStateFlow<Channel?>(null)
    val channel: StateFlow<Channel?> = _channel.asStateFlow()
    private val _suggestion = MutableStateFlow<SuggestionDto?>(null)
    val suggestion: StateFlow<SuggestionDto?> = _suggestion.asStateFlow()
    private val _version = MutableStateFlow("")
    val version: StateFlow<String> = _version.asStateFlow()
    private val _chats = MutableStateFlow<List<ChatMeta>>(emptyList())
    val chats: StateFlow<List<ChatMeta>> = _chats.asStateFlow()

    private val _pinned = MutableStateFlow(store.pinned)
    val pinned: StateFlow<Set<String>> = _pinned.asStateFlow()
    fun togglePin(id: String) { val next = if (id in _pinned.value) _pinned.value - id else _pinned.value + id; store.pinned = next; _pinned.value = next }

    private val _toasts = MutableSharedFlow<String>(extraBufferCapacity = 8)
    /** Short messages for the screen to show ("Saved", "Copied"). */
    val toasts: SharedFlow<String> = _toasts.asSharedFlow()
    fun toast(text: String) { _toasts.tryEmit(text) }

    private val _theme = MutableStateFlow(store.theme)
    val theme: StateFlow<String> = _theme.asStateFlow()
    fun setTheme(mode: String) { store.theme = mode; _theme.value = mode }

    /** Every event, for the notifier and anything else that wants to react as things happen. */
    private val _events = MutableSharedFlow<EventDto>(extraBufferCapacity = 128)
    val events: SharedFlow<EventDto> = _events.asSharedFlow()

    /** Set by the activity so notifications know whether the person is already looking. */
    @Volatile var foreground = false

    val pendingApprovals: List<Message> get() = _messages.value.filter { it.role == "approval" && it.state == "pending" }

    fun start() {
        if (!store.signedIn) { _conn.value = Conn.SignedOut; return }
        if (loop?.isActive == true) return
        loop = scope.launch { connectionLoop() }
    }

    fun stop() { loop?.cancel(); loop = null }

    /** Signs in to [origin]; on success the connection starts. */
    suspend fun signIn(input: String, password: String) {
        val origin = Servers.normalize(input)
        val cookie = api.login(origin, password)
        // Another server may be live: let it go before the credentials change under it.
        if (store.baseUrl != origin) { stop(); resetState() }
        store.baseUrl = origin
        store.cookie = cookie
        store.remember(origin, cookie)
        store.lastServers = origin
        _base.value = origin
        _servers.value = store.savedServers
        _adding.value = false
        _conn.value = Conn.Connecting
        _sessionEnded.value = false
        start()
    }

    private fun resetState() {
        generation++
        _messages.value = emptyList(); _workers.value = emptyList(); _running.value = false; _typing.value = false
        _chats.value = emptyList(); _archived.value = emptyList(); _channel.value = null; _suggestion.value = null
    }

    /** Forgets the live server's session. With another server saved, the phone moves to it instead of the sign-in screen. */
    fun signOutLocal(ended: Boolean = false) {
        val gone = store.baseUrl
        stop()
        store.signOut()
        resetState()
        _servers.value = store.savedServers
        val next = store.savedServers.firstOrNull { it != gone }
        if (next != null && switchTo(next)) {
            toast(if (ended) "Your session on ${Servers.display(gone)} ended. Now on ${Servers.display(next)}." else "Signed out of ${Servers.display(gone)}.")
            return
        }
        _base.value = ""
        _conn.value = Conn.SignedOut
    }

    suspend fun signOut() {
        runCatching { api.post("/api/logout") }
        signOutLocal()
    }

    /** Makes another saved server the live one. */
    fun switchTo(origin: String): Boolean {
        if (origin == store.baseUrl && store.cookie.isNotEmpty()) return true
        if (!store.activate(origin)) return false
        stop(); resetState()
        _sessionEnded.value = false; _adding.value = false
        _base.value = origin
        _conn.value = Conn.Connecting
        start()
        return true
    }

    /** Signs out of one saved server, live or not. */
    suspend fun forgetServer(origin: String) {
        if (origin == store.baseUrl) { signOut(); return }
        val c = store.cookieFor(origin)
        if (c.isNotEmpty()) runCatching { Api(fixedCredentials(origin, c)).post("/api/logout") }
        store.forget(origin)
        _servers.value = store.savedServers
    }

    private suspend fun connectionLoop() {
        var backoff = 1000L
        var everOnline = false
        while (currentCoroutineContext().isActive && store.signedIn) {
            try {
                _conn.value = if (everOnline) Conn.Offline("Reconnecting…") else Conn.Connecting
                val st = api.getJson<StateDto>("/api/state")
                applyState(st)
                everOnline = true
                _conn.value = Conn.Online
                backoff = 1000
                announceWaiting(st.messages)
                scope.launch { runCatching { refreshChats() } }
                // Pausing is not announced on the event stream, so it is looked at now and then.
                coroutineScope {
                    val watch = launch { while (true) { delay(20_000); runCatching { _paused.value = api.getJson<ControlDto>("/api/control").paused } } }
                    try { stream(st.epoch, st.seq) } finally { watch.cancel() }
                }
            } catch (e: CancellationException) {
                throw e
            } catch (e: AuthLost) {
                return
            } catch (e: Exception) {
                _conn.value = Conn.Offline(e.message ?: "The connection failed.")
            }
            if (_conn.value is Conn.Online) _conn.value = Conn.Offline("Reconnecting…")
            // The wait ends early when the network comes back.
            withTimeoutOrNull(backoff) { wake.receive() }
            backoff = (backoff * 2).coerceAtMost(30_000)
        }
    }

    /**
     * An approval that arrived while the phone could not hear the server never came as an event. Looking at the
     * state again after a reconnect finds it, and the notifier (which remembers what it has shown) announces it.
     */
    private fun announceWaiting(messages: List<Message>) {
        messages.filter { it.role == "approval" && it.state == "pending" && it.token.isNotEmpty() }
            .forEach { _events.tryEmit(EventDto(type = "msg", msg = it)) }
    }

    private fun applyState(st: StateDto) {
        _messages.value = st.messages
        _running.value = st.running
        _workers.value = st.agents
        _paused.value = st.paused
        _model.value = st.model
        _profile.value = st.profile
        _channel.value = st.channel
        _suggestion.value = st.suggestion
        _version.value = st.version
        _typing.value = false
    }

    private suspend fun stream(epoch: Long, after: Long) = withContext(Dispatchers.IO) {
        val call = api.streamCall("/api/events?epoch=$epoch&after=$after")
        val reg = currentCoroutineContext()[Job]!!.invokeOnCompletion { call.cancel() }
        try {
            api.checkStream(call.execute()).use { resp ->
                val src = resp.body?.source() ?: throw IOException("The event stream had no body.")
                while (currentCoroutineContext().isActive) {
                    val line = src.readUtf8Line() ?: break
                    if (!line.startsWith("data:")) continue
                    val ev = runCatching { AppJson.decodeFromString<EventDto>(line.removePrefix("data:").trim()) }.getOrNull() ?: continue
                    onEvent(ev)
                }
            }
        } finally { reg.dispose() }
    }

    private fun onEvent(ev: EventDto) {
        when (ev.type) {
            "msg" -> ev.msg?.let { m ->
                _messages.update { list -> upsert(list, m) }
                if (m.role == "assistant" || m.role == "approval") _typing.value = false
            }
            "typing" -> _typing.value = true
            "run" -> {
                // An event with no "on" is the run ending (it may carry the model that answered).
                if (ev.model.isNotEmpty()) _model.value = ev.model
                _running.value = ev.on
                if (!ev.on) _typing.value = false
            }
            "agents" -> _workers.value = ev.agents.orEmpty()
            "reset" -> scope.launch {
                val g = generation
                runCatching { api.getJson<StateDto>("/api/state") }.onSuccess { if (g == generation) { applyState(it); refreshChats() } }
            }
        }
        _events.tryEmit(ev)
    }

    // ---- actions ----

    private val _uploading = MutableStateFlow(false)
    val uploading: StateFlow<Boolean> = _uploading.asStateFlow()

    /** Reads what the person chose and sends it to the server. Photos and PDFs only, as on the web. */
    suspend fun uploadFrom(resolver: android.content.ContentResolver, uri: android.net.Uri) {
        _uploading.value = true
        try {
            val (name, bytes, mime) = withContext(Dispatchers.IO) {
                val type = resolver.getType(uri).orEmpty().ifBlank { "application/octet-stream" }
                var n = "file"
                resolver.query(uri, arrayOf(android.provider.OpenableColumns.DISPLAY_NAME), null, null, null)?.use { c -> if (c.moveToFirst()) n = c.getString(0) ?: n }
                val data = resolver.openInputStream(uri)?.use { it.readBytes() } ?: throw IOException("That file could not be read.")
                Triple(n, data, type)
            }
            if (!(mime.startsWith("image/") || mime == "application/pdf")) throw IOException("Only photos and PDFs can be sent.")
            api.upload(name, mime, bytes)
        } finally { _uploading.value = false }
    }

    suspend fun send(text: String) { api.postJson("/api/send", buildJsonObject { put("text", text) }) }

    /** Answers an approval: choice is allow, always or deny. */
    suspend fun decide(token: String, choice: String) {
        api.postJson("/api/approve", buildJsonObject { put("token", token); put("choice", choice) })
    }

    private val _archived = MutableStateFlow<List<ChatMeta>>(emptyList())
    val archived: StateFlow<List<ChatMeta>> = _archived.asStateFlow()

    suspend fun refreshChats() {
        val g = generation
        val live = api.getJson<ChatsDto>("/api/chats").chats.filter { !it.archived }
        // Shelved conversations are listed separately by the server.
        val shelved = runCatching { api.getJson<ChatsDto>("/api/chats?archived=1").chats }.getOrDefault(emptyList())
        if (g != generation) return // the phone moved to another server while this was on its way
        _chats.value = live
        _archived.value = shelved
    }

    suspend fun chatAction(body: Map<String, String>) {
        api.postJson("/api/chats", buildJsonObject { body.forEach { (k, v) -> put(k, JsonPrimitive(v)) } })
        refreshChats()
    }

    suspend fun newChat() {
        api.postJson("/api/chats", buildJsonObject { put("new", true) })
        reload()
    }

    suspend fun openChat(id: String) {
        api.postJson("/api/chats", buildJsonObject { put("open", id) })
        reload()
    }

    suspend fun setPaused(paused: Boolean) {
        api.postJson("/api/pause", buildJsonObject { put("paused", paused) })
        _paused.value = paused
    }

    suspend fun reload() {
        applyState(api.getJson<StateDto>("/api/state"))
        refreshChats()
    }
}

/** A message from the event stream replaces the one with the same id (a tool finishing, an approval answered), or is added. */
fun upsert(list: List<Message>, m: Message): List<Message> {
    val i = list.indexOfFirst { it.id == m.id }
    return if (i >= 0) list.toMutableList().also { it[i] = m } else list + m
}

/** Credentials for one server that is not the live one (to sign out of it, or to look at it from the background). */
fun fixedCredentials(origin: String, cookieValue: String): Credentials = object : Credentials {
    override val baseUrl = origin
    override val cookie = cookieValue
}
